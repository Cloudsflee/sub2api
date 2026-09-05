const crypto = require('node:crypto')

const DEFAULT_INITIAL_RATE = 1.5
const DEFAULT_MINIMUM_RATE = 0.5
const DEFAULT_MAXIMUM_RATE = 2
const DEFAULT_SUCCESS_THRESHOLD = 100
const DEFAULT_RECOVERY_QUIET_MILLISECONDS = 5 * 60_000
const DEFAULT_EGRESS_CIRCUIT_MILLISECONDS = 10 * 60_000
const DEFAULT_ACCESS_DENIED_CLUSTER_MILLISECONDS = 60_000
const DEFAULT_GLOBAL_SILENCE_MILLISECONDS = 120_000

class AdaptiveRateDeadlineError extends Error {
  constructor(message = 'adaptive request rate wait exceeds the operation deadline') {
    super(message)
    this.name = 'AdaptiveRateDeadlineError'
    this.deadlineExceeded = true
  }
}

function abortReason(signal) {
  if (signal?.reason instanceof Error) return signal.reason
  return new Error(String(signal?.reason || 'operation aborted'))
}

function throwIfAborted(signal) {
  if (signal?.aborted) throw abortReason(signal)
}

function waitForPromiseOrAbort(promise, signal) {
  if (!signal) return promise
  if (signal.aborted) {
    Promise.resolve(promise).catch(() => {})
    return Promise.reject(abortReason(signal))
  }
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

function clampRate(value, minimum, maximum, name) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) throw new Error(`${name} must be a positive number`)
  return Math.min(maximum, Math.max(minimum, parsed))
}

function finiteMilliseconds(value, fallback, name) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed < 0) {
    if (value === undefined) return fallback
    throw new Error(`${name} must be a non-negative number`)
  }
  return parsed
}

function proxyEgressID(proxy) {
  if (!proxy) return 'egress-direct'
  const identity = JSON.stringify({
    server: String(proxy.server || ''),
    username: String(proxy.username || ''),
    password: String(proxy.password || ''),
  })
  return `egress-${crypto.createHash('sha256').update(identity).digest('hex').slice(0, 12)}`
}

function redactDiagnosticValue(value) {
  return String(value || '')
    .replace(/\b(https?|socks5):\/\/[^\s/@]+(?::[^\s/@]*)?@/gi, '$1://***@')
    .replace(/\b(cookie|authorization|proxy-authorization)\s*:\s*[^\r\n]*/gi, '$1=***')
    .replace(/([?&](?:access_?token|token|api_?key|cookie|signature|sign|sig|auth|authorization)=)[^&#\s"']+/gi, '$1***')
    .replace(/\b(cookie|authorization|proxy-authorization|access_?token|token|api_?key|signature|sign|sig)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, '$1=***')
    .replace(/[\r\n\t]+/g, ' ')
    .replace(/\s{2,}/g, ' ')
    .trim()
}

function sanitizedDiagnosticValue(value, maximumLength) {
  return redactDiagnosticValue(value).slice(0, maximumLength)
}

function responseTitleSummary(body) {
  const match = String(body || '').match(/<title\b[^>]*>([\s\S]*?)<\/title>/i)
  if (!match) return ''
  return sanitizedDiagnosticValue(match[1]
    .replace(/<[^>]+>/g, ' ')
    .replace(/&nbsp;/gi, ' ')
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'"), 160)
}

function canonicalAPIPath(value) {
  try {
    return new URL(String(value || ''), 'https://product-sync.invalid').pathname.slice(0, 160)
  } catch {
    return sanitizedDiagnosticValue(String(value || '').split(/[?#]/, 1)[0], 160)
  }
}

function buildWAFResponseFingerprint(result, options = {}) {
  const body = String(result?.text || '')
  const suppliedHash = String(result?.bodySHA256 || '').trim().toLowerCase()
  const bodyHash = /^[a-f0-9]{64}$/.test(suppliedHash)
    ? suppliedHash
    : crypto.createHash('sha256').update(body).digest('hex')
  const suppliedLength = Number(result?.bodyLength)
  const bodyLength = Number.isInteger(suppliedLength) && suppliedLength >= 0
    ? suppliedLength
    : Buffer.byteLength(body)
  return {
    observed_at: new Date(options.now === undefined ? Date.now() : options.now).toISOString(),
    status: Number(result?.status) || 0,
    api_path: canonicalAPIPath(options.path || result?.requestPath || result?.requestURL),
    server: sanitizedDiagnosticValue(result?.server, 120),
    x_tengine_error: sanitizedDiagnosticValue(result?.responseError, 160),
    body_length: bodyLength,
    body_sha256: bodyHash,
    title: responseTitleSummary(body),
    egress_id: sanitizedDiagnosticValue(options.egressID, 64),
  }
}

function parseRetryAfterMilliseconds(value, now = Date.now()) {
  const raw = String(value || '').trim()
  if (!raw) return 0
  if (/^\d+(?:\.\d+)?$/.test(raw)) {
    const seconds = Number(raw)
    return Number.isFinite(seconds) && seconds >= 0 ? Math.ceil(seconds * 1000) : 0
  }
  const at = Date.parse(raw)
  return Number.isFinite(at) ? Math.max(0, at - now) : 0
}

class AdaptiveRateLimiter {
  constructor(options = {}) {
    this.minimumRate = Number(options.minimumRate ?? DEFAULT_MINIMUM_RATE)
    this.maximumRate = Number(options.maximumRate ?? DEFAULT_MAXIMUM_RATE)
    if (!(this.minimumRate > 0) || !(this.maximumRate >= this.minimumRate)) {
      throw new Error('adaptive request rate bounds are invalid')
    }
    this.currentRate = clampRate(
      options.initialRate ?? DEFAULT_INITIAL_RATE,
      this.minimumRate,
      this.maximumRate,
      'adaptive initial request rate'
    )
    this.successThreshold = Math.max(1, Math.floor(Number(options.successThreshold ?? DEFAULT_SUCCESS_THRESHOLD)))
    this.recoveryQuietMilliseconds = finiteMilliseconds(
      options.recoveryQuietMilliseconds,
      DEFAULT_RECOVERY_QUIET_MILLISECONDS,
      'adaptive recovery quiet period'
    )
    this.egressCircuitMilliseconds = finiteMilliseconds(
      options.egressCircuitMilliseconds,
      DEFAULT_EGRESS_CIRCUIT_MILLISECONDS,
      'egress circuit period'
    )
    this.accessDeniedClusterMilliseconds = finiteMilliseconds(
      options.accessDeniedClusterMilliseconds,
      DEFAULT_ACCESS_DENIED_CLUSTER_MILLISECONDS,
      'access-denied cluster period'
    )
    this.globalSilenceMilliseconds = finiteMilliseconds(
      options.globalSilenceMilliseconds,
      DEFAULT_GLOBAL_SILENCE_MILLISECONDS,
      'global silence period'
    )
    this.now = options.now || Date.now
    this.sleep = options.sleep || ((milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)))
    this.startedAt = this.now()
    this.nextRequestAt = this.startedAt
    this.lastPressureAt = 0
    this.pressureUntil = 0
    this.globalSilenceUntil = 0
    this.lastPressureKind = ''
    this.hasPressure = false
    this.consecutiveSuccesses = 0
    this.egresses = new Map()
    this.recentAccessDenied = []
    this.lastWAFFingerprint = null
    this.pending = Promise.resolve()
  }

  take(signal, deadlineAt = Number.POSITIVE_INFINITY) {
    const request = this.pending.then(() => this.takeNext(signal, deadlineAt))
    this.pending = request.catch(() => {})
    return request
  }

  async takeNext(signal, deadlineAt) {
    while (true) {
      throwIfAborted(signal)
      const now = this.now()
      const readyAt = Math.max(this.nextRequestAt, this.globalSilenceUntil)
      if (readyAt >= deadlineAt) throw new AdaptiveRateDeadlineError()
      if (readyAt > now) {
        await waitForPromiseOrAbort(this.sleep(Math.max(1, Math.ceil(readyAt - now))), signal)
        continue
      }
      this.nextRequestAt = now + (1000 / this.currentRate)
      return now
    }
  }

  setRate(rate, now = this.now()) {
    const next = clampRate(rate, this.minimumRate, this.maximumRate, 'adaptive request rate')
    const changed = Math.abs(next - this.currentRate) > 1e-9
    this.currentRate = next
    if (changed) this.nextRequestAt = Math.max(this.nextRequestAt, now + (1000 / next))
    return changed
  }

  egressState(egressID) {
    const id = String(egressID || 'egress-direct')
    let state = this.egresses.get(id)
    if (!state) {
      state = { circuitUntil: 0, consecutiveTransportFailures: 0 }
      this.egresses.set(id, state)
    }
    return state
  }

  recordSuccess(egressID) {
    const now = this.now()
    this.egressState(egressID).consecutiveTransportFailures = 0
    this.consecutiveSuccesses += 1
    const quietSince = this.hasPressure ? this.lastPressureAt : this.startedAt
    let rateChanged = false
    if (this.consecutiveSuccesses >= this.successThreshold
      && now - quietSince >= this.recoveryQuietMilliseconds) {
      rateChanged = this.setRate(this.currentRate + 0.1, now)
      this.consecutiveSuccesses = 0
    }
    return { rateChanged, ...this.snapshot(now) }
  }

  recordRatePressure(kind, options = {}) {
    const now = this.now()
    const pressureKind = kind === 'access_denied' ? 'access_denied' : 'rate_limit'
    this.hasPressure = true
    this.lastPressureAt = now
    this.lastPressureKind = pressureKind
    this.consecutiveSuccesses = 0
    this.setRate(this.currentRate / 2, now)
    const retryAfterMilliseconds = Math.max(0, Number(options.retryAfterMilliseconds) || 0)
    this.pressureUntil = Math.max(this.pressureUntil, now + retryAfterMilliseconds)

    let circuitUntil = 0
    if (pressureKind === 'access_denied') {
      const egressID = String(options.egressID || 'egress-direct')
      const state = this.egressState(egressID)
      state.circuitUntil = Math.max(state.circuitUntil, now + this.egressCircuitMilliseconds)
      state.consecutiveTransportFailures = 0
      circuitUntil = state.circuitUntil
      this.recentAccessDenied = this.recentAccessDenied.filter((event) => (
        now - event.at <= this.accessDeniedClusterMilliseconds
      ))
      const distinctPressure = this.recentAccessDenied.some((event) => event.egressID !== egressID)
      this.recentAccessDenied.push({ egressID, at: now })
      if (distinctPressure) {
        this.globalSilenceUntil = Math.max(this.globalSilenceUntil, now + this.globalSilenceMilliseconds)
        this.pressureUntil = Math.max(this.pressureUntil, this.globalSilenceUntil)
        this.setRate(this.minimumRate, now)
      }
      if (options.fingerprint) this.lastWAFFingerprint = { ...options.fingerprint }
    }

    return { circuitUntil, ...this.snapshot(now) }
  }

  recordFailure() {
    this.consecutiveSuccesses = 0
    return this.snapshot()
  }

  recordTransportFailure(egressID) {
    this.consecutiveSuccesses = 0
    const state = this.egressState(egressID)
    state.consecutiveTransportFailures += 1
    const failureCount = state.consecutiveTransportFailures
    const rotate = failureCount >= 2
    if (rotate) state.consecutiveTransportFailures = 0
    return { failureCount, rotate }
  }

  isCircuitOpen(egressID, now = this.now()) {
    return this.egressState(egressID).circuitUntil > now
  }

  circuitUntil(egressID) {
    return this.egressState(egressID).circuitUntil
  }

  nextAvailableProxyIndex(proxyPool, currentIndex, now = this.now()) {
    if (!Array.isArray(proxyPool) || proxyPool.length < 2) return -1
    for (let offset = 1; offset < proxyPool.length; offset += 1) {
      const index = (currentIndex + offset) % proxyPool.length
      if (!this.isCircuitOpen(proxyEgressID(proxyPool[index]), now)) return index
    }
    return -1
  }

  snapshot(now = this.now()) {
    const circuits = []
    for (const [egressID, state] of this.egresses) {
      if (state.circuitUntil > now) {
        circuits.push({
          egress_id: egressID,
          circuit_open_until: new Date(state.circuitUntil).toISOString(),
        })
      }
    }
    circuits.sort((left, right) => left.egress_id.localeCompare(right.egress_id))
    const silent = this.globalSilenceUntil > now
    const pressured = this.hasPressure && Math.max(
      this.pressureUntil,
      this.lastPressureAt + this.recoveryQuietMilliseconds
    ) > now
    const state = silent ? 'silent'
      : pressured ? 'pressured'
        : this.hasPressure && this.currentRate < this.maximumRate ? 'recovering' : 'clear'
    const pressureUntil = silent
      ? this.globalSilenceUntil
      : pressured ? Math.max(this.pressureUntil, this.lastPressureAt + this.recoveryQuietMilliseconds) : 0
    return {
      adaptive_rate_per_second: Number(this.currentRate.toFixed(2)),
      adaptive_success_streak: this.consecutiveSuccesses,
      global_pressure_state: state,
      global_pressure_until: pressureUntil > now ? new Date(pressureUntil).toISOString() : '',
      global_silence_until: silent ? new Date(this.globalSilenceUntil).toISOString() : '',
      last_pressure_kind: this.lastPressureKind,
      egress_circuits: circuits,
      waf_response_fingerprint: this.lastWAFFingerprint ? { ...this.lastWAFFingerprint } : null,
    }
  }
}

module.exports = {
  AdaptiveRateDeadlineError,
  AdaptiveRateLimiter,
  DEFAULT_ACCESS_DENIED_CLUSTER_MILLISECONDS,
  DEFAULT_EGRESS_CIRCUIT_MILLISECONDS,
  DEFAULT_GLOBAL_SILENCE_MILLISECONDS,
  DEFAULT_INITIAL_RATE,
  DEFAULT_MAXIMUM_RATE,
  DEFAULT_MINIMUM_RATE,
  DEFAULT_RECOVERY_QUIET_MILLISECONDS,
  DEFAULT_SUCCESS_THRESHOLD,
  buildWAFResponseFingerprint,
  parseRetryAfterMilliseconds,
  proxyEgressID,
  redactDiagnosticValue,
}
