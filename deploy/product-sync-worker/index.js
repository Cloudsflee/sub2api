const { chromium } = require('playwright-core')
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const { collectAuthoritativeSnapshot } = require('./catalog-sync')
const {
  ChallengeManager,
  challengeBackoffMilliseconds,
  challengeCleared,
  collectChallengeSnapshot,
  isChallengeError,
  recoverChallengeAcrossProxyPool,
  retryFailedChallengeOperation,
  shouldBlockResource,
} = require('./challenge-manager')
const {
  JobLeaseLostError,
  Semaphore,
  TokenBucket,
  browserResourceCounts,
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
const chromePath = process.env.CHROME_PATH || '/usr/bin/google-chrome-stable'
const statusFile = process.env.STATUS_FILE || '/data/status.json'
const challengeSessionDirectory = process.env.PRODUCT_SYNC_CHALLENGE_SESSION_DIR || '/data/challenge-sessions'
const browserProfileDirectory = process.env.PRODUCT_SYNC_BROWSER_PROFILE_DIR || '/data/browser-profiles'
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

function publishStatus(values) {
  const resources = browserResourceCounts(activeBrowserContexts)
  const lanes = laneStatuses.map((status, index) => ({
    ...status,
    context_ready: Boolean(activeBrowserContexts[index]),
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
    fs.writeFileSync(temporary, `${JSON.stringify(workerStatus, null, 2)}\n`, { mode: 0o600 })
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
    Boolean(activeBrowserContexts[index]) && readyStates.has(status.state)
  )).length
  const degradedLaneCount = laneStatuses.filter((status, index) => (
    !activeBrowserContexts[index] && ['blocked', 'error', 'restarting'].includes(status.state)
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

async function backend(path, options = {}) {
  const response = await fetch(`${backendURL}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${backendToken}`,
      ...(options.headers || {}),
    },
    signal: options.signal || AbortSignal.timeout(backendRequestTimeoutMilliseconds),
  })
  if (!response.ok) throw new BackendHTTPError(path, response.status)
  const payload = await response.json()
  if (payload.code !== 0) throw new Error(payload.message || `backend ${path} failed`)
  return payload.data
}

function ensureJobDeadline(deadlineAt, signal) {
  throwIfAborted(signal)
  if (Date.now() >= deadlineAt) throw new Error('product sync job exceeded the 30 minute limit')
}

async function postShopAPI(lane, shopToken, path, body, deadlineAt, signal) {
  ensureJobDeadline(deadlineAt, signal)
  await takeRequestTokens(lane.requestLimiter, globalRequestLimiter, signal)
  ensureJobDeadline(deadlineAt, signal)
  let result
  try {
    result = await lane.page.evaluate(async ({ requestPath, requestBody, requestTimeoutMilliseconds, visitorID }) => {
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), requestTimeoutMilliseconds)
      try {
        const response = await fetch(requestPath, {
          method: 'POST',
          credentials: 'include',
          headers: {
            'Content-Type': 'application/json',
            Accept: 'application/json',
            Visitorid: visitorID,
          },
          body: JSON.stringify(requestBody),
          signal: controller.signal,
        })
        const contentType = response.headers.get('content-type') || ''
        const text = await response.text()
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
          text: text.slice(0, 2_000),
          responseError: response.headers.get('x-tengine-error') || '',
          retryAfter: response.headers.get('retry-after') || '',
        }
      } finally {
        clearTimeout(timer)
      }
    }, {
      requestPath: path,
      requestBody: body,
      requestTimeoutMilliseconds: Math.min(shopRequestTimeoutMilliseconds, Math.max(1, deadlineAt - Date.now())),
      visitorID: `sub2api${shopToken.replace(/[^a-zA-Z0-9]/g, '').slice(0, 24)}`,
    })
  } catch (error) {
    throwIfAborted(signal)
    throw shopRequestError(path, error)
  }
  throwIfAborted(signal)
  return parseShopHTTPResponse(result)
}

function publishChallengeState(lane, event) {
  const values = { challenge_state: event.state }
  if (event.provider !== undefined) values.challenge_provider = event.provider
  if (event.attempt !== undefined) values.challenge_attempt = event.attempt
  if (event.startedAt !== undefined) values.challenge_started_at = event.startedAt
  if (event.solvedAt !== undefined) values.challenge_solved_at = event.solvedAt
  publishLaneStatus(lane, values)
}

async function solveChallengeForContext(lane, context, page, proxy, signal) {
  const result = await challengeManager.solve({
    context,
    page,
    proxy,
    signal,
    onState: (event) => publishChallengeState(lane, event),
    setResourcesAllowed: (allowed) => { lane.challengeResourcesAllowed = allowed },
  })
  lane.challengeState.failureCount = 0
  return result
}

async function recoverLaneChallenge(lane, signal) {
  const previousProxyIndex = lane.proxyIndex
  const recoveredProxyIndex = await recoverChallengeAcrossProxyPool({
    poolSize: lane.proxyPool.length,
    currentIndex: previousProxyIndex,
    solveCurrent: () => solveChallengeForContext(lane, lane.context, lane.page, lane.proxy, signal),
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
}

async function postShopAPIWithChallengeRecovery(lane, shopToken, path, body, deadlineAt, signal) {
  return retryFailedChallengeOperation(
    () => postShopAPI(lane, shopToken, path, body, deadlineAt, signal),
    async () => {
      ensureJobDeadline(deadlineAt, signal)
      await recoverLaneChallenge(lane, signal)
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
      recover: async () => {
        if (stopping) throw new Error('product sync worker is stopping')
        await recoverLaneAfterPressure(lane)
        publishLaneStatus(lane, {
          state: 'syncing',
          retry_at: '',
        })
      },
    }
  )
}

async function initializeShopPage(lane, context, page, proxy, recovery = false, signal = workerStopController.signal) {
  try {
    throwIfAborted(signal)
    let response
    try {
      response = await page.goto('https://pay.ldxp.cn/', { waitUntil: 'domcontentloaded', timeout: 20_000 })
    } catch (error) {
      if (error.name !== 'TimeoutError') throw error
      console.log(`${new Date().toISOString()} shop session navigation timed out; continuing with the loaded document`)
    }
    const snapshot = await collectChallengeSnapshot(page, response)
    throwIfAborted(signal)
    if (snapshot.isChallenge && !challengeCleared(snapshot)) {
      await solveChallengeForContext(lane, context, page, proxy, signal)
      if (recovery) console.log(`${new Date().toISOString()} lane ${lane.index + 1} shop session recovered after verification`)
    } else {
      publishChallengeState(lane, { state: 'clear' })
    }
  } catch (error) {
    if (isChallengeError(error) || error?.kind) throw error
    throw shopRequestError('/', error)
  }
}

function startJobHeartbeat(lane, job, controller) {
  return createJobHeartbeat({
    intervalMilliseconds: heartbeatMilliseconds,
    send: async () => {
      await backend('/api/v1/public/account-import/products/sync-heartbeat', {
        method: 'POST',
        body: JSON.stringify({ shop_id: job.shop_id, attempt_id: job.attempt_id }),
      })
      publishLaneStatus(lane, { last_heartbeat_at: new Date().toISOString() })
    },
    onLeaseLost: async () => {
      const error = new JobLeaseLostError('product sync job lease expired', { restartLane: true })
      controller.abort(error)
      console.warn(`${new Date().toISOString()} lease lost for ${job.shop_name}; cancelling lane ${lane.index + 1}`)
      publishLaneStatus(lane, {
        state: 'restarting',
        lease_lost_at: new Date().toISOString(),
        last_error: '',
        retry_at: '',
      })
      await closeLaneContext(lane)
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
    await heartbeat.stop()
    ensureJobDeadline(deadlineAt, controller.signal)
    const result = await backend('/api/v1/public/account-import/products/sync', {
      method: 'POST',
      body: JSON.stringify({
        shop_id: job.shop_id,
        attempt_id: job.attempt_id,
        ...snapshot,
      }),
    })
    console.log(`${new Date().toISOString()} synced ${job.shop_name}: ${result.accepted}/${snapshot.source_product_count} sellable products`)
    publishStatus({
      last_success_at: new Date().toISOString(),
      last_shop_id: job.shop_id,
      last_error: '',
    })
  } catch (error) {
    await heartbeat.stop()
    const finalError = controller.signal.aborted
      ? controller.signal.reason
      : error?.status === 409
        ? new JobLeaseLostError('product sync job lease expired before publication')
        : error
    if (finalError?.kind !== 'lease_lost') await reportJobFailure(job, finalError)
    throw finalError
  }
}

function browserLaunchOptions() {
  return {
    executablePath: chromePath,
    headless: false,
    timeout: browserProtocolTimeoutMilliseconds,
    args: [
      '--no-sandbox',
      '--disable-dev-shm-usage',
      '--disable-gpu',
      '--disable-background-timer-throttling',
      '--disable-backgrounding-occluded-windows',
      '--disable-renderer-backgrounding',
      '--disable-blink-features=AutomationControlled',
      '--no-first-run',
      '--lang=zh-CN',
      '--window-size=1024,768',
    ],
  }
}

function browserContextOptions(proxy) {
  return {
    viewport: { width: 1024, height: 768 },
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    colorScheme: 'light',
    extraHTTPHeaders: { 'Accept-Language': 'zh-CN,zh;q=0.9,en;q=0.8' },
    ...(proxy ? { proxy } : {}),
  }
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
    .update(`${lane.index}\0${profileIdentity(proxy)}`)
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
  const lockPath = path.join(profileDirectory, 'SingletonLock')
  let lockTarget
  try {
    lockTarget = fs.readlinkSync(lockPath)
  } catch (error) {
    if (error?.code === 'ENOENT' || error?.code === 'EINVAL') return false
    return true
  }
  const match = String(lockTarget).match(/-(\d+)$/)
  if (!match) return true
  const pid = match[1]
  try {
    const commandLine = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').replaceAll('\0', ' ')
    return commandLine.includes(profileDirectory)
  } catch (error) {
    return !['ENOENT', 'EACCES'].includes(error?.code)
  }
}

function clearStaleBrowserProfileLocks(profileDirectory) {
  if (browserProfileIsInUse(profileDirectory)) {
    throw new Error(`browser profile is already in use: ${redactURLCredentials(profileDirectory)}`)
  }
  for (const lockName of ['SingletonLock', 'SingletonSocket', 'SingletonCookie']) {
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
  await context.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => undefined, configurable: true })
    Object.defineProperty(Navigator.prototype, 'languages', { get: () => ['zh-CN', 'zh', 'en'], configurable: true })
    if (!window.chrome) Object.defineProperty(window, 'chrome', { value: { runtime: {} } })

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
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', ensureAliyunCaptchaButton, { once: true })
    } else {
      ensureAliyunCaptchaButton()
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
  lane.challengeResourcesAllowed = false
  if (activeBrowserContexts[lane.index] === context) activeBrowserContexts[lane.index] = null
  await context?.close().catch(() => {})
  publishStatus({})
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
    // A crashed Chrome leaves Singleton* symlinks in the persistent profile.
    // They otherwise make every subsequent launch fail with "profile appears
    // to be in use", preventing the lane from ever reaching the challenge
    // manager.  Only remove locks when no process still references this
    // profile, so a genuinely concurrent owner is never corrupted.
    clearStaleBrowserProfileLocks(profileDirectory)
    context = await chromium.launchPersistentContext(profileDirectory, {
      ...browserContextOptions(proxy),
      executablePath: chromePath,
      headless: false,
      timeout: browserProtocolTimeoutMilliseconds,
      ignoreDefaultArgs: ['--enable-automation'],
      args: [
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
      ],
    })
    const restoredPage = context.pages()[0] || await context.newPage()
    await restorePersistentStorage(context, restoredPage, restored?.storageState)
    const page = await prepareLaneContext(lane, context, proxy, recovery, signal)
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

async function recoverLaneAfterPressure(lane) {
  try {
    if (lane.proxyPool.length < 2) {
      await initializeShopPage(lane, lane.context, lane.page, lane.proxy, true)
      return
    }

    const previousProxy = lane.proxy
    const nextProxyIndex = (lane.proxyIndex + 1) % lane.proxyPool.length
    const nextProxy = lane.proxyPool[nextProxyIndex]
    await replaceLaneContext(lane, nextProxyIndex, true)
    console.log(`${new Date().toISOString()} lane ${lane.index + 1} rotated product sync proxy after pressure backoff: ${previousProxy?.server || 'direct'} -> ${nextProxy?.server || 'direct'}`)
    publishLaneStatus(lane, {
      proxy_server: nextProxy?.server || '',
      proxy_pool_position: nextProxyIndex + 1,
      proxy_rotated_at: new Date().toISOString(),
    })
  } catch (error) {
    const restartError = error instanceof Error ? error : new Error(String(error))
    restartError.restartLane = true
    throw restartError
  }
}

async function runLane(lane) {
  while (activeBrowser?.isConnected() && !stopping) {
    try {
      const data = await backend('/api/v1/public/account-import/products/sync-job?limit=1')
      const job = Array.isArray(data.jobs) && data.jobs.length > 0 ? data.jobs[0] : data.job
      publishLaneStatus(lane, {
        state: job ? 'syncing' : 'idle',
        last_poll_at: new Date().toISOString(),
        active_shop_id: job?.shop_id || '',
        active_shop_name: job?.shop_name || '',
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
        })
      } catch (error) {
        if (error?.restartLane || error?.restartBrowser) throw error
        if (error?.kind === 'lease_lost') {
          console.warn(`${new Date().toISOString()} discarded expired sync result for ${job.shop_name}`)
          publishLaneStatus(lane, {
            state: 'idle',
            active_shop_id: '',
            active_shop_name: '',
            last_error: '',
            retry_at: '',
          })
        } else if (!isPressureError(error)) {
          console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync failed for ${job.shop_name}: ${errorMessage(error)}`)
          publishLaneStatus(lane, {
            state: 'error',
            active_shop_id: '',
            active_shop_name: '',
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
              await recoverLaneAfterPressure(lane)
            } catch (recoveryError) {
              console.error(`${new Date().toISOString()} lane ${lane.index + 1} pressure recovery failed: ${errorMessage(recoveryError)}`)
              if (recoveryError?.restartLane || recoveryError?.restartBrowser) throw recoveryError
            }
          }
          publishLaneStatus(lane, { state: 'idle', retry_at: '' })
        }
      }
      await sleep(activePollMilliseconds)
    } catch (error) {
      if (error?.restartLane || error?.restartBrowser) throw error
      console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync polling failed: ${errorMessage(error)}`)
      publishLaneStatus(lane, {
        state: 'error',
        active_shop_id: '',
        active_shop_name: '',
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
    // the existing lane supervisor semantics while each lane owns its Chrome
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
      browser_engine: 'playwright',
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

process.once('SIGINT', () => void shutdown('SIGINT'))
process.once('SIGTERM', () => void shutdown('SIGTERM'))

main().catch((error) => {
  console.error(`${new Date().toISOString()} worker stopped: ${errorMessage(error)}`)
  publishStatus({ state: 'stopped', last_error: errorMessage(error) })
  process.exitCode = 1
})
