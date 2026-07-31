const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')

const DEFAULT_ORIGIN = 'https://pay.ldxp.cn'
const MAX_DRAG_ATTEMPTS = 2
const BLOCKED_RESOURCE_TYPES = new Set(['image', 'media', 'font'])

class ChallengeError extends Error {
  constructor(state, message, options = {}) {
    super(message)
    this.name = 'ChallengeError'
    this.kind = 'verification'
    this.challengeState = state
    this.restartLane = Boolean(options.restartLane)
    if (options.cause) this.cause = options.cause
  }
}

function abortReason(signal) {
  return signal?.reason instanceof Error
    ? signal.reason
    : new Error(String(signal?.reason || 'operation aborted'))
}

function throwIfAborted(signal) {
  if (signal?.aborted) throw abortReason(signal)
}

function waitForPromiseOrAbort(promise, signal) {
  if (!signal) return Promise.resolve(promise)
  if (signal.aborted) return Promise.reject(abortReason(signal))

  return new Promise((resolve, reject) => {
    const onAbort = () => reject(abortReason(signal))
    signal.addEventListener('abort', onAbort, { once: true })
    Promise.resolve(promise).then(
      (value) => {
        signal.removeEventListener('abort', onAbort)
        resolve(value)
      },
      (error) => {
        signal.removeEventListener('abort', onAbort)
        reject(error)
      }
    )
  })
}

function combinedAbortSignal(signals) {
  const active = signals.filter(Boolean)
  if (active.length === 0) return undefined
  if (active.length === 1) return active[0]
  if (typeof AbortSignal.any === 'function') return AbortSignal.any(active)

  const controller = new AbortController()
  for (const signal of active) {
    if (signal.aborted) {
      controller.abort(abortReason(signal))
      break
    }
    signal.addEventListener('abort', () => controller.abort(abortReason(signal)), { once: true })
  }
  return controller.signal
}

class CancellableMutex {
  constructor() {
    this.locked = false
    this.queue = []
  }

  acquire(signal) {
    throwIfAborted(signal)
    if (!this.locked) {
      this.locked = true
      return Promise.resolve(this.releaseFunction())
    }

    return new Promise((resolve, reject) => {
      const entry = { cancelled: false, resolve, reject, signal, onAbort: null }
      entry.onAbort = () => {
        entry.cancelled = true
        reject(abortReason(signal))
      }
      signal?.addEventListener('abort', entry.onAbort, { once: true })
      this.queue.push(entry)
    })
  }

  releaseFunction() {
    let released = false
    return () => {
      if (released) return
      released = true
      this.release()
    }
  }

  release() {
    while (this.queue.length > 0) {
      const entry = this.queue.shift()
      entry.signal?.removeEventListener('abort', entry.onAbort)
      if (entry.cancelled || entry.signal?.aborted) continue
      entry.resolve(this.releaseFunction())
      return
    }
    this.locked = false
  }

  async runExclusive(task, signal) {
    const release = await this.acquire(signal)
    try {
      throwIfAborted(signal)
      return await task()
    } finally {
      release()
    }
  }
}

function challengeBackoffMilliseconds(failureCount, state = 'failed') {
  if (state === 'unsupported') return 6 * 60 * 60_000
  const delays = [15 * 60_000, 60 * 60_000, 6 * 60 * 60_000]
  const index = Math.min(Math.max(1, Number(failureCount) || 1), delays.length) - 1
  return delays[index]
}

function isChallengeError(error) {
  return error?.kind === 'verification' || error instanceof ChallengeError
}

function isHTTPCustomDenial(value) {
  return /denied\s+by\s+http_custom/i.test(String(value || ''))
}

function challengeTextDetected(title, text) {
  const normalizedTitle = String(title || '').trim()
  const normalizedText = String(text || '').replace(/\s+/g, ' ').slice(0, 2_000)
  return /^(?:verification|security verification|human verification|安全验证|人机验证)$/i.test(normalizedTitle)
    || /please\s+(?:slide|drag).{0,50}(?:verify|right|end)/i.test(normalizedText)
    || /verify\s+that\s+you\s+are\s+(?:a\s+)?real\s+person/i.test(normalizedText)
    || /(?:拖动|拖拽|滑动|滑块).{0,30}(?:验证|最右|右边|尽头)/i.test(normalizedText)
}

function challengeSnapshotDetected(snapshot) {
  if (!snapshot || typeof snapshot !== 'object') return false
  if (isHTTPCustomDenial(snapshot.responseError)) return true
  return (snapshot.frames || []).some((frame) => (
    frame?.hasCaptchaDOM
    || frame?.hasAliyunDOM
    || frame?.hasAliyunScript
    || challengeTextDetected(frame?.title, frame?.text)
  ))
}

function challengeCleared(snapshot) {
  if (!snapshot || typeof snapshot !== 'object' || isHTTPCustomDenial(snapshot.responseError)) return false
  return !(snapshot.frames || []).some((frame) => (
    frame?.hasCaptchaDOM
    || frame?.hasAliyunDOM
    || challengeTextDetected(frame?.title, frame?.text)
  ))
}

function aliyunSnapshotDetected(snapshot) {
  if (isHTTPCustomDenial(snapshot?.responseError)) return true
  return (snapshot?.frames || []).some((frame) => (
    frame?.hasAliyunDOM || frame?.hasAliyunScript
  ))
}

function selectChallengeProvider(snapshot, providers = defaultChallengeProviders()) {
  const classifiedSnapshot = {
    ...snapshot,
    isChallenge: challengeSnapshotDetected(snapshot),
  }
  if (!classifiedSnapshot.isChallenge) return null
  return providers.find((provider) => provider.detect(classifiedSnapshot)) || null
}

async function responseErrorHeader(response) {
  if (!response) return ''
  try {
    if (typeof response.headerValue === 'function') {
      return String(await response.headerValue('x-tengine-error') || '')
    }
    const headers = typeof response.headers === 'function' ? await response.headers() : response.headers
    return String(headers?.['x-tengine-error'] || headers?.['X-Tengine-Error'] || '')
  } catch {
    return ''
  }
}

async function collectChallengeSnapshot(page, response) {
  const frames = []
  for (const frame of page.frames()) {
    try {
      frames.push(await frame.evaluate(() => {
        const text = (document.body?.innerText || '').slice(0, 2_000)
        const scripts = Array.from(document.scripts || [])
          .map((script) => `${script.src || ''}\n${script.id || ''}\n${script.textContent || ''}`)
          .join('\n')
          .slice(0, 100_000)
        const hasAliyunDOM = Boolean(document.querySelector([
          '#aliyunCaptcha-sliding-slider',
          '#aliyunCaptcha-sliding-wrapper',
          '#captcha-element',
          '[id*="aliyunCaptcha"]',
          '[class*="aliyunCaptcha"]',
          '[class*="aliyun-captcha"]',
        ].join(',')))
        const hasCaptchaDOM = hasAliyunDOM || Boolean(document.querySelector([
          '[id*="captcha" i]',
          '[class*="captcha" i]',
          '[data-captcha]',
        ].join(',')))
        const hasGenericSlider = Boolean(document.querySelector([
          '[class*="slide" i]',
          '[class*="slider" i]',
          '[class*="drag" i]',
          '[aria-valuemax]',
        ].join(',')))
        return {
          url: location.href,
          title: document.title,
          text,
          hasAliyunDOM,
          hasCaptchaDOM,
          hasGenericSlider,
          hasAliyunScript: /(?:aliyun|alicloud|alibabacloud|alicdn).{0,80}captcha|captcha.{0,80}(?:aliyun|alicloud|alibabacloud|alicdn)|aliyuncaptcha|acw_sc__v2|\/awsc(?:\/|\.js)/i.test(scripts),
        }
      }))
    } catch {
      // Frames can navigate or detach while the challenge is rendering.
    }
  }
  const snapshot = {
    responseError: await responseErrorHeader(response),
    frames,
  }
  snapshot.isChallenge = challengeSnapshotDetected(snapshot)
  return snapshot
}

function validBox(box) {
  return box && [box.x, box.y, box.width, box.height].every(Number.isFinite)
    && box.width > 0 && box.height > 0
}

function computeDragDistance(trackBox, handleBox) {
  if (!validBox(trackBox) || !validBox(handleBox)) {
    throw new ChallengeError('unsupported', 'verification slider geometry is invalid')
  }
  const distance = trackBox.x + trackBox.width - (handleBox.x + handleBox.width)
  if (trackBox.width < 160 || handleBox.width < 18 || handleBox.width > Math.min(100, trackBox.width * 0.5)
    || distance < 60 || distance > trackBox.width) {
    throw new ChallengeError('unsupported', 'verification slider geometry is unsupported')
  }
  return distance
}

function boundedRandomInteger(minimum, maximum, random) {
  const value = Math.max(0, Math.min(0.999999999, Number(random()) || 0))
  return minimum + Math.floor(value * (maximum - minimum + 1))
}

function generateDragTrajectory(distance, options = {}) {
  if (!Number.isFinite(distance) || distance <= 0) throw new Error('drag distance must be positive')
  const random = options.random || Math.random
  const steps = Number.isInteger(options.steps)
    ? Math.max(36, Math.min(60, options.steps))
    : boundedRandomInteger(36, 60, random)
  const durationMilliseconds = Number.isFinite(options.durationMilliseconds)
    ? Math.max(900, Math.min(1_600, Math.round(options.durationMilliseconds)))
    : boundedRandomInteger(900, 1_600, random)
  const baseDelay = Math.floor(durationMilliseconds / steps)
  let remainingDelay = durationMilliseconds - baseDelay * steps
  const correction = Math.max(1.25, Math.min(2.5, distance * 0.006))
  const points = []

  for (let index = 1; index <= steps; index += 1) {
    const progress = index / steps
    const eased = 1 - ((1 - progress) ** 3)
    let x = distance * eased
    if (index === steps - 2) x = distance - correction
    if (index === steps - 1) x = distance - 0.35
    if (index === steps) x = distance
    const y = index === steps ? 0 : (Number(random()) - 0.5) * 2.4
    const delayMilliseconds = baseDelay + (remainingDelay > 0 ? 1 : 0)
    remainingDelay -= remainingDelay > 0 ? 1 : 0
    points.push({ x, y, delayMilliseconds })
  }

  return { steps, durationMilliseconds, points }
}

function sliderMarkerToken() {
  return crypto.randomBytes(8).toString('hex')
}

async function markSliderCandidate(frame, providerID, token) {
  return frame.evaluate(({ providerID, token }) => {
    const visible = (element) => {
      const style = getComputedStyle(element)
      const box = element.getBoundingClientRect()
      return style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity || 1) > 0
        && box.width > 0 && box.height > 0
    }
    const descriptor = (element) => `${element.id || ''} ${element.className || ''} ${element.getAttribute('role') || ''}`.toLowerCase()
    const all = Array.from(document.querySelectorAll('*')).filter(visible)
    const challengeRoots = all.filter((element) => /captcha|aliyun|verify|slide|slider|drag/.test(descriptor(element)))
    const trackCandidates = new Set(challengeRoots)
    for (const root of challengeRoots) {
      let ancestor = root.parentElement
      for (let depth = 0; ancestor && depth < 5; depth += 1) {
        if (visible(ancestor)) trackCandidates.add(ancestor)
        ancestor = ancestor.parentElement
      }
    }
    const tracks = Array.from(trackCandidates).filter((element) => {
      const box = element.getBoundingClientRect()
      return box.width >= 160 && box.height >= 20 && box.height <= 140
    })
    let best = null

    for (const track of tracks) {
      const trackBox = track.getBoundingClientRect()
      const trackName = descriptor(track)
      const scopedElements = Array.from(track.querySelectorAll('*')).filter(visible)
      const namedHandles = challengeRoots.filter((element) => (
        element !== track && track.contains(element) && /handle|button|btn|slider|thumb|drag/.test(descriptor(element))
      ))
      const handles = Array.from(new Set([...namedHandles, ...scopedElements]))
      for (const handle of handles) {
        const handleBox = handle.getBoundingClientRect()
        const handleName = descriptor(handle)
        if (handleBox.width < 18 || handleBox.width > Math.min(100, trackBox.width * 0.5)) continue
        if (handleBox.height < 18 || handleBox.height > Math.max(100, trackBox.height * 2)) continue
        if (handleBox.left < trackBox.left - 12 || handleBox.right > trackBox.right + 12) continue
        if (handleBox.bottom < trackBox.top || handleBox.top > trackBox.bottom) continue
        const distance = trackBox.right - handleBox.right
        if (distance < 60 || distance > trackBox.width) continue
        const aliyunScope = /aliyun|captcha/.test(`${trackName} ${handleName}`)
          || Boolean(track.closest('#captcha-element,[id*="captcha" i],[class*="captcha" i]'))
        let score = 0
        if (aliyunScope) score += 100
        if (providerID === 'aliyun-esa' && !aliyunScope) score -= 40
        if (/track|wrapper|slide|slider/.test(trackName)) score += 25
        if (/handle|button|btn|thumb|slider|drag/.test(handleName)) score += 20
        score += Math.max(0, 20 - Math.abs(trackBox.width - 360) / 10)
        score += Math.max(0, 10 - Math.abs(handleBox.width - 40) / 4)
        if (!best || score > best.score) best = { track, handle, score }
      }
    }

    if (!best) return false
    best.track.setAttribute('data-sub2api-challenge-track', token)
    best.handle.setAttribute('data-sub2api-challenge-handle', token)
    return true
  }, { providerID, token })
}

async function locateSlider(page, providerID, options = {}) {
  const signal = options.signal
  const deadlineAt = Number.isFinite(options.deadlineAt) ? options.deadlineAt : Date.now() + 15_000
  while (Date.now() < deadlineAt) {
    throwIfAborted(signal)
    for (const frame of page.frames()) {
      const token = sliderMarkerToken()
      try {
        if (!await markSliderCandidate(frame, providerID, token)) continue
        const track = frame.locator(`[data-sub2api-challenge-track="${token}"]`).first()
        const handle = frame.locator(`[data-sub2api-challenge-handle="${token}"]`).first()
        const [trackBox, handleBox] = await Promise.all([track.boundingBox(), handle.boundingBox()])
        if (validBox(trackBox) && validBox(handleBox)) return { frame, trackBox, handleBox }
      } catch {
        // Continue through frames while the challenge DOM is settling.
      }
    }
    await waitForPromiseOrAbort(new Promise((resolve) => setTimeout(resolve, 200)), signal)
  }
  return null
}

async function dragSlider(page, geometry, trajectory, options = {}) {
  const signal = options.signal
  const startX = geometry.handleBox.x + geometry.handleBox.width / 2
  const startY = geometry.handleBox.y + geometry.handleBox.height / 2
  throwIfAborted(signal)
  await page.mouse.move(startX, startY)
  await page.mouse.down()
  try {
    for (const point of trajectory.points) {
      throwIfAborted(signal)
      await page.mouse.move(startX + point.x, startY + point.y)
      await waitForPromiseOrAbort(
        new Promise((resolve) => setTimeout(resolve, point.delayMilliseconds)),
        signal
      )
    }
  } finally {
    await page.mouse.up().catch(() => {})
  }
}

function defaultChallengeProviders() {
  return [
    {
      id: 'aliyun-esa',
      detect: aliyunSnapshotDetected,
      locate: (page, options) => locateSlider(page, 'aliyun-esa', options),
    },
    {
      id: 'generic-slide-to-end',
      detect: (snapshot) => snapshot.isChallenge
        && (snapshot.frames || []).some((frame) => frame?.hasGenericSlider),
      locate: (page, options) => locateSlider(page, 'generic-slide-to-end', options),
    },
  ]
}

function proxyIdentity(proxy) {
  if (!proxy) return 'direct'
  return JSON.stringify({
    server: String(proxy.server || ''),
    username: String(proxy.username || ''),
    password: String(proxy.password || ''),
  })
}

function sessionDigest(providerID, origin, proxy) {
  return crypto.createHash('sha256')
    .update(JSON.stringify({ provider: providerID, origin, proxy: proxyIdentity(proxy) }))
    .digest('hex')
}

function sessionFilePath(sessionDirectory, providerID, origin, proxy) {
  return path.join(sessionDirectory, `${sessionDigest(providerID, origin, proxy)}.json`)
}

function validStorageState(value) {
  return value && typeof value === 'object' && !Array.isArray(value)
    && Array.isArray(value.cookies) && Array.isArray(value.origins)
}

function ensurePrivateDirectory(directory) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 })
  fs.chmodSync(directory, 0o700)
}

function readStoredSession(sessionDirectory, providers, origin, proxy) {
  ensurePrivateDirectory(sessionDirectory)
  for (const provider of providers) {
    const file = sessionFilePath(sessionDirectory, provider.id, origin, proxy)
    if (!fs.existsSync(file)) continue
    try {
      const storageState = JSON.parse(fs.readFileSync(file, 'utf8'))
      if (!validStorageState(storageState)) throw new Error('invalid storage state')
      fs.chmodSync(file, 0o600)
      return { provider: provider.id, storageState }
    } catch {
      fs.rmSync(file, { force: true })
    }
  }
  return null
}

function writeStoredSession(sessionDirectory, providerID, origin, proxy, storageState) {
  if (!validStorageState(storageState)) throw new Error('cannot save invalid browser storage state')
  ensurePrivateDirectory(sessionDirectory)
  const destination = sessionFilePath(sessionDirectory, providerID, origin, proxy)
  const temporary = `${destination}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`
  try {
    fs.writeFileSync(temporary, `${JSON.stringify(storageState, null, 2)}\n`, { mode: 0o600 })
    fs.chmodSync(temporary, 0o600)
    fs.renameSync(temporary, destination)
    fs.chmodSync(destination, 0o600)
  } finally {
    fs.rmSync(temporary, { force: true })
  }
  return destination
}

async function navigateHome(page, origin, timeoutMilliseconds) {
  try {
    return await page.goto(`${origin}/`, {
      waitUntil: 'domcontentloaded',
      timeout: timeoutMilliseconds,
    })
  } catch (error) {
    if (error?.name !== 'TimeoutError') throw error
    return null
  }
}

async function reloadChallenge(page, timeoutMilliseconds) {
  try {
    return await page.reload({ waitUntil: 'domcontentloaded', timeout: timeoutMilliseconds })
  } catch (error) {
    if (error?.name !== 'TimeoutError') throw error
    return null
  }
}

class ChallengeManager {
  constructor(options = {}) {
    this.enabled = Boolean(options.enabled)
    this.origin = String(options.origin || DEFAULT_ORIGIN).replace(/\/$/, '')
    this.sessionDirectory = options.sessionDirectory || '/data/challenge-sessions'
    this.timeoutMilliseconds = Math.max(1, Number(options.timeoutMilliseconds) || 90_000)
    this.navigationTimeoutMilliseconds = Math.max(1, Number(options.navigationTimeoutMilliseconds) || 20_000)
    this.providers = options.providers || defaultChallengeProviders()
    this.mutex = options.mutex || new CancellableMutex()
    this.stopSignal = options.stopSignal
    this.random = options.random || Math.random
    this.now = options.now || Date.now
    this.inspect = options.inspect || collectChallengeSnapshot
    this.navigate = options.navigate || ((page, timeout) => navigateHome(page, this.origin, timeout))
    this.reload = options.reload || reloadChallenge
    this.drag = options.drag || dragSlider
    this.logger = options.logger || console
    this.contextAttempts = new WeakMap()
  }

  loadSession(proxy) {
    if (!this.enabled) return null
    return readStoredSession(this.sessionDirectory, this.providers, this.origin, proxy)
  }

  async saveSession(context, providerID, proxy) {
    const storageState = await context.storageState()
    return writeStoredSession(this.sessionDirectory, providerID, this.origin, proxy, storageState)
  }

  async solve(options = {}) {
    if (!this.enabled) {
      throw new ChallengeError('disabled', 'shop API verification required but automatic challenge recovery is disabled')
    }
    const timeoutController = new AbortController()
    const timeoutError = new ChallengeError('timeout', `verification challenge exceeded ${this.timeoutMilliseconds} milliseconds`)
    const timer = setTimeout(() => timeoutController.abort(timeoutError), this.timeoutMilliseconds)
    const signal = combinedAbortSignal([options.signal, this.stopSignal, timeoutController.signal])
    const report = (values) => options.onState?.(values)
    report({ state: 'queued' })

    try {
      return await this.mutex.runExclusive(
        () => this.solveLocked(options, signal, report),
        signal
      )
    } catch (error) {
      if (options.signal?.aborted || this.stopSignal?.aborted) throw abortReason(options.signal?.aborted ? options.signal : this.stopSignal)
      const finalError = timeoutController.signal.aborted ? timeoutError : error
      if (finalError instanceof ChallengeError) {
        report({ state: finalError.challengeState })
        throw finalError
      }
      const wrapped = new ChallengeError('failed', `verification challenge recovery failed: ${String(finalError?.message || finalError)}`, { cause: finalError })
      report({ state: wrapped.challengeState })
      throw wrapped
    } finally {
      clearTimeout(timer)
    }
  }

  async operation(promise, signal) {
    return waitForPromiseOrAbort(promise, signal)
  }

  async solveLocked(options, signal, report) {
    const { context, page, proxy } = options
    if (!context || !page) throw new ChallengeError('failed', 'verification challenge requires an active browser context and page')
    const startedAt = new Date(this.now()).toISOString()
    report({ state: 'detecting', startedAt })
    options.setResourcesAllowed?.(true)

    try {
      let response = await this.operation(this.navigate(page, this.navigationTimeoutMilliseconds, signal), signal)
      let snapshot = await this.operation(this.inspect(page, response), signal)
      if (!challengeSnapshotDetected(snapshot)) {
        report({ state: 'clear' })
        return { state: 'clear', provider: '' }
      }

      let provider = selectChallengeProvider(snapshot, this.providers)
      if (!provider) throw new ChallengeError('unsupported', 'verification page does not contain a supported slide-to-end challenge')
      let attempt = this.contextAttempts.get(context) || 0
      report({ state: 'solving', provider: provider.id, attempt })

      while (attempt < MAX_DRAG_ATTEMPTS) {
        throwIfAborted(signal)
        if (attempt > 0) {
          response = await this.operation(this.reload(page, this.navigationTimeoutMilliseconds, signal), signal)
          snapshot = await this.operation(this.inspect(page, response), signal)
          if (challengeCleared(snapshot)) {
            await this.persistSolvedSession(context, provider.id, proxy)
            const solvedAt = new Date(this.now()).toISOString()
            report({ state: 'solved', provider: provider.id, attempt, solvedAt })
            return { state: 'solved', provider: provider.id, attempt }
          }
          provider = selectChallengeProvider(snapshot, this.providers) || provider
        }

        const deadlineAt = this.now() + Math.min(15_000, this.timeoutMilliseconds)
        const geometry = await this.operation(provider.locate(page, { signal, deadlineAt }), signal)
        if (!geometry) {
          throw new ChallengeError('unsupported', `${provider.id} challenge has no supported visible slide-to-end control`)
        }
        const distance = computeDragDistance(geometry.trackBox, geometry.handleBox)
        const trajectory = generateDragTrajectory(distance, { random: this.random })
        attempt += 1
        this.contextAttempts.set(context, attempt)
        report({ state: 'solving', provider: provider.id, attempt })
        await this.operation(this.drag(page, geometry, trajectory, { signal }), signal)

        response = await this.operation(this.navigate(page, this.navigationTimeoutMilliseconds, signal), signal)
        snapshot = await this.operation(this.inspect(page, response), signal)
        if (challengeCleared(snapshot)) {
          await this.persistSolvedSession(context, provider.id, proxy)
          const solvedAt = new Date(this.now()).toISOString()
          report({ state: 'solved', provider: provider.id, attempt, solvedAt })
          return { state: 'solved', provider: provider.id, attempt }
        }
      }
      throw new ChallengeError('failed', `${provider.id} challenge remained after ${MAX_DRAG_ATTEMPTS} drag attempts`)
    } finally {
      options.setResourcesAllowed?.(false)
    }
  }

  async persistSolvedSession(context, providerID, proxy) {
    try {
      await this.saveSession(context, providerID, proxy)
    } catch (error) {
      this.logger.error?.(`${new Date().toISOString()} failed to save verification session: ${String(error?.message || error)}`)
    }
  }
}

async function recoverChallengeAcrossProxyPool(options = {}) {
  const poolSize = Number(options.poolSize) || 0
  const currentIndex = Number(options.currentIndex) || 0
  if (poolSize < 1 || typeof options.solveCurrent !== 'function' || typeof options.switchTo !== 'function') {
    throw new Error('challenge proxy recovery requires a pool, current solver, and switch callback')
  }
  let lastError
  try {
    await options.solveCurrent()
    return currentIndex
  } catch (error) {
    lastError = error
  }

  for (let offset = 1; offset < poolSize; offset += 1) {
    const proxyIndex = (currentIndex + offset) % poolSize
    try {
      await options.switchTo(proxyIndex)
      return proxyIndex
    } catch (error) {
      lastError = error
    }
  }
  const finalError = isChallengeError(lastError)
    ? lastError
    : new ChallengeError('failed', `verification challenge failed on every lane proxy: ${String(lastError?.message || lastError)}`, { cause: lastError })
  finalError.restartLane = true
  throw finalError
}

async function retryFailedChallengeOperation(task, recover, options = {}) {
  if (typeof task !== 'function' || typeof recover !== 'function') {
    throw new Error('challenge operation retry requires task and recovery callbacks')
  }
  const maxRecoveries = Number.isInteger(options.maxRecoveries) && options.maxRecoveries >= 0
    ? options.maxRecoveries
    : 2
  let recoveries = 0
  while (true) {
    try {
      return await task()
    } catch (error) {
      if (!isChallengeError(error)) throw error
      if (recoveries >= maxRecoveries) {
        throw new ChallengeError(
          'unsupported',
          String(options.exhaustedMessage || 'operation repeatedly returned a verification response'),
          { restartLane: true, cause: error }
        )
      }
      recoveries += 1
      await recover({ error, recovery: recoveries })
    }
  }
}

function shouldBlockResource(resourceType, challengeResourcesAllowed) {
  return !challengeResourcesAllowed && BLOCKED_RESOURCE_TYPES.has(resourceType)
}

module.exports = {
  ChallengeError,
  ChallengeManager,
  CancellableMutex,
  MAX_DRAG_ATTEMPTS,
  aliyunSnapshotDetected,
  challengeBackoffMilliseconds,
  challengeCleared,
  challengeSnapshotDetected,
  challengeTextDetected,
  collectChallengeSnapshot,
  computeDragDistance,
  defaultChallengeProviders,
  generateDragTrajectory,
  isChallengeError,
  isHTTPCustomDenial,
  readStoredSession,
  recoverChallengeAcrossProxyPool,
  retryFailedChallengeOperation,
  selectChallengeProvider,
  sessionDigest,
  sessionFilePath,
  shouldBlockResource,
  validStorageState,
  writeStoredSession,
}
