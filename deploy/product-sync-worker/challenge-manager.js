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
  const settledPromise = Promise.resolve(promise)
  if (!signal) return settledPromise
  if (signal.aborted) {
    // A navigation or drag can reject after a lane has been cancelled. Attach
    // a sink before returning the abort error to avoid an unhandled rejection.
    settledPromise.catch(() => {})
    return Promise.reject(abortReason(signal))
  }

  return new Promise((resolve, reject) => {
    const onAbort = () => reject(abortReason(signal))
    signal.addEventListener('abort', onAbort, { once: true })
    settledPromise.then(
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

function challengeFrameEvidenceDetected(frame) {
  return frame?.hasCaptchaDOM
    || frame?.hasAliyunDOM
    || frame?.hasAliyunScript
    || isHTTPCustomDenial(frame?.text)
    || challengeTextDetected(frame?.title, frame?.text)
}

function challengeEvidenceDetected(snapshot) {
  return (snapshot?.frames || []).some((frame) => challengeFrameEvidenceDetected(frame))
}

// The response delivering an initial/reloaded challenge is authoritative.
// After a successful drag, however, ESA can leave http_custom on the callback
// navigation even though the resulting document is already the normal shop.
function trustedHTTPCustomResponseEvidence(snapshot) {
  return isHTTPCustomDenial(snapshot?.responseError)
    && snapshot?.responseContext !== 'post-drag-submission'
}

function challengePageEvidenceDetected(snapshot) {
  return challengeEvidenceDetected(snapshot) || trustedHTTPCustomResponseEvidence(snapshot)
}

function challengeSnapshotDetected(snapshot) {
  if (!snapshot || typeof snapshot !== 'object') return false
  if (isHTTPCustomDenial(snapshot.responseError)) return true
  return challengeEvidenceDetected(snapshot)
}

function challengeCleared(snapshot) {
  if (!snapshot || typeof snapshot !== 'object' || isHTTPCustomDenial(snapshot.responseError)) return false
  return !(snapshot.frames || []).some((frame) => (
    frame?.hasCaptchaDOM
    || frame?.hasAliyunDOM
    // A generic provider is selected only after the page has already been
    // classified as a verification page. Keep its visible slider in the
    // uncleared set as well; otherwise a drag that merely removes the copy
    // can be reported as solved while the actual control is still present.
    || frame?.hasGenericSlider
    || isHTTPCustomDenial(frame?.text)
    || challengeTextDetected(frame?.title, frame?.text)
  ))
}

// ESA can leave the x-tengine-error header from the challenge response on the
// navigation object which delivered the post-drag page. The subsequent
// document (and API requests made by it) can already be the normal shop page.
// Keep challengeCleared() strict for initial detection, but recognise this
// post-drag state when a real HTTP document is present and no challenge DOM or
// copy remains.
function challengeContentCleared(snapshot) {
  if (!snapshot || typeof snapshot !== 'object') return false
  const frames = Array.isArray(snapshot.frames) ? snapshot.frames : []
  if (!frames.some((frame) => /^https?:\/\//i.test(String(frame?.url || '')))) return false
  const ignoreGenericSlider = snapshot.responseContext === 'post-drag-submission'
  return !frames.some((frame) => (
    frame?.hasCaptchaDOM
    || frame?.hasAliyunDOM
    || (!ignoreGenericSlider && frame?.hasGenericSlider)
    || isHTTPCustomDenial(frame?.text)
    || challengeTextDetected(frame?.title, frame?.text)
  ))
}

function aliyunSnapshotDetected(snapshot) {
  const frames = Array.isArray(snapshot?.frames) ? snapshot.frames : []
  const hasAliyunEvidence = frames.some((frame) => (
    frame?.hasAliyunDOM || frame?.hasAliyunScript || isHTTPCustomDenial(frame?.text)
  ))
  if (hasAliyunEvidence) return true
  if (isHTTPCustomDenial(snapshot?.responseError)) {
    // Keep header-only ESA detection for the shell that has not painted its
    // slider yet, but do not let that stale header claim an unrelated visible
    // slider on an otherwise normal shop page.
    return !frames.some((frame) => frame?.hasGenericSlider)
  }
  return false
}

function genericSlideSnapshotDetected(snapshot) {
  if (!snapshot?.isChallenge) return false
  const frames = Array.isArray(snapshot.frames) ? snapshot.frames : []
  // DOM evidence can live in a parent frame while the control is rendered in
  // a child H5 iframe. A fresh http_custom response is also authoritative, but
  // the same header is deliberately ignored on the post-drag callback page.
  const hasVerificationEvidence = challengePageEvidenceDetected(snapshot)
  return hasVerificationEvidence && frames.some((frame) => frame?.hasGenericSlider)
}

function withChallengeResponseContext(snapshot, responseContext) {
  if (!snapshot || typeof snapshot !== 'object') return snapshot
  return { ...snapshot, responseContext }
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
        const visible = (element) => {
          if (!element || element.closest?.('[hidden],[aria-hidden="true"]')) return false
          const style = getComputedStyle(element)
          if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) return false
          const rect = element.getBoundingClientRect()
          return rect.width > 0 && rect.height > 0
        }
        const hasVisible = (selectors) => Array.from(document.querySelectorAll(selectors)).some(visible)
        const aliyunSelectors = [
          '#aliyunCaptcha-sliding-slider',
          '#aliyunCaptcha-sliding-wrapper',
          '#captcha-element',
          '[id*="aliyunCaptcha"]',
          '[class*="aliyunCaptcha"]',
          '[class*="aliyun-captcha"]',
        ].join(',')
        const captchaSelectors = [
          '[id*="captcha" i]',
          '[class*="captcha" i]',
          '[data-captcha]',
        ].join(',')
        const hasAliyunDOM = hasVisible(aliyunSelectors)
        const hasCaptchaDOM = hasAliyunDOM || hasVisible(captchaSelectors)
        const hasGenericSlider = hasVisible([
          '[class*="slide" i]',
          '[class*="slider" i]',
          '[class*="drag" i]',
          '[aria-valuemax]',
        ].join(','))
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

function boundedRandomNumber(minimum, maximum, random) {
  const value = Math.max(0, Math.min(0.999999999, Number(random()) || 0))
  return minimum + value * (maximum - minimum)
}

function distributedDelays(count, durationMilliseconds, random, weightForIndex) {
  const weights = Array.from({ length: count }, (_, index) => (
    Math.max(0.05, Number(weightForIndex(index)) || (0.7 + Number(random()) * 0.6))
  ))
  const weightTotal = weights.reduce((sum, value) => sum + value, 0)
  const delays = weights.map((weight) => Math.max(1, Math.round(durationMilliseconds * weight / weightTotal)))
  let delta = durationMilliseconds - delays.reduce((sum, value) => sum + value, 0)
  for (let index = delays.length - 1; delta !== 0; index = (index - 1 + delays.length) % delays.length) {
    if (delta > 0) {
      delays[index] += 1
      delta -= 1
    } else if (delays[index] > 1) {
      delays[index] -= 1
      delta += 1
    }
  }
  return delays
}

const HUMAN_PROGRESS_ANCHORS = [
  [0, 0],
  [0.1, 0.07],
  [0.2, 0.165],
  [0.3, 0.27],
  [0.4, 0.34],
  [0.5, 0.405],
  [0.6, 0.447],
  [0.7, 0.506],
  [0.8, 0.566],
  [0.85, 0.65],
  [0.9, 0.716],
  [0.93, 0.775],
  [0.95, 0.838],
  [1, 1],
]

function measuredHumanProgress(progress) {
  const value = Math.max(0, Math.min(1, progress))
  for (let index = 1; index < HUMAN_PROGRESS_ANCHORS.length; index += 1) {
    const previous = HUMAN_PROGRESS_ANCHORS[index - 1]
    const next = HUMAN_PROGRESS_ANCHORS[index]
    if (value > next[0]) continue
    const localProgress = (value - previous[0]) / (next[0] - previous[0])
    return previous[1] + (next[1] - previous[1]) * localProgress
  }
  return 1
}

function cubicBezier(start, control1, control2, end, progress) {
  const inverse = 1 - progress
  return inverse ** 3 * start
    + 3 * inverse ** 2 * progress * control1
    + 3 * inverse * progress ** 2 * control2
    + progress ** 3 * end
}

function generateDragTrajectory(distance, options = {}) {
  if (!Number.isFinite(distance) || distance <= 0) throw new Error('drag distance must be positive')
  const random = options.random || Math.random
  const steps = Number.isInteger(options.steps)
    ? Math.max(36, Math.min(60, options.steps))
    : boundedRandomInteger(50, 60, random)
  const settleSteps = Math.min(8, Math.max(5, Math.round(steps * 0.11)))
  const travelSteps = steps - settleSteps
  // Bound the pressed portion as one budget. Keeping travel and end-correction
  // independent previously produced 1.45-2.15s gestures, outside the
  // recovery contract's 900-1600ms trajectory range.
  const hasExplicitTravel = Number.isFinite(options.travelDurationMilliseconds)
  const hasExplicitSettle = Number.isFinite(options.settleDurationMilliseconds)
  const hasExplicitDuration = Number.isFinite(options.durationMilliseconds)
  let durationMilliseconds
  let travelDurationMilliseconds
  let settleDurationMilliseconds
  if (hasExplicitDuration) {
    durationMilliseconds = Math.max(900, Math.min(1_600, Math.round(options.durationMilliseconds)))
    settleDurationMilliseconds = hasExplicitSettle
      ? Math.max(180, Math.min(600, Math.round(options.settleDurationMilliseconds)))
      : boundedRandomInteger(220, Math.min(400, durationMilliseconds - 600), random)
    settleDurationMilliseconds = Math.min(settleDurationMilliseconds, durationMilliseconds - 600)
    travelDurationMilliseconds = durationMilliseconds - settleDurationMilliseconds
  } else if (hasExplicitTravel || hasExplicitSettle) {
    travelDurationMilliseconds = hasExplicitTravel
      ? Math.max(600, Math.min(1_400, Math.round(options.travelDurationMilliseconds)))
      : 1_000
    settleDurationMilliseconds = hasExplicitSettle
      ? Math.max(180, Math.min(600, Math.round(options.settleDurationMilliseconds)))
      : 300
    durationMilliseconds = Math.max(900, Math.min(1_600, travelDurationMilliseconds + settleDurationMilliseconds))
    const scale = durationMilliseconds / (travelDurationMilliseconds + settleDurationMilliseconds)
    travelDurationMilliseconds = Math.max(600, Math.round(travelDurationMilliseconds * scale))
    settleDurationMilliseconds = durationMilliseconds - travelDurationMilliseconds
    if (settleDurationMilliseconds < 180) {
      settleDurationMilliseconds = 180
      travelDurationMilliseconds = durationMilliseconds - settleDurationMilliseconds
    }
  } else {
    durationMilliseconds = boundedRandomInteger(1_200, 1_600, random)
    settleDurationMilliseconds = boundedRandomInteger(220, Math.min(400, durationMilliseconds - 600), random)
    travelDurationMilliseconds = durationMilliseconds - settleDurationMilliseconds
  }
  const hoverMilliseconds = Number.isFinite(options.hoverMilliseconds)
    ? Math.max(0, Math.min(4_000, Math.round(options.hoverMilliseconds)))
    : boundedRandomInteger(800, 1_400, random)
  const approachStartHoldMilliseconds = Number.isFinite(options.approachStartHoldMilliseconds)
    ? Math.max(0, Math.min(3_000, Math.round(options.approachStartHoldMilliseconds)))
    : boundedRandomInteger(900, 1_600, random)
  const approachDurationMilliseconds = Number.isFinite(options.approachDurationMilliseconds)
    ? Math.max(500, Math.min(2_500, Math.round(options.approachDurationMilliseconds)))
    : boundedRandomInteger(1_100, 1_700, random)
  const approachSteps = Number.isInteger(options.approachSteps)
    ? Math.max(16, Math.min(48, options.approachSteps))
    : boundedRandomInteger(30, 42, random)
  const holdMilliseconds = Number.isFinite(options.holdMilliseconds)
    ? Math.max(0, Math.min(2_000, Math.round(options.holdMilliseconds)))
    : boundedRandomInteger(550, 850, random)
  const overshootPixels = Number.isFinite(options.overshootPixels)
    ? Math.max(12, Math.min(48, Number(options.overshootPixels)))
    : Math.max(24, Math.min(42, distance * boundedRandomNumber(0.075, 0.13, random)))
  const startOffsetX = Number.isFinite(options.startOffsetX)
    ? Math.max(-8, Math.min(8, Number(options.startOffsetX)))
    : boundedRandomNumber(-3, 5, random)
  const startOffsetY = Number.isFinite(options.startOffsetY)
    ? Math.max(-8, Math.min(8, Number(options.startOffsetY)))
    : boundedRandomNumber(2, 7, random)
  const travelDelays = distributedDelays(travelSteps, travelDurationMilliseconds, random, (index) => (
    index === 0 ? 0.3 + Number(random()) * 0.25 : 0.7 + Number(random()) * 0.65
  ))
  const settleDelays = distributedDelays(settleSteps, settleDurationMilliseconds, random, (index) => (
    index === 2 ? 4.2 + Number(random()) * 1.8 : 0.65 + Number(random()) * 0.7
  ))
  const horizontalPhase = boundedRandomNumber(-Math.PI, Math.PI, random)
  const horizontalAmplitude = boundedRandomNumber(0.003, 0.011, random)
  const verticalControl1 = boundedRandomNumber(-4, -2, random)
  const verticalControl2 = boundedRandomNumber(2, 4.5, random)
  const verticalEnd = boundedRandomNumber(-1, 1.5, random)
  const settleVerticalEnd = boundedRandomNumber(-3, 0, random)
  let elapsed = 0
  const approachStartX = Number.isFinite(options.approachStartX)
    ? Math.max(80, Math.min(300, Number(options.approachStartX)))
    : distance * boundedRandomNumber(0.58, 0.75, random)
  const approachStartY = Number.isFinite(options.approachStartY)
    ? Math.max(-16, Math.min(16, Number(options.approachStartY)))
    : boundedRandomNumber(-8, 4, random)
  const approachControlY = boundedRandomNumber(-12, 8, random)
  const approachDelays = distributedDelays(approachSteps, approachDurationMilliseconds, random, () => (
    0.7 + Number(random()) * 0.75
  ))
  const approachPoints = []
  elapsed = 0
  for (let index = 0; index < approachSteps; index += 1) {
    elapsed += approachDelays[index]
    const progress = Math.min(1, elapsed / approachDurationMilliseconds)
    const inverse = 1 - progress
    const x = approachStartX * (inverse ** 4 + 0.055 * inverse)
    const y = cubicBezier(approachStartY, approachControlY, -2, 0, progress)
    approachPoints.push({
      x: index === approachSteps - 1 ? 0 : x,
      y: index === approachSteps - 1 ? 0 : y,
      delayMilliseconds: approachDelays[index],
    })
  }
  const points = []
  let previousX = 0
  elapsed = 0

  for (let index = 0; index < travelSteps; index += 1) {
    elapsed += travelDelays[index]
    const progress = Math.min(1, elapsed / travelDurationMilliseconds)
    const variation = horizontalAmplitude * Math.sin(Math.PI * progress)
      * Math.sin(2 * Math.PI * progress + horizontalPhase)
    let x = distance * Math.max(0, Math.min(1, measuredHumanProgress(progress) + variation))
    if (index === travelSteps - 1) x = distance
    else x = Math.max(previousX + Math.min(0.3, distance / travelSteps / 4), Math.min(distance - 0.5, x))
    previousX = x
    const y = cubicBezier(0, verticalControl1, verticalControl2, verticalEnd, progress)
    points.push({ x, y, delayMilliseconds: travelDelays[index] })
  }

  elapsed = 0
  for (let index = 0; index < settleSteps; index += 1) {
    elapsed += settleDelays[index]
    const progress = Math.min(1, elapsed / settleDurationMilliseconds)
    const x = distance + overshootPixels * (1 - (1 - progress) ** 2.2)
    const y = verticalEnd + (settleVerticalEnd - verticalEnd) * (progress * (2 - progress))
    points.push({
      x: index === settleSteps - 1 ? distance + overshootPixels : x,
      y: index === settleSteps - 1 ? settleVerticalEnd : y,
      delayMilliseconds: settleDelays[index],
    })
  }

  return {
    steps,
    durationMilliseconds,
    travelDurationMilliseconds,
    settleDurationMilliseconds,
    approachStartX,
    approachStartY,
    approachStartHoldMilliseconds,
    approachDurationMilliseconds,
    approachPoints,
    hoverMilliseconds,
    holdMilliseconds,
    overshootPixels,
    startOffsetX,
    startOffsetY,
    points,
  }
}

function sliderMarkerToken() {
  return crypto.randomBytes(8).toString('hex')
}

async function markSliderCandidate(frame, providerID, token, options = {}) {
  return frame.evaluate(({ providerID, token, allowPageEvidence }) => {
    const visible = (element) => {
      const style = getComputedStyle(element)
      const box = element.getBoundingClientRect()
      return style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity || 1) > 0
        && box.width > 0 && box.height > 0
    }
    const descriptor = (element) => `${element.id || ''} ${element.className || ''} ${element.getAttribute('role') || ''}`.toLowerCase()
    const all = Array.from(document.querySelectorAll('*')).filter(visible)
    const bodyText = String(document.body?.innerText || '').replace(/\s+/g, ' ').slice(0, 2_000)
    const frameDescriptor = `${location.href} ${document.title} ${bodyText}`.toLowerCase()
    const frameHasVerificationEvidence = /(?:captcha|aliyun|alicloud|alibabacloud|verify|verification|challenge|security check|人机|验证|滑块|拖动|拖拽|最右|尽头)/i.test(frameDescriptor)
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
          || frameHasVerificationEvidence
          || allowPageEvidence
        if (providerID === 'aliyun-esa' && !aliyunScope) continue
        if (providerID === 'generic-slide-to-end' && !frameHasVerificationEvidence && !allowPageEvidence) continue
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
    }, { providerID, token, allowPageEvidence: Boolean(options.allowPageEvidence) })
}

async function locateSlider(page, providerID, options = {}) {
  const signal = options.signal
  // ESA injects the slider asynchronously after its challenge iframe and
  // image assets have loaded.  Fifteen seconds is too short on a cold proxy
  // connection (the DOM commonly appears around 10-12s), and a busy proxy
  // can take another round-trip before the SDK paints the control. Keep a
  // generous discovery budget while still leaving time for the drag itself.
  const deadlineAt = Number.isFinite(options.deadlineAt) ? options.deadlineAt : Date.now() + 45_000
  while (Date.now() < deadlineAt) {
    throwIfAborted(signal)
    for (const frame of page.frames()) {
      const token = sliderMarkerToken()
      try {
        if (!await markSliderCandidate(frame, providerID, token, {
          allowPageEvidence: options.allowPageEvidence,
        })) continue
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
  const startX = geometry.handleBox.x + geometry.handleBox.width / 2 + (Number(trajectory.startOffsetX) || 0)
  const startY = geometry.handleBox.y + geometry.handleBox.height / 2 + (Number(trajectory.startOffsetY) || 0)
  throwIfAborted(signal)
  const approachPoints = Array.isArray(trajectory.approachPoints) ? trajectory.approachPoints : []
  if (approachPoints.length > 0) {
    await page.mouse.move(
      startX + (Number(trajectory.approachStartX) || 0),
      startY + (Number(trajectory.approachStartY) || 0)
    )
    if (trajectory.approachStartHoldMilliseconds > 0) {
      await waitForPromiseOrAbort(wait(trajectory.approachStartHoldMilliseconds), signal)
    }
    const approachStartedAt = now()
    let approachScheduledAt = 0
    for (const point of approachPoints) {
      approachScheduledAt += point.delayMilliseconds
      const waitMilliseconds = approachScheduledAt - (now() - approachStartedAt)
      if (waitMilliseconds > 0) await waitForPromiseOrAbort(wait(waitMilliseconds), signal)
      throwIfAborted(signal)
      await page.mouse.move(startX + point.x, startY + point.y)
    }
  } else {
    await page.mouse.move(startX, startY)
  }
  if (trajectory.hoverMilliseconds > 0) {
    await waitForPromiseOrAbort(wait(trajectory.hoverMilliseconds), signal)
  }
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
    if (trajectory.holdMilliseconds > 0) {
      await waitForPromiseOrAbort(wait(trajectory.holdMilliseconds), signal)
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
  return execFileAsync(executable, argumentsList.map((value) => String(value)), {
    timeout,
    windowsHide: true,
  })
}

async function focusNativeBrowserWindow(page, processID, execute, options = {}) {
  if (!Number.isInteger(processID) || processID <= 0) {
    throw new Error('native challenge browser pid is unavailable')
  }
  const searchResult = await execute(['search', '--onlyvisible', '--pid', processID], options)
  const windowIDs = String(searchResult?.stdout || searchResult || '')
    .split(/\s+/)
    .filter((value) => /^\d+$/.test(value))
  if (windowIDs.length === 0) throw new Error(`native challenge browser window was not found for pid ${processID}`)

  let windowID = windowIDs[0]
  const pageTitle = typeof page.title === 'function' ? await page.title().catch(() => '') : ''
  if (pageTitle && windowIDs.length > 1) {
    for (const candidate of windowIDs) {
      const nameResult = await execute(['getwindowname', candidate], options).catch(() => null)
      if (String(nameResult?.stdout || nameResult || '').includes(pageTitle)) {
        windowID = candidate
        break
      }
    }
  }
  await execute(['windowraise', windowID], options)
  await execute(['windowfocus', '--sync', windowID], options)
  return Number(windowID)
}

async function resizeNativeBrowserWindow(processID, width, height, options = {}) {
  if (!Number.isInteger(processID) || processID <= 0) {
    throw new Error('native browser pid is unavailable')
  }
  if (!Number.isInteger(width) || width <= 0 || !Number.isInteger(height) || height <= 0) {
    throw new Error('native browser window dimensions must be positive integers')
  }
  const execute = options.executeXdotool || runXdotool
  const searchResult = await execute(['search', '--onlyvisible', '--pid', processID], options)
  const windowID = String(searchResult?.stdout || searchResult || '')
    .split(/\s+/)
    .find((value) => /^\d+$/.test(value))
  if (!windowID) throw new Error(`native browser window was not found for pid ${processID}`)

  // Resizing does not raise or focus the window. Later lanes can therefore be
  // prepared while another lane owns the challenge lock and native pointer.
  await execute(['windowsize', '--sync', windowID, width, height], options)
  return Number(windowID)
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
  const nativeWindowID = await focusNativeBrowserWindow(
    page,
    Number(options.nativeWindowPID),
    execute,
    options
  )
  let screen = options.screenGeometry || await pageScreenGeometry(page)
  if (!options.screenGeometry) {
    screen = await calibrateNativeScreenGeometry(page, screen, {
      ...options,
      execute,
    })
  }
  const scale = Number.isFinite(screen.scale) && screen.scale > 0 ? screen.scale : 1
  const startViewportX = geometry.handleBox.x + geometry.handleBox.width / 2 + (Number(trajectory.startOffsetX) || 0)
  const startViewportY = geometry.handleBox.y + geometry.handleBox.height / 2 + (Number(trajectory.startOffsetY) || 0)
  const toScreen = (x, y) => [
    Math.round(screen.left + x * scale),
    Math.round(screen.top + y * scale),
  ]
  const [startX, startY] = toScreen(startViewportX, startViewportY)

  options.onNativeDragStart?.({ screen, geometry, startX, startY, nativeWindowID })
  throwIfAborted(signal)
  // --sync is intentionally used only for the initial positioning.  Repeating
  // it for trajectory points can block forever when two points round to the
  // same pixel, especially on a 1x Xvfb display.
  const approachPoints = Array.isArray(trajectory.approachPoints) ? trajectory.approachPoints : []
  if (approachPoints.length > 0) {
    const [approachX, approachY] = toScreen(
      startViewportX + (Number(trajectory.approachStartX) || 0),
      startViewportY + (Number(trajectory.approachStartY) || 0)
    )
    await execute(['mousemove', '--sync', approachX, approachY], options)
    if (trajectory.approachStartHoldMilliseconds > 0) {
      await waitForPromiseOrAbort(wait(trajectory.approachStartHoldMilliseconds), signal)
    }
    const approachStartedAt = now()
    let approachScheduledAt = 0
    let previousApproachX = approachX
    let previousApproachY = approachY
    for (const point of approachPoints) {
      approachScheduledAt += point.delayMilliseconds
      const waitMilliseconds = approachScheduledAt - (now() - approachStartedAt)
      if (waitMilliseconds > 0) await waitForPromiseOrAbort(wait(waitMilliseconds), signal)
      throwIfAborted(signal)
      const [x, y] = toScreen(startViewportX + point.x, startViewportY + point.y)
      if (x === previousApproachX && y === previousApproachY) continue
      await execute(['mousemove', x, y], options)
      previousApproachX = x
      previousApproachY = y
    }
  } else {
    await execute(['mousemove', '--sync', startX, startY], options)
  }
  if (trajectory.hoverMilliseconds > 0) {
    await waitForPromiseOrAbort(wait(trajectory.hoverMilliseconds), signal)
  }
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
    if (trajectory.holdMilliseconds > 0) {
      await waitForPromiseOrAbort(wait(trajectory.holdMilliseconds), signal)
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
      detect: genericSlideSnapshotDetected,
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
  const validCookieURL = (value) => {
    if (typeof value !== 'string' || value.length === 0) return false
    try {
      return ['http:', 'https:'].includes(new URL(value).protocol)
    } catch {
      return false
    }
  }
  const validCookie = (cookie) => {
    if (!cookie || typeof cookie !== 'object' || Array.isArray(cookie)
      || typeof cookie.name !== 'string' || cookie.name.length === 0
      || typeof cookie.value !== 'string') return false
    const hasURL = validCookieURL(cookie.url)
    const hasDomainAndPath = typeof cookie.domain === 'string' && cookie.domain.length > 0
      && typeof cookie.path === 'string' && cookie.path.startsWith('/')
    if (!hasURL && !hasDomainAndPath) return false
    if (cookie.expires !== undefined && !Number.isFinite(cookie.expires)) return false
    if (cookie.httpOnly !== undefined && typeof cookie.httpOnly !== 'boolean') return false
    if (cookie.secure !== undefined && typeof cookie.secure !== 'boolean') return false
    if (cookie.sameSite !== undefined && !['Strict', 'Lax', 'None'].includes(cookie.sameSite)) return false
    if (cookie.partitionKey !== undefined && typeof cookie.partitionKey !== 'string') return false
    return true
  }
  const validOrigin = (entry) => entry && typeof entry === 'object' && !Array.isArray(entry)
    && validCookieURL(entry.origin)
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
      // A corrupt session must never keep a lane in a restart loop.  Cleanup
      // is best effort; the next load will still ignore the same file and a
      // later successful solve can replace it atomically.
      try {
        fs.rmSync(file, { force: true })
      } catch {
        // The session directory may be temporarily read-only.  Treat the
        // state as absent and let the caller rebuild it in memory.
      }
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

function isExpectedNavigationAbort(error) {
  return /\b(?:NS_BINDING_ABORTED|NS_ERROR_ABORT|ERR_ABORTED)\b/i.test(String(error?.message || ''))
}

async function navigateHome(page, origin, timeoutMilliseconds) {
  try {
    return await page.goto(`${origin}/`, {
      waitUntil: 'domcontentloaded',
      timeout: timeoutMilliseconds,
    })
  } catch (error) {
    if (error?.name !== 'TimeoutError' && !isExpectedNavigationAbort(error)) throw error
    if (error?.name !== 'TimeoutError' && typeof page.waitForLoadState === 'function') {
      await page.waitForLoadState('domcontentloaded', {
        timeout: Math.min(5_000, timeoutMilliseconds),
      }).catch(() => {})
    }
    return null
  }
}

async function reloadChallenge(page, timeoutMilliseconds) {
  try {
    return await page.reload({ waitUntil: 'domcontentloaded', timeout: timeoutMilliseconds })
  } catch (error) {
    if (error?.name !== 'TimeoutError' && !isExpectedNavigationAbort(error)) throw error
    if (error?.name !== 'TimeoutError' && typeof page.waitForLoadState === 'function') {
      await page.waitForLoadState('domcontentloaded', {
        timeout: Math.min(5_000, timeoutMilliseconds),
      }).catch(() => {})
    }
    return null
  }
}

/**
 * Aliyun's ESA page does not report a successful drag through a DOM event.  Its
 * success callback submits the one-time `u_atoken`/`u_asig` pair by navigating
 * the current page back to the protected URL.  Starting a navigation waiter
 * before the mouse-up and giving that submission a chance to finish is
 * essential: issuing an immediate `page.goto(origin)` races the callback and
 * aborts the verification request, which makes a valid drag look like F001.
 *
 * The helper intentionally returns `null` when no navigation happened (a
 * failed drag or a test double without Playwright's navigation API).  The
 * caller can then perform its normal reload/home navigation and try again.
 */
function startChallengeNavigationWait(page, timeoutMilliseconds) {
  if (!page || typeof page.waitForNavigation !== 'function') return null
  // Production ESA callbacks have occasionally started just after ten
  // seconds. Waiting a little longer avoids racing that callback with the
  // fallback home request while remaining inside the 90-second solve budget.
  const timeout = Math.max(250, Math.min(15_000, Number(timeoutMilliseconds) || 8_000))
  // Attach the rejection handler to the Playwright promise immediately.  A
  // context can be closed while the drag is still in progress (lane restart,
  // lease loss, or an aborted solve); in that case the waiter rejects after
  // the caller has already left this attempt.  Leaving the raw promise
  // unobserved would produce an unhandled-rejection warning in the worker.
  // Every navigation failure is treated as "no callback navigation" so the
  // normal home request remains the recovery path.  A page-close/abort is
  // still surfaced by the drag or the subsequent home request itself.
  let navigation
  try {
    // Invoke waitForNavigation synchronously so the listener is installed
    // before the first mouse event is sent by the drag implementation.
    navigation = page.waitForNavigation({
      waitUntil: 'domcontentloaded',
      timeout,
    })
  } catch {
    return Promise.resolve(null)
  }
  return Promise.resolve(navigation).then(
    (response) => response || null,
    () => null
  )
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
      if (options.signal?.aborted || this.stopSignal?.aborted) {
        // A lane can be cancelled while waiting for the shared challenge lock.
        // Publish a terminal state even though solveLocked never started; an
        // otherwise harmless lease loss would leave the lane displayed as
        // permanently queued until the next challenge attempt.
        report({ state: 'cancelled' })
        throw abortReason(options.signal?.aborted ? options.signal : this.stopSignal)
      }
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
      let snapshot = withChallengeResponseContext(
        await this.operation(this.inspect(page, response), signal),
        'initial-navigation'
      )
      // ESA's loader script can remain on an otherwise normal page after a
      // successful verification. Treat the page as clear when the challenge
      // DOM/copy is gone; a stale http_custom header on the navigation is
      // accepted only when the resulting document is a real normal page.
      if (!challengeSnapshotDetected(snapshot)
        || challengeCleared(snapshot)
        || challengeContentCleared(snapshot)) {
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
          snapshot = withChallengeResponseContext(
            await this.operation(this.inspect(page, response), signal),
            'reload-navigation'
          )
          if (challengeCleared(snapshot) || challengeContentCleared(snapshot)) {
            await this.persistSolvedSession(context, provider.id, proxy)
            const solvedAt = new Date(this.now()).toISOString()
            report({ state: 'solved', provider: provider.id, attempt, solvedAt })
            return { state: 'solved', provider: provider.id, attempt }
          }
          provider = selectChallengeProvider(snapshot, this.providers) || provider
        }

        let geometry = await this.operation(provider.locate(page, {
          signal,
          deadlineAt: this.now() + Math.min(45_000, this.timeoutMilliseconds),
          allowPageEvidence: challengePageEvidenceDetected(snapshot),
        }), signal)
        // A valid ESA page can briefly expose only the verification shell
        // while its SDK replaces the shell with the slider. Refresh once on
        // the first miss so that this transient state is not misclassified as
        // an unsupported challenge and parked for the six-hour backoff.
        if (!geometry && attempt === 0) {
          response = await this.operation(this.reload(page, this.navigationTimeoutMilliseconds, signal), signal)
          snapshot = withChallengeResponseContext(
            await this.operation(this.inspect(page, response), signal),
            'reload-navigation'
          )
          if (challengeCleared(snapshot) || challengeContentCleared(snapshot)) {
            await this.persistSolvedSession(context, provider.id, proxy)
            const solvedAt = new Date(this.now()).toISOString()
            report({ state: 'solved', provider: provider.id, attempt, solvedAt })
            return { state: 'solved', provider: provider.id, attempt }
          }
          provider = selectChallengeProvider(snapshot, this.providers) || provider
          geometry = await this.operation(provider.locate(page, {
            signal,
            deadlineAt: this.now() + Math.min(45_000, this.timeoutMilliseconds),
            allowPageEvidence: challengePageEvidenceDetected(snapshot),
          }), signal)
        }
        if (!geometry) {
          throw new ChallengeError('unsupported', `${provider.id} challenge has no supported visible slide-to-end control`)
        }
        const distance = computeDragDistance(geometry.trackBox, geometry.handleBox)
        const trajectory = generateDragTrajectory(distance, { random: this.random })
        attempt += 1
        this.contextAttempts.set(context, attempt)
        report({ state: 'solving', provider: provider.id, attempt })
        // ESA submits its verification token by navigating from the challenge
        // page.  Arm the waiter before the drag so the navigation cannot be
        // cancelled by the fallback home request below.
        const submissionNavigation = startChallengeNavigationWait(
          page,
          Math.min(this.navigationTimeoutMilliseconds, this.timeoutMilliseconds)
        )
        await this.operation(this.drag(page, geometry, trajectory, {
          signal,
          nativeDrag: this.nativeDrag,
          nativeWindowPID: options.nativeWindowPID,
          onNativeDragStart: this.nativeDragDebug
            ? (values) => this.logger.log?.(`${new Date().toISOString()} native challenge drag geometry: ${JSON.stringify(values)}`)
            : undefined,
          onNativeDragError: (error) => this.logger.warn?.(`${new Date().toISOString()} native challenge drag unavailable; falling back to Playwright mouse: ${String(error?.message || error)}`),
        }), signal)

        // Prefer the response produced by the captcha callback.  Only issue a
        // fresh home navigation when no callback navigation occurred (failed
        // drag, an old SDK variant, or a lightweight test double).
        response = submissionNavigation
          ? await this.operation(submissionNavigation, signal)
          : null
        const responseContext = response ? 'post-drag-submission' : 'post-drag-fallback'
        if (!response) {
          response = await this.operation(this.navigate(page, this.navigationTimeoutMilliseconds, signal), signal)
        }
        snapshot = withChallengeResponseContext(
          await this.operation(this.inspect(page, response), signal),
          responseContext
        )
        if (challengeCleared(snapshot) || challengeContentCleared(snapshot)) {
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
      return await this.saveSession(context, providerID, proxy)
    } catch (error) {
      this.logger.error?.(`${new Date().toISOString()} failed to save verification session: ${String(error?.message || error)}`)
      throw new ChallengeError(
        'failed',
        `verification succeeded but its session could not be saved: ${String(error?.message || error)}`,
        { cause: error }
      )
    }
  }
}

async function recoverChallengeAcrossProxyPool(options = {}) {
  const poolSize = Number(options.poolSize) || 0
  const currentIndex = Number(options.currentIndex) || 0
  const signal = options.signal
  if (poolSize < 1 || typeof options.solveCurrent !== 'function' || typeof options.switchTo !== 'function') {
    throw new Error('challenge proxy recovery requires a pool, current solver, and switch callback')
  }
  throwIfAborted(signal)
  let lastError
  try {
    await options.solveCurrent()
    return currentIndex
  } catch (error) {
    throwIfAborted(signal)
    lastError = error
  }

  for (let offset = 1; offset < poolSize; offset += 1) {
    throwIfAborted(signal)
    const proxyIndex = (currentIndex + offset) % poolSize
    try {
      await options.switchTo(proxyIndex)
      return proxyIndex
    } catch (error) {
      throwIfAborted(signal)
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
  challengeContentCleared,
  challengeCleared,
  challengeSnapshotDetected,
  challengeTextDetected,
  challengePageEvidenceDetected,
  genericSlideSnapshotDetected,
  collectChallengeSnapshot,
  computeDragDistance,
  defaultChallengeProviders,
  dragSlider,
  dragSliderNative,
  generateDragTrajectory,
  isChallengeError,
  isExpectedNavigationAbort,
  isHTTPCustomDenial,
  readStoredSession,
  recoverChallengeAcrossProxyPool,
  resizeNativeBrowserWindow,
  retryFailedChallengeOperation,
  selectChallengeProvider,
  sessionDigest,
  sessionFilePath,
  shouldBlockResource,
  validStorageState,
  writeStoredSession,
}
