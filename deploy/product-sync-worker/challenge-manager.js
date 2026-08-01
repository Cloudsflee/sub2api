const crypto = require('node:crypto')
const { execFile } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')
const { performance } = require('node:perf_hooks')
const { promisify } = require('node:util')

const DEFAULT_ORIGIN = 'https://pay.ldxp.cn'
const MAX_DRAG_ATTEMPTS = 2
const BLOCKED_RESOURCE_TYPES = new Set(['image', 'media', 'font'])
const execFileAsync = promisify(execFile)

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
  return /^(?:verification|security verification|security check|human verification|verify you are human|安全验证|人机验证)$/i.test(normalizedTitle)
    || /(?:please\s+)?(?:slide|drag|move).{0,80}(?:verify|verification|right\s*(?:end|edge)|to\s+the\s+end|finish)/i.test(normalizedText)
    || /(?:verify|verification).{0,80}(?:slide|drag|move|slider)/i.test(normalizedText)
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
    || isHTTPCustomDenial(frame?.text)
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
    frame?.hasAliyunDOM || frame?.hasAliyunScript || isHTTPCustomDenial(frame?.text)
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
  const correction = Math.max(1.25, Math.min(2.5, distance * 0.006))
  // Humans do not emit a perfectly periodic stream of points.  Give the
  // initial press a short hesitation, vary inter-event delays, and normalize
  // the result back to the requested total so the caller still has a strict
  // trajectory budget.
  const delayWeights = Array.from({ length: steps }, (_, index) => {
    if (index === 0) return boundedRandomInteger(2.5, 4.5, random)
    if (index === steps - 1) return boundedRandomInteger(1.8, 3.4, random)
    return 0.75 + Number(random()) * 0.7
  })
  const delayWeightTotal = delayWeights.reduce((sum, value) => sum + value, 0)
  const delays = delayWeights.map((weight) => Math.max(1, Math.round(durationMilliseconds * weight / delayWeightTotal)))
  let delayDelta = durationMilliseconds - delays.reduce((sum, value) => sum + value, 0)
  for (let index = steps - 2; delayDelta !== 0 && index >= 0; index -= 1) {
    if (delayDelta > 0) {
      delays[index] += 1
      delayDelta -= 1
    } else if (delays[index] > 1) {
      delays[index] -= 1
      delayDelta += 1
    }
    if (index === 0 && delayDelta < 0) index = steps - 1
  }
  const points = []
  let previousX = 0

  for (let index = 1; index <= steps; index += 1) {
    const progress = index / steps
    // Accelerate, settle into the main movement, then decelerate into a
    // deliberate final correction.  Small horizontal noise prevents every
    // solve from having the same mathematical cubic signature while the
    // monotonic clamp keeps the handle from making implausible reversals.
    const eased = progress < 0.2
      ? 0.2 * ((progress / 0.2) ** 1.7)
      : progress < 0.75
        ? 0.2 + 0.55 * (((progress - 0.2) / 0.55) ** 0.86)
        : 0.75 + 0.25 * (1 - ((1 - (progress - 0.75) / 0.25) ** 2.4))
    const noise = (Number(random()) - 0.5) * (progress > 0.85 ? 0.004 : 0.014)
    let x = distance * Math.max(0, Math.min(1, eased + noise))
    if (index === steps - 2) x = distance - correction
    if (index === steps - 1) x = distance - 0.35
    if (index === steps) x = distance
    if (index < steps - 2) x = Math.max(previousX + Math.min(0.25, distance / steps / 3), x)
    previousX = x
    const y = index === steps ? 0 : (Number(random()) - 0.5) * 2.4
    const delayMilliseconds = delays[index - 1]
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
  // ESA injects the slider asynchronously after its challenge iframe and
  // image assets have loaded.  Fifteen seconds is too short on a cold proxy
  // connection (the DOM commonly appears around 10-12s), so use a 30s
  // discovery budget unless the caller supplied an explicit deadline.
  const deadlineAt = Number.isFinite(options.deadlineAt) ? options.deadlineAt : Date.now() + 30_000
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

async function dragSliderWithMouse(page, geometry, trajectory, options = {}) {
  const signal = options.signal
  const now = options.now || (() => performance.now())
  const wait = options.wait || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
  const startX = geometry.handleBox.x + geometry.handleBox.width / 2
  const startY = geometry.handleBox.y + geometry.handleBox.height / 2
  throwIfAborted(signal)
  await page.mouse.move(startX, startY)
  await page.mouse.down()
  try {
    const startedAt = now()
    let scheduledAt = 0
    for (const point of trajectory.points) {
      scheduledAt += point.delayMilliseconds
      const waitMilliseconds = scheduledAt - (now() - startedAt)
      if (waitMilliseconds > 0) {
        await waitForPromiseOrAbort(wait(waitMilliseconds), signal)
      }
      throwIfAborted(signal)
      await page.mouse.move(startX + point.x, startY + point.y)
    }
  } finally {
    await page.mouse.up().catch(() => {})
  }
}

/**
 * Convert Playwright viewport coordinates to the coordinates understood by
 * xdotool.  Playwright's mouse API is relative to the content viewport while
 * xdotool addresses the X11 root window.  The browser window decorations are
 * therefore accounted for using the live outer/inner window dimensions rather
 * than a fixed Chrome toolbar height.
 */
async function pageScreenGeometry(page) {
  if (typeof page.evaluate !== 'function') {
    throw new Error('native challenge drag requires page.evaluate')
  }
  const values = await page.evaluate(() => ({
    screenX: Number(window.screenX ?? window.screenLeft ?? 0),
    screenY: Number(window.screenY ?? window.screenTop ?? 0),
    outerWidth: Number(window.outerWidth || 0),
    outerHeight: Number(window.outerHeight || 0),
    innerWidth: Number(window.innerWidth || 0),
    innerHeight: Number(window.innerHeight || 0),
    devicePixelRatio: Number(window.devicePixelRatio || 1),
  }))
  const finite = (value, fallback = 0) => Number.isFinite(value) ? value : fallback
  const screenX = finite(values?.screenX)
  const screenY = finite(values?.screenY)
  const outerWidth = finite(values?.outerWidth)
  const outerHeight = finite(values?.outerHeight)
  const innerWidth = finite(values?.innerWidth)
  const innerHeight = finite(values?.innerHeight)
  const devicePixelRatio = Math.max(0.25, Math.min(4, finite(values?.devicePixelRatio, 1)))
  if (innerWidth <= 0 || innerHeight <= 0) throw new Error('native challenge drag has no visible viewport')

  // On X11 Chrome's content viewport starts after the left border and the top
  // browser chrome.  With a maximized/frameless window the differences are
  // zero, which is also handled by these calculations.
  const chromeLeft = Math.max(0, (outerWidth - innerWidth) / 2)
  const chromeTop = Math.max(0, outerHeight - innerHeight - chromeLeft)
  return {
    left: (screenX + chromeLeft) * devicePixelRatio,
    top: (screenY + chromeTop) * devicePixelRatio,
    scale: devicePixelRatio,
    viewportWidth: innerWidth,
    viewportHeight: innerHeight,
  }
}

/**
 * Chrome does not expose the native X11 frame/toolbar offset through
 * `window.outerHeight` reliably.  In particular, a headed Chrome under Xvfb
 * can report a 17px smaller top chrome than the root-window coordinate seen by
 * xdotool.  Calibrate once with a harmless native move and use the trusted
 * event's screen/client pair as the authoritative viewport origin.  If the
 * probe cannot be delivered (for example in a unit test or a transiently
 * unfocused window), retain the best browser-derived estimate.
 */
async function calibrateNativeScreenGeometry(page, screen, options = {}) {
  if (options.calibrate === false || typeof page?.evaluate !== 'function') return screen
  const scale = Number.isFinite(screen.scale) && screen.scale > 0 ? screen.scale : 1
  const viewportWidth = Number.isFinite(screen.viewportWidth) ? screen.viewportWidth : 1024
  const viewportHeight = Number.isFinite(screen.viewportHeight) ? screen.viewportHeight : 768
  const probeViewportX = Math.max(2, Math.min(viewportWidth - 2, 8))
  const probeViewportY = Math.max(2, viewportHeight - 8)
  const probeX = Math.round(screen.left + probeViewportX * scale)
  const probeY = Math.round(screen.top + probeViewportY * scale)
  const marker = `__sub2apiNativeCalibration${Date.now()}${Math.random().toString(16).slice(2)}`
  try {
    await page.evaluate((name) => {
      window[name] = null
      const listener = (event) => {
        window[name] = {
          clientX: Number(event.clientX),
          clientY: Number(event.clientY),
          screenX: Number(event.screenX),
          screenY: Number(event.screenY),
        }
        document.removeEventListener('mousemove', listener, true)
      }
      document.addEventListener('mousemove', listener, true)
    }, marker)
    await (options.execute || runXdotool)(['mousemove', '--sync', probeX, probeY], options)
    const observed = await page.evaluate((name) => window[name], marker)
    await page.evaluate((name) => { try { delete window[name] } catch {} }, marker).catch(() => {})
    if (!observed || !Number.isFinite(observed.clientX) || !Number.isFinite(observed.clientY)) return screen
    const observedScreenX = Number.isFinite(observed.screenX)
      ? observed.screenX
      : probeX
    const observedScreenY = Number.isFinite(observed.screenY)
      ? observed.screenY
      : probeY
    const left = observedScreenX - observed.clientX * scale
    const top = observedScreenY - observed.clientY * scale
    if (!Number.isFinite(left) || !Number.isFinite(top)) return screen
    return { ...screen, left, top, calibration: { probeX, probeY, observed } }
  } catch {
    await page.evaluate((name) => { try { delete window[name] } catch {} }, marker).catch(() => {})
    return screen
  }
}

async function runXdotool(argumentsList, options = {}) {
  const executable = options.executable || 'xdotool'
  const timeout = Number.isFinite(options.timeout) ? Math.max(250, options.timeout) : 5_000
  await execFileAsync(executable, argumentsList.map((value) => String(value)), {
    timeout,
    windowsHide: true,
  })
}

/**
 * Drag using native X11 input.  ESA's browser-side pointer accounting is more
 * reliable with real X11 mouse events than with Playwright's protocol mouse
 * when Chrome is running headed under Xvfb.  The executor is injectable for
 * tests and the caller can fall back to the Playwright implementation when
 * xdotool is unavailable.
 */
async function dragSliderNative(page, geometry, trajectory, options = {}) {
  const signal = options.signal
  const now = options.now || (() => performance.now())
  const wait = options.wait || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
  const execute = options.executeXdotool || runXdotool
  throwIfAborted(signal)
  if (typeof page.bringToFront === 'function') await page.bringToFront()
  let screen = options.screenGeometry || await pageScreenGeometry(page)
  if (!options.screenGeometry) {
    screen = await calibrateNativeScreenGeometry(page, screen, {
      ...options,
      execute,
    })
  }
  const scale = Number.isFinite(screen.scale) && screen.scale > 0 ? screen.scale : 1
  const startViewportX = geometry.handleBox.x + geometry.handleBox.width / 2
  const startViewportY = geometry.handleBox.y + geometry.handleBox.height / 2
  const toScreen = (x, y) => [
    Math.round(screen.left + x * scale),
    Math.round(screen.top + y * scale),
  ]
  const [startX, startY] = toScreen(startViewportX, startViewportY)

  options.onNativeDragStart?.({ screen, geometry, startX, startY })
  throwIfAborted(signal)
  // --sync is intentionally used only for the initial positioning.  Repeating
  // it for trajectory points can block forever when two points round to the
  // same pixel, especially on a 1x Xvfb display.
  await execute(['mousemove', '--sync', startX, startY], options)
  await execute(['mousedown', '1'], options)
  let pressed = true
  try {
    const startedAt = now()
    let scheduledAt = 0
    let previousX = startX
    let previousY = startY
    for (const point of trajectory.points) {
      scheduledAt += point.delayMilliseconds
      const waitMilliseconds = scheduledAt - (now() - startedAt)
      if (waitMilliseconds > 0) await waitForPromiseOrAbort(wait(waitMilliseconds), signal)
      throwIfAborted(signal)
      const [x, y] = toScreen(startViewportX + point.x, startViewportY + point.y)
      // Filter duplicate integer coordinates.  A duplicate event contributes
      // no information to the challenge and only adds process overhead.
      if (x === previousX && y === previousY) continue
      await execute(['mousemove', x, y], options)
      previousX = x
      previousY = y
    }
  } finally {
    if (pressed) {
      pressed = false
      await execute(['mouseup', '1'], options).catch(() => {})
    }
  }
}

async function dragSlider(page, geometry, trajectory, options = {}) {
  if (options.nativeDrag) {
    try {
      return await dragSliderNative(page, geometry, trajectory, options)
    } catch (error) {
      if (options.signal?.aborted) throw error
      options.onNativeDragError?.(error)
      // Keep a protocol-mouse fallback for environments where xdotool is not
      // installed or the browser has temporarily lost its X11 window.
    }
  }
  return dragSliderWithMouse(page, geometry, trajectory, options)
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
  if (!value || typeof value !== 'object' || Array.isArray(value)
    || !Array.isArray(value.cookies) || !Array.isArray(value.origins)) return false
  const validCookie = (cookie) => cookie && typeof cookie === 'object' && !Array.isArray(cookie)
    && typeof cookie.name === 'string' && cookie.name.length > 0
    && typeof cookie.value === 'string'
    && (typeof cookie.url === 'string' || typeof cookie.domain === 'string')
  const validOrigin = (entry) => entry && typeof entry === 'object' && !Array.isArray(entry)
    && typeof entry.origin === 'string'
    && Array.isArray(entry.localStorage)
    && entry.localStorage.every((item) => item && typeof item === 'object'
      && typeof item.name === 'string' && typeof item.value === 'string')
  return value.cookies.every(validCookie) && value.origins.every(validOrigin)
}

function proxySessionSummary(proxy) {
  const identity = proxyIdentity(proxy)
  let server = ''
  try {
    const parsed = new URL(String(proxy?.server || ''))
    server = `${parsed.protocol}//${parsed.host}`
  } catch {
    // A direct connection has no server. Invalid values are never expected
    // from parseProxyConfiguration, but keeping the summary empty avoids
    // leaking an arbitrary credential-bearing string if called directly.
  }
  return {
    server,
    identity: crypto.createHash('sha256').update(identity).digest('hex'),
  }
}

function sessionMetadata(providerID, origin, proxy) {
  return {
    version: 1,
    provider: String(providerID),
    origin: String(origin),
    proxy: proxySessionSummary(proxy),
  }
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
      const parsed = JSON.parse(fs.readFileSync(file, 'utf8'))
      if (!validStorageState(parsed)) throw new Error('invalid storage state')
      const metadata = parsed._sub2api
      if (metadata !== undefined) {
        const expected = sessionMetadata(provider.id, origin, proxy)
        if (!metadata || metadata.provider !== expected.provider
          || metadata.origin !== expected.origin
          || metadata.proxy?.identity !== expected.proxy.identity) {
          throw new Error('session metadata does not match its lane identity')
        }
      }
      const { _sub2api: ignoredMetadata, ...storageState } = parsed
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
  const persistedState = {
    ...storageState,
    _sub2api: sessionMetadata(providerID, origin, proxy),
  }
  try {
    fs.writeFileSync(temporary, `${JSON.stringify(persistedState, null, 2)}\n`, { mode: 0o600 })
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
    this.nativeDrag = Boolean(options.nativeDrag)
    this.nativeDragDebug = Boolean(options.nativeDragDebug)
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
    const timeoutError = new ChallengeError('timeout', `verification challenge exceeded ${this.timeoutMilliseconds} milliseconds`)
    const queueSignal = combinedAbortSignal([options.signal, this.stopSignal])
    let timeoutController
    const report = (values) => options.onState?.(values)
    report({ state: 'queued' })

    try {
      return await this.mutex.runExclusive(
        async () => {
          timeoutController = new AbortController()
          const timer = setTimeout(() => timeoutController.abort(timeoutError), this.timeoutMilliseconds)
          const signal = combinedAbortSignal([options.signal, this.stopSignal, timeoutController.signal])
          try {
            return await this.solveLocked(options, signal, report)
          } finally {
            clearTimeout(timer)
          }
        },
        queueSignal
      )
    } catch (error) {
      if (options.signal?.aborted || this.stopSignal?.aborted) throw abortReason(options.signal?.aborted ? options.signal : this.stopSignal)
      const finalError = timeoutController?.signal.aborted ? timeoutError : error
      if (finalError instanceof ChallengeError) {
        report({ state: finalError.challengeState })
        throw finalError
      }
      const wrapped = new ChallengeError('failed', `verification challenge recovery failed: ${String(finalError?.message || finalError)}`, { cause: finalError })
      report({ state: wrapped.challengeState })
      throw wrapped
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
      // ESA's loader script can remain on an otherwise normal page after a
      // successful verification.  Treat the page as clear when the challenge
      // DOM/copy and the http_custom denial are gone; the script marker alone
      // must not trigger an unsupported solve loop.
      if (!challengeSnapshotDetected(snapshot) || challengeCleared(snapshot)) {
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

        const deadlineAt = this.now() + Math.min(30_000, this.timeoutMilliseconds)
        const geometry = await this.operation(provider.locate(page, { signal, deadlineAt }), signal)
        if (!geometry) {
          throw new ChallengeError('unsupported', `${provider.id} challenge has no supported visible slide-to-end control`)
        }
        const distance = computeDragDistance(geometry.trackBox, geometry.handleBox)
        const trajectory = generateDragTrajectory(distance, { random: this.random })
        attempt += 1
        this.contextAttempts.set(context, attempt)
        report({ state: 'solving', provider: provider.id, attempt })
        await this.operation(this.drag(page, geometry, trajectory, {
          signal,
          nativeDrag: this.nativeDrag,
          onNativeDragStart: this.nativeDragDebug
            ? (values) => this.logger.log?.(`${new Date().toISOString()} native challenge drag geometry: ${JSON.stringify(values)}`)
            : undefined,
          onNativeDragError: (error) => this.logger.warn?.(`${new Date().toISOString()} native challenge drag unavailable; falling back to Playwright mouse: ${String(error?.message || error)}`),
        }), signal)

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
  dragSlider,
  dragSliderNative,
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
