const { chromium, firefox } = require('playwright-core')
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const { collectAuthoritativeSnapshot } = require('./catalog-sync')
const {
  ChallengeManager,
  challengeBackoffMilliseconds,
  challengeContentCleared,
  challengeCleared,
  collectChallengeSnapshot,
  isChallengeError,
  isExpectedNavigationAbort,
  recoverChallengeAcrossProxyPool,
  resizeNativeBrowserWindow,
  retryFailedChallengeOperation,
  shouldBlockResource,
} = require('./challenge-manager')
const {
  JobLeaseLostError,
  Semaphore,
  ShopSyncError,
  TokenBucket,
  browserContextIsReady,
  browserFingerprintSeed,
  browserProfileProcessInfo,
  browserProcessIDForProfile,
  browserResourceCounts,
  camoufoxFirefoxUserPrefs,
  createJobHeartbeat,
  initializeProxyPool,
  isPressureError,
  parseBoolean,
  parsePositiveMilliseconds,
  parseProxyConfigurations,
  parseRequestRatePerLane,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  pressureRecoveryMode,
  proxyPoolsForConcurrency,
  redactURLCredentials,
  shopRequestError,
  takeRequestTokens,
  throwIfAborted,
  waitWithStatusRefresh,
  withPressureRecovery,
} = require('./worker-utils')

const backendURL = process.env.BACKEND_URL || 'http://sub2api:8080'
const backendToken = String(process.env.PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN || '').trim()
const shopOrigin = 'https://pay.ldxp.cn'
const shopHomeURL = `${shopOrigin}/`
const idlePollMilliseconds = 10_000
const activePollMilliseconds = 10_000
const heartbeatMilliseconds = 30_000
const maxJobMilliseconds = 30 * 60_000
const shopRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.SHOP_REQUEST_TIMEOUT_MILLISECONDS, 20_000, 'SHOP_REQUEST_TIMEOUT_MILLISECONDS')
const browserProtocolTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS, 45_000, 'BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS')
const backendRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BACKEND_REQUEST_TIMEOUT_MILLISECONDS, 10_000, 'BACKEND_REQUEST_TIMEOUT_MILLISECONDS')
const challengeTimeoutMilliseconds = parsePositiveMilliseconds(process.env.PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS, 90_000, 'PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS')
const syncConcurrency = parseSyncConcurrency(process.env.PRODUCT_SYNC_CONCURRENCY)
const requestRatePerLane = parseRequestRatePerLane(process.env.PRODUCT_SYNC_REQUEST_RATE_PER_LANE)
const challengeAutoSolveEnabled = parseBoolean(
  process.env.PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE,
  false,
  'PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE'
)
const challengeNativeDragEnabled = parseBoolean(
  process.env.PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG,
  false,
  'PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG'
)
const challengeNativeDragDebug = parseBoolean(
  process.env.PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG_DEBUG,
  false,
  'PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG_DEBUG'
)
const browserEngine = String(process.env.PRODUCT_SYNC_BROWSER || 'chromium').trim().toLowerCase()
if (!['chromium', 'camoufox'].includes(browserEngine)) {
  throw new Error(`PRODUCT_SYNC_BROWSER must be chromium or camoufox, got ${browserEngine}`)
}
const chromePath = process.env.CHROME_PATH || '/usr/bin/google-chrome-stable'
const camoufoxPath = process.env.CAMOUFOX_PATH || '/opt/camoufox/camoufox'
const browserExecutablePath = browserEngine === 'camoufox' ? camoufoxPath : chromePath
const browserType = browserEngine === 'camoufox' ? firefox : chromium
const statusFile = process.env.STATUS_FILE || '/data/status.json'
const challengeSessionDirectory = process.env.PRODUCT_SYNC_CHALLENGE_SESSION_DIR || '/data/challenge-sessions'
const browserProfileDirectory = process.env.PRODUCT_SYNC_BROWSER_PROFILE_DIR || '/data/browser-profiles'
const camoufoxWindow = { width: 1024, height: 824 }
const configuredProxies = parseProxyConfigurations(
  process.env.PRODUCT_SYNC_PROXY_URLS,
  process.env.PRODUCT_SYNC_PROXY_URL
)
const configuredFallbackProxies = parseProxyConfigurations(
  process.env.PRODUCT_SYNC_PROXY_FALLBACK_URLS,
  '',
  'PRODUCT_SYNC_PROXY_FALLBACK_URLS'
)
const laneProxyPools = proxyPoolsForConcurrency(syncConcurrency, configuredProxies, configuredFallbackProxies)
const laneProxies = laneProxyPools.map((pool) => pool[0])
const configuredProxyCount = configuredProxies.length + configuredFallbackProxies.length
const globalRequestRate = Number((syncConcurrency * requestRatePerLane).toFixed(10))
const globalRequestLimiter = new TokenBucket(globalRequestRate, 1)
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))
const workerStopController = new AbortController()
const challengeManager = new ChallengeManager({
  enabled: challengeAutoSolveEnabled,
  sessionDirectory: challengeSessionDirectory,
  timeoutMilliseconds: challengeTimeoutMilliseconds,
  navigationTimeoutMilliseconds: Math.min(20_000, browserProtocolTimeoutMilliseconds),
  nativeDrag: challengeNativeDragEnabled,
  nativeDragDebug: challengeNativeDragDebug,
  stopSignal: workerStopController.signal,
})
let workerStatus = {
  state: 'starting',
  proxy_enabled: configuredProxyCount > 0,
  proxy_count: configuredProxyCount,
  browser_engine: browserEngine,
  sync_concurrency: syncConcurrency,
  request_rate_per_second_per_lane: requestRatePerLane,
  request_rate_per_second_global: globalRequestRate,
  challenge_auto_solve_enabled: challengeAutoSolveEnabled,
  challenge_native_drag_enabled: challengeNativeDragEnabled,
  started_at: new Date().toISOString(),
}
const laneStatuses = Array.from({ length: syncConcurrency }, (_, index) => ({
  index,
  state: 'starting',
  challenge_provider: '',
  challenge_state: challengeAutoSolveEnabled ? 'idle' : 'disabled',
  challenge_attempt: 0,
  challenge_started_at: '',
  challenge_solved_at: '',
  session_restored: false,
}))
let activeBrowser
let activeBrowserContexts = []
let stopping = false

function ensureStatusFileDirectory() {
  const directory = path.dirname(statusFile)
  if (directory && directory !== '.') fs.mkdirSync(directory, { recursive: true })
}

function publishStatus(values) {
  const resources = browserResourceCounts(activeBrowserContexts)
  const lanes = laneStatuses.map((status, index) => ({
    ...status,
    context_ready: browserContextIsReady(activeBrowserContexts[index]),
  }))
  workerStatus = {
    ...workerStatus,
    ...values,
    lanes,
    browser_context_count: resources.contextCount,
    browser_page_count: resources.pageCount,
    updated_at: new Date().toISOString(),
  }
  const temporary = `${statusFile}.tmp`
  try {
    ensureStatusFileDirectory()
    fs.writeFileSync(temporary, `${JSON.stringify(workerStatus, null, 2)}\n`, { mode: 0o600 })
    fs.chmodSync(temporary, 0o600)
    fs.renameSync(temporary, statusFile)
  } catch (error) {
    console.error(`${new Date().toISOString()} failed to publish worker status: ${error.message}`)
  }
}

function publishLaneStatus(lane, values) {
  laneStatuses[lane.index] = {
    ...laneStatuses[lane.index],
    ...values,
    index: lane.index,
    updated_at: new Date().toISOString(),
  }
  const states = laneStatuses.map((status) => status.state)
  const readyStates = new Set(['idle', 'syncing', 'blocked'])
  const readyLaneCount = laneStatuses.filter((status, index) => (
    browserContextIsReady(activeBrowserContexts[index]) && readyStates.has(status.state)
  )).length
  const degradedLaneCount = laneStatuses.filter((status, index) => (
    !browserContextIsReady(activeBrowserContexts[index]) && ['blocked', 'error', 'restarting'].includes(status.state)
  )).length
  const state = stopping ? 'stopping'
    : states.includes('syncing') ? 'syncing'
      : readyLaneCount > 0 && degradedLaneCount > 0 ? 'degraded'
        : states.includes('blocked') ? 'blocked'
          : states.includes('idle') ? 'idle'
            : states.every((value) => value === 'error') ? 'error'
              : 'starting'
  publishStatus({
    state,
    active_jobs: states.filter((value) => value === 'syncing').length,
    ready_lane_count: readyLaneCount,
    degraded_lane_count: degradedLaneCount,
    lanes: laneStatuses,
  })
}

function errorMessage(error) {
  return redactURLCredentials(error?.message || error).replace(/[\r\n]+/g, ' ').slice(0, 500)
}

class BackendHTTPError extends Error {
  constructor(path, status) {
    super(`backend ${path} returned HTTP ${status}`)
    this.name = 'BackendHTTPError'
    this.path = path
    this.status = status
  }
}

function backendRequestSignal(externalSignal, timeoutMilliseconds = backendRequestTimeoutMilliseconds) {
  const controller = new AbortController()
  const timeout = Math.max(1, Number(timeoutMilliseconds) || 1)
  const timeoutError = new Error(`backend request timed out after ${timeout}ms`)
  timeoutError.name = 'TimeoutError'
  const onAbort = () => controller.abort(externalSignal.reason instanceof Error
    ? externalSignal.reason
    : new Error(String(externalSignal.reason || 'backend request aborted')))
  if (externalSignal?.aborted) onAbort()
  else externalSignal?.addEventListener('abort', onAbort, { once: true })
  const timer = setTimeout(() => controller.abort(timeoutError), timeout)
  return {
    signal: controller.signal,
    dispose() {
      clearTimeout(timer)
      externalSignal?.removeEventListener('abort', onAbort)
    },
  }
}

async function backend(path, options = {}, timeoutMilliseconds = backendRequestTimeoutMilliseconds) {
  const { signal: externalSignal, ...fetchOptions } = options
  const requestSignal = backendRequestSignal(externalSignal, timeoutMilliseconds)
  try {
    const response = await fetch(`${backendURL}${path}`, {
      ...fetchOptions,
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${backendToken}`,
        ...(options.headers || {}),
      },
      signal: requestSignal.signal,
    })
    if (!response.ok) throw new BackendHTTPError(path, response.status)
    const payload = await response.json()
    if (payload.code !== 0) throw new Error(payload.message || `backend ${path} failed`)
    return payload.data
  } finally {
    // Keep the timeout and external cancellation active through response.json().
    // A backend can send headers and then stall while streaming the body.
    requestSignal.dispose()
  }
}

function ensureJobDeadline(deadlineAt, signal) {
  throwIfAborted(signal)
  if (Date.now() >= deadlineAt) throw new Error('product sync job exceeded the 30 minute limit')
}

function laneContextIsReady(lane) {
  return Boolean(lane?.context && lane?.page
    && (typeof lane.page.isClosed !== 'function' || !lane.page.isClosed()))
}

function ensureLaneContextReady(lane) {
  if (laneContextIsReady(lane)) return
  const error = new Error(`product sync browser context for lane ${Number(lane?.index || 0) + 1} is unavailable`)
  error.restartLane = true
  throw error
}

async function postShopAPI(lane, shopToken, path, body, deadlineAt, signal) {
  ensureJobDeadline(deadlineAt, signal)
  await takeRequestTokens(lane.requestLimiter, globalRequestLimiter, signal)
  ensureJobDeadline(deadlineAt, signal)
  const requestURL = new URL(path, shopHomeURL)
  if (requestURL.origin !== shopOrigin) throw new Error(`shop API path escapes ${shopOrigin}`)
  let result
  try {
    result = await evaluateShopRequest(lane.page, evaluateShopRequestInBrowser, {
      requestPath: requestURL.href,
      requestBody: body,
      requestTimeoutMilliseconds: Math.min(shopRequestTimeoutMilliseconds, Math.max(1, deadlineAt - Date.now())),
      visitorID: `sub2api${shopToken.replace(/[^a-zA-Z0-9]/g, '').slice(0, 24)}`,
    }, Math.min(shopRequestTimeoutMilliseconds + 5_000, Math.max(1, deadlineAt - Date.now())), signal)
  } catch (error) {
    throwIfAborted(signal)
    // A protocol timeout is the last-resort lane recovery path. Browser-side
    // fetch/body timeouts are returned as structured results and remain
    // ordinary pressure errors so a single stuck response does not discard
    // the persistent context.
    if (error?.restartLane) throw error
    throw shopRequestError(path, error)
  }
  throwIfAborted(signal)
  if (result && typeof result === 'object') result.requestPath = path
  return parseShopHTTPResponse(result)
}

/**
 * Browser-side shop request. Keep the fetch and response body phases under a
 * single deadline so an upstream that sends headers but never finishes the
 * body cannot leave page.evaluate pending indefinitely.
 */
async function evaluateShopRequestInBrowser({ requestPath, requestBody, requestTimeoutMilliseconds, visitorID }) {
  const timeoutMilliseconds = Math.max(1, Number(requestTimeoutMilliseconds) || 1)
  const controller = new AbortController()
  const startedAt = performance.now()
  let timedOutPhase = ''

  const waitForPhase = async (promise, phase) => {
    const remaining = Math.max(1, timeoutMilliseconds - (performance.now() - startedAt))
    let timer
    try {
      return await Promise.race([
        promise,
        new Promise((resolve, reject) => {
          timer = setTimeout(() => {
            timedOutPhase = phase
            reject({ __shopBrowserTimeout: true, phase, timeoutMilliseconds })
            controller.abort()
          }, remaining)
        }),
      ])
    } finally {
      if (timer) clearTimeout(timer)
    }
  }

  try {
    const response = await waitForPhase(fetch(requestPath, {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        Visitorid: visitorID,
      },
      body: JSON.stringify(requestBody),
      signal: controller.signal,
    }), 'fetch')
    const contentType = response.headers.get('content-type') || ''
    const text = await waitForPhase(response.text(), 'response_body')
    let payload = null
    if (contentType.toLowerCase().includes('application/json')) {
      try {
        payload = JSON.parse(text)
      } catch {
        payload = null
      }
    }
    return {
      status: response.status,
      contentType,
      payload,
      text: text.slice(0, contentType.toLowerCase().includes('application/json') ? 2_000 : 512_000),
      responseURL: response.url || requestPath,
      responseError: response.headers.get('x-tengine-error') || '',
      retryAfter: response.headers.get('retry-after') || '',
    }
  } catch (error) {
    if (error?.__shopBrowserTimeout || timedOutPhase) {
      return {
        browserTimeout: true,
        timeoutPhase: error?.phase || timedOutPhase || 'request',
        timeoutMilliseconds,
      }
    }
    throw error
  }
}

/**
 * Evaluate a shop request with a Node-side deadline and cancellation race.
 *
 * Playwright's page.evaluate timeout only applies to the protocol command;
 * it does not necessarily interrupt a browser-side fetch that is stuck in
 * response.text().  Keep an independent timer here and tag timeout failures
 * for lane recreation.  The original protocol promise is observed after the
 * race so a late rejection cannot become an unhandled rejection.
 */
async function evaluateShopRequest(page, callback, args, timeoutMilliseconds, signal) {
  if (!page || typeof page.evaluate !== 'function') throw new Error('shop page is unavailable')
  const timeout = Math.max(1, Number(timeoutMilliseconds) || 1)
  let timer
  let abortHandler
  let operation
  try {
    operation = Promise.resolve().then(() => page.evaluate(callback, args))
    const timeoutPromise = new Promise((resolve, reject) => {
      timer = setTimeout(() => {
        const error = new ShopSyncError('network', `shop API browser evaluation timed out after ${timeout}ms`)
        error.restartLane = true
        error.timeoutPhase = 'protocol'
        error.timeoutMilliseconds = timeout
        reject(error)
      }, timeout)
    })
    const cancellationPromise = signal
      ? new Promise((resolve, reject) => {
        if (signal.aborted) {
          reject(signal.reason instanceof Error ? signal.reason : new Error(String(signal.reason || 'operation aborted')))
          return
        }
        abortHandler = () => reject(signal.reason instanceof Error
          ? signal.reason
          : new Error(String(signal.reason || 'operation aborted')))
        signal.addEventListener('abort', abortHandler, { once: true })
      })
      : new Promise(() => {})
    return await Promise.race([operation, timeoutPromise, cancellationPromise])
  } finally {
    if (timer) clearTimeout(timer)
    if (signal && abortHandler) signal.removeEventListener('abort', abortHandler)
    operation?.catch(() => {})
  }
}

function challengeStatusValues(event = {}) {
  const values = { challenge_state: event.state }
  if (event.state === 'queued' || event.state === 'detecting') {
    values.challenge_provider = ''
    values.challenge_attempt = 0
    values.challenge_started_at = ''
    values.challenge_solved_at = ''
  }
  if (event.provider !== undefined) values.challenge_provider = event.provider
  if (event.attempt !== undefined) values.challenge_attempt = event.attempt
  if (event.startedAt !== undefined) values.challenge_started_at = event.startedAt
  if (event.solvedAt !== undefined) values.challenge_solved_at = event.solvedAt
  return values
}

function publishChallengeState(lane, event) {
  publishLaneStatus(lane, challengeStatusValues(event))
}

async function solveChallengeForContext(lane, context, page, proxy, signal, challengeResponse) {
  const result = await challengeManager.solve({
    context,
    page,
    proxy,
    challengeResponse,
    nativeWindowPID: lane.browserProcessID,
    signal,
    onState: (event) => publishChallengeState(lane, event),
    setResourcesAllowed: (allowed) => { lane.challengeResourcesAllowed = allowed },
  })
  lane.challengeState.failureCount = 0
  return result
}

async function recoverLaneChallenge(lane, signal, challengeResponse) {
  try {
    const previousProxyIndex = lane.proxyIndex
    const recoveredProxyIndex = await recoverChallengeAcrossProxyPool({
      poolSize: lane.proxyPool.length,
      currentIndex: previousProxyIndex,
      signal,
      solveCurrent: () => solveChallengeForContext(
        lane,
        lane.context,
        lane.page,
        lane.proxy,
        signal,
        challengeResponse
      ),
      switchTo: (proxyIndex) => replaceLaneContext(lane, proxyIndex, true, signal),
    })
    if (recoveredProxyIndex !== previousProxyIndex) {
      console.log(`${new Date().toISOString()} lane ${lane.index + 1} switched to its fallback after verification recovery failed on the current exit`)
      publishLaneStatus(lane, {
        proxy_server: lane.proxy?.server || '',
        proxy_pool_position: recoveredProxyIndex + 1,
        proxy_rotated_at: new Date().toISOString(),
      })
    }
  } catch (error) {
    throw laneRecoveryCancellationFailure(lane, error, signal)
  }
}

async function postShopAPIWithChallengeRecovery(lane, shopToken, path, body, deadlineAt, signal) {
  return retryFailedChallengeOperation(
    () => postShopAPI(lane, shopToken, path, body, deadlineAt, signal),
    async ({ error }) => {
      ensureJobDeadline(deadlineAt, signal)
      await recoverLaneChallenge(lane, signal, error.challengeResponse)
      ensureJobDeadline(deadlineAt, signal)
    },
    { exhaustedMessage: `shop API ${path} repeatedly returned HTML after verification recovery` }
  )
}

async function postShopAPIWithPressureRecovery(lane, shopToken, path, body, deadlineAt, signal) {
  return withPressureRecovery(
    () => postShopAPIWithChallengeRecovery(lane, shopToken, path, body, deadlineAt, signal),
    {
      state: lane.pressureState,
      deadlineAt,
      signal,
      onBackoff: async ({ error, failureCount, waitMilliseconds }) => {
        publishLaneStatus(lane, {
          state: 'blocked',
          pressure_failure_count: failureCount,
          retry_at: new Date(Date.now() + waitMilliseconds).toISOString(),
          last_error_at: new Date().toISOString(),
          last_error: errorMessage(error),
        })
        console.log(`${new Date().toISOString()} lane ${lane.index + 1} preserving the active shop and applying product sync pressure backoff for ${Math.round(waitMilliseconds / 1000)} seconds`)
      },
      recover: async ({ failureCount }) => {
        if (stopping) throw new Error('product sync worker is stopping')
        await recoverLaneAfterPressure(lane, failureCount, signal)
        publishLaneStatus(lane, {
          state: 'syncing',
          retry_at: '',
        })
      },
    }
  )
}

async function initializeShopPage(lane, context, page, proxy, recovery = false, signal = workerStopController.signal) {
  const previousResourcesAllowed = Boolean(lane?.challengeResourcesAllowed)
  // The first navigation is the moment ESA loads its slider image, fonts, and
  // media.  Those resources must be available before the challenge manager can
  // inspect the page; otherwise the shell can be classified as unsupported.
  if (challengeAutoSolveEnabled && lane) lane.challengeResourcesAllowed = true
  try {
    throwIfAborted(signal)
    let response
    try {
      response = await page.goto(shopHomeURL, { waitUntil: 'domcontentloaded', timeout: 20_000 })
    } catch (error) {
      if (error.name !== 'TimeoutError' && !isExpectedNavigationAbort(error)) throw error
      console.log(`${new Date().toISOString()} shop session navigation was interrupted; checking the loaded document origin`)
      if (error.name !== 'TimeoutError' && typeof page.waitForLoadState === 'function') {
        await page.waitForLoadState('domcontentloaded', {
          timeout: Math.min(5_000, browserProtocolTimeoutMilliseconds),
        }).catch(() => {})
      }
    }
    const snapshot = await collectChallengeSnapshot(page, response)
    const reachedShopOrigin = snapshot.frames.some((frame) => {
      try {
        return new URL(frame?.url).origin === shopOrigin
      } catch {
        return false
      }
    })
    if (!reachedShopOrigin) {
      throw new ShopSyncError('network', `shop session navigation did not reach ${shopOrigin}`)
    }
    throwIfAborted(signal)
    if (snapshot.isChallenge && !challengeCleared(snapshot) && !challengeContentCleared(snapshot)) {
      await solveChallengeForContext(lane, context, page, proxy, signal)
      if (recovery) console.log(`${new Date().toISOString()} lane ${lane.index + 1} shop session recovered after verification`)
    } else {
      publishChallengeState(lane, { state: 'clear' })
    }
  } catch (error) {
    if (isChallengeError(error) || error?.kind) throw error
    throw shopRequestError('/', error)
  } finally {
    if (lane) lane.challengeResourcesAllowed = previousResourcesAllowed
  }
}

function startJobHeartbeat(lane, job, controller) {
  return createJobHeartbeat({
    intervalMilliseconds: heartbeatMilliseconds,
    send: async () => {
      await backend('/api/v1/public/account-import/products/sync-heartbeat', {
        method: 'POST',
        body: JSON.stringify({ shop_id: job.shop_id, attempt_id: job.attempt_id }),
        signal: controller.signal,
      })
      publishLaneStatus(lane, { last_heartbeat_at: new Date().toISOString() })
    },
    onLeaseLost: async () => {
      const error = new JobLeaseLostError('product sync job lease expired')
      controller.abort(error)
      console.warn(`${new Date().toISOString()} lease lost for ${job.shop_name}; cancelling the stale job on lane ${lane.index + 1}`)
      publishLaneStatus(lane, {
        lease_lost_at: new Date().toISOString(),
        last_error: '',
        retry_at: '',
      })
    },
    onError: (error) => {
      console.error(`${new Date().toISOString()} heartbeat failed for ${job.shop_name}: ${errorMessage(error)}`)
    },
  })
}

async function reportJobFailure(job, error) {
  try {
    await backend('/api/v1/public/account-import/products/sync-failure', {
      method: 'POST',
      body: JSON.stringify({
        shop_id: job.shop_id,
        attempt_id: job.attempt_id,
        error: errorMessage(error),
      }),
    })
  } catch (reportError) {
    console.error(`${new Date().toISOString()} failed to report sync failure for ${job.shop_name}: ${errorMessage(reportError)}`)
  }
}

async function syncJob(lane, job) {
  const deadlineAt = Date.now() + maxJobMilliseconds
  const controller = new AbortController()
  const heartbeat = startJobHeartbeat(lane, job, controller)
  try {
    ensureLaneContextReady(lane)
    const snapshot = await collectAuthoritativeSnapshot({
      shopToken: job.token,
      quoteSemaphore: lane.quoteSemaphore,
      post: (path, body) => postShopAPIWithPressureRecovery(
        lane,
        job.token,
        path,
        body,
        deadlineAt,
        controller.signal
      ),
    })
    ensureJobDeadline(deadlineAt, controller.signal)
    const result = await publishJobSnapshot(job, snapshot, controller.signal)
    ensureJobDeadline(deadlineAt, controller.signal)
    await heartbeat.stop()
    console.log(`${new Date().toISOString()} synced ${job.shop_name}: ${result.accepted}/${snapshot.source_product_count} sellable products`)
    publishStatus({
      last_success_at: new Date().toISOString(),
      last_shop_id: job.shop_id,
      last_error: '',
    })
  } catch (error) {
    await heartbeat.stop()
    const finalError = syncJobFinalError(error, controller.signal)
    if (finalError?.kind !== 'lease_lost') await reportJobFailure(job, finalError)
    throw finalError
  }
}

async function publishJobSnapshot(job, snapshot, signal, publish = backend) {
  throwIfAborted(signal)
  const result = await publish('/api/v1/public/account-import/products/sync', {
    method: 'POST',
    body: JSON.stringify({
      shop_id: job.shop_id,
      attempt_id: job.attempt_id,
      ...snapshot,
    }),
    signal,
  })
  // A lease can be lost while the publication request is in flight. Never
  // report that stale attempt as a worker success even if a test double or a
  // delayed backend response resolves after cancellation.
  throwIfAborted(signal)
  return result
}

function syncJobFinalError(error, signal) {
  // A recovery can be cancelled after it has closed the old context. Preserve
  // its restart marker even though the job signal also carries lease_lost;
  // otherwise runLane would keep polling with lane.page === null.
  if (error?.restartLane || error?.restartBrowser) return error
  if (signal?.aborted) {
    return signal.reason instanceof Error
      ? signal.reason
      : new Error(String(signal.reason || 'product sync job aborted'))
  }
  return error?.status === 409
    ? new JobLeaseLostError('product sync job lease expired before publication')
    : error
}

function browserContextOptions(proxy) {
  return {
    // Camoufox's patched Firefox protocol does not accept Chromium's
    // `isMobile` viewport field.  A null viewport lets the virtual display
    // provide the real window dimensions instead.
    viewport: browserEngine === 'camoufox' ? null : { width: 1024, height: 768 },
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    colorScheme: 'light',
    extraHTTPHeaders: { 'Accept-Language': 'zh-CN,zh;q=0.9,en;q=0.8' },
    ...(proxy ? { proxy } : {}),
  }
}

function camoufoxEnvironment(lane, proxy) {
  const environment = { ...process.env }
  if (environment.CAMOU_CONFIG_1) return environment

  // The stock Camoufox binary advertises a `Camoufox/<version>` UA when no
  // fingerprint config is supplied.  Keep the browser's native Firefox
  // fingerprinting patches, but use a normal Firefox UA so the site cannot
  // reject the browser solely on the product token.  Deployments can provide
  // a complete CAMOU_CONFIG_* set when they need a pinned fingerprint.
  const firefoxVersion = String(process.env.CAMOUFOX_FIREFOX_VERSION || '152.0').trim()
  const laneSeed = browserFingerprintSeed(lane?.index, proxy)
  environment.CAMOU_CONFIG_1 = JSON.stringify({
    'navigator.userAgent': process.env.CAMOUFOX_USER_AGENT
      || `Mozilla/5.0 (X11; Linux x86_64; rv:${firefoxVersion}) Gecko/20100101 Firefox/${firefoxVersion}`,
    'navigator.platform': 'Linux x86_64',
    'navigator.oscpu': 'Linux x86_64',
    'fonts:spacing_seed': Number.parseInt(laneSeed, 16) || 1,
    'audio:seed': Number.parseInt(laneSeed.slice(0, 6), 16) || 1,
    'canvas:seed': Number.parseInt(laneSeed.slice(2, 8), 16) || 1,
  })
  return environment
}

function browserLaunchArguments() {
  if (browserEngine === 'camoufox') {
    return [
      '--no-remote',
      '--lang=zh-CN',
    ]
  }
  return [
    '--no-sandbox',
    '--disable-dev-shm-usage',
    '--disable-gpu',
    '--disable-background-timer-throttling',
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
    '--disable-blink-features=AutomationControlled',
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-sync',
    '--lang=zh-CN',
    '--window-size=1280,900',
  ]
}

function profileIdentity(proxy) {
  return JSON.stringify({
    server: String(proxy?.server || ''),
    username: String(proxy?.username || ''),
    password: String(proxy?.password || ''),
  })
}

function laneProfilePath(lane, proxy) {
  const digest = crypto.createHash('sha256')
    // Keep Firefox and Chromium profiles separate.  Reusing a Chromium
    // profile after switching engines makes Firefox fail before navigation.
    .update(`${browserEngine}\0${lane.index}\0${profileIdentity(proxy)}`)
    .digest('hex')
  return path.join(browserProfileDirectory, `lane-${lane.index + 1}-${digest}`)
}

function ensureBrowserProfileDirectory() {
  fs.mkdirSync(browserProfileDirectory, { recursive: true, mode: 0o700 })
  try { fs.chmodSync(browserProfileDirectory, 0o700) } catch (error) {
    if (!['EPERM', 'EACCES'].includes(error?.code)) throw error
  }
}

function browserProfileIsInUse(profileDirectory) {
  // Camoufox/Firefox uses a regular `lock` or `.parentlock` file rather than
  // Chromium's Singleton symlinks.  The process scan is the authoritative
  // check for both engines when the browser exposes `-profile` in /proc.
  const profileProcess = browserProfileProcessInfo(path.resolve(profileDirectory))
  if (profileProcess.pid > 0) return true

  const lockPath = path.join(profileDirectory, 'SingletonLock')
  let lockTarget
  try {
    lockTarget = fs.readlinkSync(lockPath)
  } catch (error) {
    if (error?.code !== 'ENOENT' && error?.code !== 'EINVAL') return true
  }
  if (lockTarget !== undefined) {
    const match = String(lockTarget).match(/-(\d+)$/)
    if (!match) return true
    const pid = match[1]
    try {
      const commandLine = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').replaceAll('\0', ' ')
      if (commandLine.includes(profileDirectory)) return true
    } catch (error) {
      if (error?.code !== 'ENOENT') return true
    }
  }

  for (const lockName of ['lock', '.parentlock']) {
    let present = false
    try {
      fs.statSync(path.join(profileDirectory, lockName))
      present = true
    } catch (error) {
      if (error?.code !== 'ENOENT') return true
    }
    if (!present) continue

    // Linux production workers can inspect /proc.  On platforms without a
    // process table, retaining an unknown Firefox lock is safer than deleting
    // a lock owned by a live browser.
    if (process.platform !== 'linux' || !profileProcess.available) return true
  }
  return false
}

function clearStaleBrowserProfileLocks(profileDirectory) {
  if (browserProfileIsInUse(profileDirectory)) {
    throw new Error(`browser profile is already in use: ${redactURLCredentials(profileDirectory)}`)
  }
  for (const lockName of ['SingletonLock', 'SingletonSocket', 'SingletonCookie', 'lock', '.parentlock']) {
    try {
      fs.unlinkSync(path.join(profileDirectory, lockName))
    } catch (error) {
      if (error?.code !== 'ENOENT') throw error
    }
  }
}

async function restorePersistentStorage(context, page, storageState) {
  if (!storageState || typeof storageState !== 'object') return false
  const cookies = Array.isArray(storageState.cookies) ? storageState.cookies : []
  if (cookies.length) await context.addCookies(cookies)
  const origins = Array.isArray(storageState.origins) ? storageState.origins : []
  for (const originState of origins) {
    if (!originState?.origin || !Array.isArray(originState.localStorage)) continue
    try {
      await page.goto(originState.origin, {
        waitUntil: 'domcontentloaded',
        timeout: Math.min(15_000, browserProtocolTimeoutMilliseconds),
      })
      await page.evaluate((entries) => {
        for (const entry of entries) {
          if (entry && typeof entry.name === 'string') localStorage.setItem(entry.name, String(entry.value ?? ''))
        }
      }, originState.localStorage)
    } catch {
      // A stale origin must not prevent the lane from opening; the challenge
      // manager will rebuild the state after the first successful request.
    }
  }
  return cookies.length > 0 || origins.length > 0
}

async function prepareLaneContext(lane, context, proxy, recovery = false, signal = workerStopController.signal) {
  context.setDefaultTimeout(browserProtocolTimeoutMilliseconds)
  // Camoufox applies these values inside its patched Firefox engine.  Adding
  // JavaScript property overrides on top would reintroduce detectable
  // descriptors, so only Chromium receives the legacy compatibility shim.
  if (browserEngine !== 'camoufox') {
    await context.addInitScript(() => {
      Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => undefined, configurable: true })
      Object.defineProperty(Navigator.prototype, 'languages', { get: () => ['zh-CN', 'zh', 'en'], configurable: true })
      if (!window.chrome) Object.defineProperty(window, 'chrome', { value: { runtime: {} } })
    })
  }

  await context.addInitScript(() => {
    // Alibaba ESA's generated verification page passes button: "#button" to
    // initAliyunCaptcha even though the page does not render that button.  The
    // SDK refuses to initialise when the selector is missing, leaving only the
    // static captcha-element and causing the worker to classify the challenge
    // as unsupported.  Provide the selector before the page's load handler
    // appends AliyunCaptcha.js.  Keep the element off-screen so it cannot alter
    // the verification UI; it remains a real DOM element for the SDK's lookup.
    const ensureAliyunCaptchaButton = () => {
      if (document.getElementById('button')) return
      const button = document.createElement('button')
      button.id = 'button'
      button.type = 'button'
      button.tabIndex = -1
      button.setAttribute('aria-hidden', 'true')
      button.style.cssText = 'position:fixed;left:-10000px;top:-10000px;width:1px;height:1px;opacity:0;pointer-events:none'
      const parent = document.body || document.documentElement
      if (parent) parent.appendChild(button)
    }
    // Run immediately as well as at DOMContentLoaded. ESA's generated page can
    // load and invoke initAliyunCaptcha before DOMContentLoaded; waiting for
    // that event leaves the SDK with an invalid #button selector and no
    // discoverable slider, which is then incorrectly reported as unsupported.
    ensureAliyunCaptchaButton()
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', ensureAliyunCaptchaButton, { once: true })
    }
  })
  await context.route('**/*', async (route) => {
    if (shouldBlockResource(route.request().resourceType(), lane.challengeResourcesAllowed)) await route.abort()
    else await route.continue()
  })
  const existingPages = context.pages()
  for (const page of existingPages.slice(1)) {
    await page.close()
  }
  const page = existingPages[0] || await context.newPage()
  await initializeShopPage(lane, context, page, proxy, recovery, signal)
  return page
}

function createLane(index, proxyPool) {
  return {
    index,
    context: null,
    page: null,
    browserProcessID: 0,
    proxyPool,
    proxyIndex: 0,
    proxy: proxyPool[0],
    pressureState: { failureCount: 0 },
    challengeState: { failureCount: 0 },
    challengeResourcesAllowed: false,
    quoteSemaphore: new Semaphore(1),
    requestLimiter: new TokenBucket(requestRatePerLane, 1),
  }
}

async function closeLaneContext(lane) {
  const context = lane.context
  lane.context = null
  lane.page = null
  lane.browserProcessID = 0
  lane.challengeResourcesAllowed = false
  if (activeBrowserContexts[lane.index] === context) activeBrowserContexts[lane.index] = null
  await context?.close().catch(() => {})
  publishStatus({})
}

function observeLaneContext(lane, context) {
  if (!context || typeof context.once !== 'function') return
  context.once('close', () => {
    let wasCurrent = false
    if (lane.context === context) {
      lane.context = null
      lane.page = null
      lane.browserProcessID = 0
      lane.challengeResourcesAllowed = false
      wasCurrent = true
    }
    if (activeBrowserContexts[lane.index] === context) {
      activeBrowserContexts[lane.index] = null
      wasCurrent = true
    }
    if (!wasCurrent) return
    publishLaneStatus(lane, {
      state: stopping ? 'stopping' : 'restarting',
      last_error_at: stopping ? laneStatuses[lane.index]?.last_error_at : new Date().toISOString(),
      last_error: stopping ? laneStatuses[lane.index]?.last_error || '' : 'browser context closed unexpectedly',
    })
  })
}

async function replaceLaneContext(lane, proxyIndex, recovery = false, signal = workerStopController.signal) {
  throwIfAborted(signal)
  await closeLaneContext(lane)
  if (stopping) throw new Error('product sync worker is stopping')
  if (!activeBrowser?.isConnected()) throw new Error('product sync browser is not connected')

  const proxy = lane.proxyPool[proxyIndex]
  let context
  try {
    ensureBrowserProfileDirectory()
    const restored = challengeManager.loadSession(proxy)
    publishLaneStatus(lane, {
      challenge_provider: restored?.provider || '',
      challenge_state: restored ? 'restored' : challengeAutoSolveEnabled ? 'idle' : 'disabled',
      challenge_attempt: 0,
      challenge_started_at: '',
      challenge_solved_at: '',
      session_restored: Boolean(restored),
    })
    const profileDirectory = laneProfilePath(lane, proxy)
    fs.mkdirSync(profileDirectory, { recursive: true, mode: 0o700 })
    try { fs.chmodSync(profileDirectory, 0o700) } catch (error) {
      if (!['EPERM', 'EACCES'].includes(error?.code)) throw error
    }
    // A crashed browser can leave Chromium Singleton* or Firefox lock files in
    // the persistent profile.  Only remove them when no process still
    // references this profile, so a genuinely concurrent owner is never
    // corrupted.
    clearStaleBrowserProfileLocks(profileDirectory)
    const launchOptions = {
      ...browserContextOptions(proxy),
      executablePath: browserExecutablePath,
      headless: false,
      timeout: browserProtocolTimeoutMilliseconds,
      args: browserLaunchArguments(),
    }
    if (browserEngine === 'camoufox') {
      launchOptions.env = camoufoxEnvironment(lane, proxy)
      launchOptions.firefoxUserPrefs = camoufoxFirefoxUserPrefs()
    } else {
      launchOptions.ignoreDefaultArgs = ['--enable-automation']
    }
    context = await browserType.launchPersistentContext(profileDirectory, launchOptions)
    observeLaneContext(lane, context)
    lane.browserProcessID = browserEngine === 'camoufox'
      ? browserProcessIDForProfile(profileDirectory)
      : 0
    if (browserEngine === 'camoufox') {
      try {
        await resizeNativeBrowserWindow(
          lane.browserProcessID,
          camoufoxWindow.width,
          camoufoxWindow.height
        )
      } catch (error) {
        console.warn(`${new Date().toISOString()} lane ${lane.index + 1} could not resize its Camoufox window: ${errorMessage(error)}`)
      }
    }
    const restoredPage = context.pages()[0] || await context.newPage()
    await restorePersistentStorage(context, restoredPage, restored?.storageState)
    const page = await prepareLaneContext(lane, context, proxy, recovery, signal)
    if (typeof page?.isClosed === 'function' && page.isClosed()) {
      throw new Error('product sync browser page closed during lane initialization')
    }
    throwIfAborted(signal)
    if (stopping) throw new Error('product sync worker is stopping')
    if (!activeBrowser?.isConnected()) throw new Error('product sync browser is not connected')
    lane.context = context
    lane.page = page
    lane.proxyIndex = proxyIndex
    lane.proxy = proxy
    activeBrowserContexts[lane.index] = context
    publishStatus({})
  } catch (error) {
    lane.browserProcessID = 0
    await context?.close().catch(() => {})
    if (activeBrowserContexts[lane.index] === context) activeBrowserContexts[lane.index] = null
    throw error
  }
}

async function initializeLane(lane, recovery = false) {
  const result = await initializeProxyPool(
    lane.proxyPool,
    lane.proxyIndex,
    async (_proxy, proxyIndex) => {
      await replaceLaneContext(lane, proxyIndex, recovery)
    },
    {
      attemptsPerProxy: 2,
      retryMilliseconds: 2_000,
      shouldRetryProxy: ({ error }) => !isChallengeError(error),
      onFailure: async ({ error, proxy, proxyIndex, attempt }) => {
        console.error(`${new Date().toISOString()} lane ${lane.index + 1} initialization failed on ${proxy?.server || 'direct'} (attempt ${attempt}): ${errorMessage(error)}`)
        publishLaneStatus(lane, {
          state: 'starting',
          proxy_server: proxy?.server || '',
          proxy_pool_position: proxyIndex + 1,
          initialization_attempt: attempt,
          last_error_at: new Date().toISOString(),
          last_error: errorMessage(error),
        })
        if (stopping || !activeBrowser?.isConnected()) throw error
      },
    }
  )
  return result.proxyIndex
}

function laneRecoveryCancellationFailure(lane, error, signal) {
  if (!signal?.aborted) return error
  const reason = signal.reason instanceof Error
    ? signal.reason
    : new Error(String(signal.reason || 'lane recovery aborted'))
  const pageIsOpen = lane?.context && lane?.page
    && (typeof lane.page.isClosed !== 'function' || !lane.page.isClosed())
  if (pageIsOpen) return reason

  // A fallback rotation closes the old context before opening the new one.
  // If cancellation lands inside that interval, the lane still needs an
  // independent rebuild, but the lease-loss classification must be retained.
  const restartError = new Error(reason.message, { cause: reason })
  restartError.name = reason.name
  if (reason.kind) restartError.kind = reason.kind
  restartError.restartLane = true
  return restartError
}

function pressureRecoveryFailure(lane, error, signal) {
  if (signal?.aborted) return laneRecoveryCancellationFailure(lane, error, signal)
  const restartError = error instanceof Error ? error : new Error(String(error))
  restartError.restartLane = true
  return restartError
}

async function recoverLaneAfterPressure(
  lane,
  failureCount = lane.pressureState.failureCount,
  signal = workerStopController.signal
) {
  try {
    throwIfAborted(signal)
    const failures = Math.max(1, Number(failureCount) || 1)
    // Keep the persistent browser/profile on the first pressure failure. A
    // fresh navigation clears a half-read response and avoids needless
    // Camoufox process churn; rotate to the lane fallback only after the
    // current exit has failed twice.
    if (pressureRecoveryMode(failures, lane.proxyPool.length) === 'reload_current_exit') {
      await initializeShopPage(lane, lane.context, lane.page, lane.proxy, true, signal)
      throwIfAborted(signal)
      publishLaneStatus(lane, {
        pressure_recovery: 'reload_current_exit',
        pressure_recovery_at: new Date().toISOString(),
      })
      console.log(`${new Date().toISOString()} lane ${lane.index + 1} reloaded the active shop page after pressure failure ${failures}`)
      return
    }

    const previousProxy = lane.proxy
    const nextProxyIndex = (lane.proxyIndex + 1) % lane.proxyPool.length
    const nextProxy = lane.proxyPool[nextProxyIndex]
    await replaceLaneContext(lane, nextProxyIndex, true, signal)
    throwIfAborted(signal)
    console.log(`${new Date().toISOString()} lane ${lane.index + 1} rotated product sync proxy after pressure backoff: ${previousProxy?.server || 'direct'} -> ${nextProxy?.server || 'direct'}`)
    publishLaneStatus(lane, {
      proxy_server: nextProxy?.server || '',
      proxy_pool_position: nextProxyIndex + 1,
      proxy_rotated_at: new Date().toISOString(),
      pressure_recovery: 'switch_fallback',
      pressure_recovery_at: new Date().toISOString(),
    })
  } catch (error) {
    throw pressureRecoveryFailure(lane, error, signal)
  }
}

async function runLane(lane) {
  while (activeBrowser?.isConnected() && !stopping) {
    try {
      ensureLaneContextReady(lane)
      const data = await backend('/api/v1/public/account-import/products/sync-job?limit=1')
      const job = Array.isArray(data.jobs) && data.jobs.length > 0 ? data.jobs[0] : data.job
      publishLaneStatus(lane, {
        state: job ? 'syncing' : 'idle',
        last_poll_at: new Date().toISOString(),
        active_shop_id: job?.shop_id || '',
        active_shop_name: job?.shop_name || '',
        ...(job ? {} : { pressure_failure_count: 0 }),
      })
      if (!job) {
        await sleep(idlePollMilliseconds)
        continue
      }

      try {
        await syncJob(lane, job)
        lane.pressureState.failureCount = 0
        publishLaneStatus(lane, {
          state: 'idle',
          active_shop_id: '',
          active_shop_name: '',
          last_error: '',
          retry_at: '',
          pressure_failure_count: 0,
        })
      } catch (error) {
        if (error?.restartLane || error?.restartBrowser) throw error
        if (error?.kind === 'lease_lost') {
          console.warn(`${new Date().toISOString()} discarded expired sync result for ${job.shop_name}`)
          lane.pressureState.failureCount = 0
          publishLaneStatus(lane, {
            state: 'idle',
            active_shop_id: '',
            active_shop_name: '',
            last_error: '',
            retry_at: '',
            pressure_failure_count: 0,
          })
        } else if (error?.pressureDeadlineExceeded) {
          // withPressureRecovery has already consumed every retry/backoff that
          // fit inside this job's deadline. Do not apply the same pressure tier
          // again at lane level or rotate the proxy a second time. The failed
          // attempt has been reported by syncJob; let this lane poll normally
          // and start the next job with a fresh consecutive-pressure counter.
          const failureCount = Math.max(0, Number(error.pressureFailureCount) || 0)
          console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync exhausted its job deadline under upstream pressure: ${errorMessage(error)}`)
          lane.pressureState.failureCount = 0
          publishLaneStatus(lane, {
            state: 'error',
            active_shop_id: '',
            active_shop_name: '',
            pressure_failure_count: failureCount,
            retry_at: '',
            last_error_at: new Date().toISOString(),
            last_error: errorMessage(error),
          })
        } else if (!isPressureError(error)) {
          console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync failed for ${job.shop_name}: ${errorMessage(error)}`)
          lane.pressureState.failureCount = 0
          publishLaneStatus(lane, {
            state: 'error',
            active_shop_id: '',
            active_shop_name: '',
            pressure_failure_count: 0,
            last_error_at: new Date().toISOString(),
            last_error: errorMessage(error),
          })
        } else {
          console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync failed for ${job.shop_name}: ${errorMessage(error)}`)
          lane.pressureState.failureCount += 1
          const waitMilliseconds = pressureBackoffMilliseconds(lane.pressureState.failureCount)
          publishLaneStatus(lane, {
            state: 'blocked',
            active_shop_id: '',
            active_shop_name: '',
            pressure_failure_count: lane.pressureState.failureCount,
            retry_at: new Date(Date.now() + waitMilliseconds).toISOString(),
            last_error_at: new Date().toISOString(),
            last_error: errorMessage(error),
          })
          console.log(`${new Date().toISOString()} lane ${lane.index + 1} applying product sync pressure backoff for ${Math.round(waitMilliseconds / 1000)} seconds`)
          await waitWithStatusRefresh(waitMilliseconds, {
            sleep,
            shouldContinue: () => !stopping,
            onRefresh: () => publishLaneStatus(lane, {}),
          })
          if (!stopping) {
            try {
              await recoverLaneAfterPressure(lane, lane.pressureState.failureCount, workerStopController.signal)
            } catch (recoveryError) {
              console.error(`${new Date().toISOString()} lane ${lane.index + 1} pressure recovery failed: ${errorMessage(recoveryError)}`)
              if (recoveryError?.restartLane || recoveryError?.restartBrowser) throw recoveryError
            }
          }
          lane.pressureState.failureCount = 0
          publishLaneStatus(lane, {
            state: 'idle',
            retry_at: '',
            pressure_failure_count: 0,
          })
        }
      }
      await sleep(activePollMilliseconds)
    } catch (error) {
      if (error?.restartLane || error?.restartBrowser) throw error
      console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync polling failed: ${errorMessage(error)}`)
      lane.pressureState.failureCount = 0
      publishLaneStatus(lane, {
        state: 'error',
        active_shop_id: '',
        active_shop_name: '',
        pressure_failure_count: 0,
        last_error_at: new Date().toISOString(),
        last_error: errorMessage(error),
      })
      await sleep(idlePollMilliseconds)
    }
  }
}

async function runLaneLifecycle(lane, browser) {
  if (lane.index > 0) await sleep(lane.index * 1_100)
  let initializationFailures = 0
  try {
    while (activeBrowser === browser && browser.isConnected() && !stopping) {
      try {
        await initializeLane(lane, initializationFailures > 0 || lane.challengeState.failureCount > 0)
        initializationFailures = 0
        lane.pressureState.failureCount = 0
        lane.challengeState.failureCount = 0
        publishLaneStatus(lane, {
          state: 'idle',
          proxy_server: lane.proxy?.server || '',
          proxy_pool_position: lane.proxyIndex + 1,
          proxy_pool_size: lane.proxyPool.length,
          active_shop_id: '',
          active_shop_name: '',
          last_error: '',
          retry_at: '',
          challenge_failure_count: 0,
        })
        await runLane(lane)
      } catch (error) {
        if (stopping || activeBrowser !== browser || !browser.isConnected()) break
        await closeLaneContext(lane)
        const challengeFailure = isChallengeError(error)
        if (challengeFailure) lane.challengeState.failureCount += 1
        else initializationFailures += 1
        const waitMilliseconds = error?.kind === 'lease_lost'
          ? 1_000
          : challengeFailure
            ? challengeBackoffMilliseconds(lane.challengeState.failureCount, error.challengeState)
            : isPressureError(error)
              ? pressureBackoffMilliseconds(initializationFailures)
              : Math.min(60_000, 5_000 * (2 ** Math.min(initializationFailures - 1, 4)))
        console.error(`${new Date().toISOString()} lane ${lane.index + 1} restarting independently in ${Math.round(waitMilliseconds / 1000)} seconds: ${errorMessage(error)}`)
        publishLaneStatus(lane, {
          state: challengeFailure || isPressureError(error) ? 'blocked' : 'restarting',
          active_shop_id: '',
          active_shop_name: '',
          retry_at: new Date(Date.now() + waitMilliseconds).toISOString(),
          last_error_at: new Date().toISOString(),
          last_error: error?.kind === 'lease_lost' ? '' : errorMessage(error),
          ...(challengeFailure ? {
            challenge_state: error.challengeState || 'failed',
            challenge_failure_count: lane.challengeState.failureCount,
          } : {}),
        })
        await waitWithStatusRefresh(waitMilliseconds, {
          sleep,
          shouldContinue: () => !stopping,
          onRefresh: () => publishLaneStatus(lane, {}),
        })
      }
    }
  } finally {
    await closeLaneContext(lane)
  }
}

async function runBrowser() {
  let contexts = []
  const lanes = []
  let browser
  try {
    // Persistent contexts are intentionally used instead of incognito
    // contexts. ESA fingerprints a fresh incognito profile and returns F001
    // even when the drag itself is valid. The small connection sentinel keeps
    // the existing lane supervisor semantics while each lane owns its browser
    // process/profile.
    browser = {
      isConnected: () => !stopping,
      close: async () => {},
    }
    contexts = Array.from({ length: syncConcurrency }, () => null)
    activeBrowser = browser
    activeBrowserContexts = contexts
    for (let index = 0; index < syncConcurrency; index += 1) {
      const lane = createLane(index, laneProxyPools[index])
      lanes.push(lane)
      publishLaneStatus(lane, {
        state: 'starting',
        proxy_server: laneProxies[index]?.server || '',
        proxy_pool_position: 1,
        proxy_pool_size: laneProxyPools[index].length,
      })
    }
    publishStatus({
      browser_engine: browserEngine,
      sync_concurrency: syncConcurrency,
      quote_concurrency_per_lane: 1,
      request_rate_per_second_per_lane: requestRatePerLane,
      request_rate_per_second_global: globalRequestRate,
      browser_started_at: new Date().toISOString(),
    })
    await Promise.all(lanes.map((lane) => runLaneLifecycle(lane, browser)))
    if (!stopping) throw new Error('product sync browser disconnected')
  } finally {
    if (activeBrowser === browser) activeBrowser = undefined
    if (activeBrowserContexts === contexts) activeBrowserContexts = []
    await Promise.all(contexts.filter(Boolean).map((context) => context.close().catch(() => {})))
  }
}

async function main() {
  if (!backendToken) throw new Error('PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN is required')
  publishStatus({ state: 'starting' })
  console.log(`${new Date().toISOString()} product sync worker starting; proxy_lanes=${configuredProxies.length}; proxy_endpoints=${configuredProxyCount}; challenge_auto_solve=${challengeAutoSolveEnabled}`)
  let browserPressureFailures = 0
  while (!stopping) {
    try {
      await runBrowser()
      browserPressureFailures = 0
    } catch (error) {
      console.error(`${new Date().toISOString()} browser restart: ${errorMessage(error)}`)
      publishStatus({ state: 'error', last_error: errorMessage(error) })
      if (isPressureError(error)) browserPressureFailures += 1
      else browserPressureFailures = 0
      const restartDelay = isPressureError(error)
        ? pressureBackoffMilliseconds(browserPressureFailures)
        : 15_000
      await sleep(restartDelay)
    }
  }
}

async function shutdown(signal) {
  if (stopping) return
  stopping = true
  workerStopController.abort(new Error(`product sync worker received ${signal}`))
  publishStatus({ state: 'stopping', stop_signal: signal })
  try {
    await Promise.all(activeBrowserContexts.filter(Boolean).map((context) => context.close().catch(() => {})))
    await activeBrowser?.close().catch(() => {})
  } catch (error) {
    console.error(`${new Date().toISOString()} browser shutdown failed: ${errorMessage(error)}`)
  }
  process.exit(0)
}

if (require.main === module) {
  process.once('SIGINT', () => void shutdown('SIGINT'))
  process.once('SIGTERM', () => void shutdown('SIGTERM'))

  main().catch((error) => {
    console.error(`${new Date().toISOString()} worker stopped: ${errorMessage(error)}`)
    publishStatus({ state: 'stopped', last_error: errorMessage(error) })
    process.exitCode = 1
  })
}

module.exports = {
  backend,
  challengeStatusValues,
  backendRequestSignal,
  browserProfileIsInUse,
  clearStaleBrowserProfileLocks,
  evaluateShopRequest,
  evaluateShopRequestInBrowser,
  laneContextIsReady,
  laneRecoveryCancellationFailure,
  publishJobSnapshot,
  pressureRecoveryFailure,
  recoverLaneAfterPressure,
  syncJobFinalError,
}
