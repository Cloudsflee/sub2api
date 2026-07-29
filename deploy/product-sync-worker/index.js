const { chromium } = require('playwright-core')
const fs = require('node:fs')
const { collectAuthoritativeSnapshot } = require('./catalog-sync')
const {
  Semaphore,
  TokenBucket,
  isPressureError,
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfigurations,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  proxyPoolsForConcurrency,
  shopRequestError,
  withPressureRecovery,
} = require('./worker-utils')

const backendURL = process.env.BACKEND_URL || 'http://sub2api:8080'
const backendToken = String(process.env.PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN || '').trim()
const idlePollMilliseconds = 10_000
const activePollMilliseconds = 10_000
const heartbeatMilliseconds = 30_000
const maxJobMilliseconds = 20 * 60_000
const shopRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.SHOP_REQUEST_TIMEOUT_MILLISECONDS, 20_000, 'SHOP_REQUEST_TIMEOUT_MILLISECONDS')
const browserProtocolTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS, 45_000, 'BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS')
const backendRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BACKEND_REQUEST_TIMEOUT_MILLISECONDS, 10_000, 'BACKEND_REQUEST_TIMEOUT_MILLISECONDS')
const syncConcurrency = parseSyncConcurrency(process.env.PRODUCT_SYNC_CONCURRENCY)
const chromePath = process.env.CHROME_PATH || '/usr/bin/chromium-browser'
const chromeProfileDirectory = process.env.CHROME_PROFILE_DIRECTORY || '/data/chrome-profile'
const statusFile = process.env.STATUS_FILE || '/data/status.json'
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
const shopRequestsPerSecond = 1
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))
let workerStatus = {
  state: 'starting',
  proxy_enabled: configuredProxyCount > 0,
  proxy_count: configuredProxyCount,
  started_at: new Date().toISOString(),
}
const laneStatuses = Array.from({ length: syncConcurrency }, (_, index) => ({ index, state: 'starting' }))
let activeBrowser
let activeBrowserContexts = []
let stopping = false

function publishStatus(values) {
  workerStatus = { ...workerStatus, ...values, updated_at: new Date().toISOString() }
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
  const state = stopping ? 'stopping'
    : states.includes('syncing') ? 'syncing'
      : states.includes('blocked') ? 'blocked'
        : states.includes('error') ? 'error'
          : states.every((value) => value === 'starting') ? 'starting'
            : 'idle'
  publishStatus({
    state,
    active_jobs: states.filter((value) => value === 'syncing').length,
    lanes: laneStatuses,
  })
}

function errorMessage(error) {
  return String(error?.message || error).replace(/[\r\n]+/g, ' ').slice(0, 500)
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
  if (!response.ok) throw new Error(`backend ${path} returned HTTP ${response.status}`)
  const payload = await response.json()
  if (payload.code !== 0) throw new Error(payload.message || `backend ${path} failed`)
  return payload.data
}

function ensureJobDeadline(deadlineAt) {
  if (Date.now() >= deadlineAt) throw new Error('product sync job exceeded the 20 minute limit')
}

async function postShopAPI(lane, shopToken, path, body, deadlineAt) {
  ensureJobDeadline(deadlineAt)
  await lane.requestLimiter.take()
  ensureJobDeadline(deadlineAt)
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
    throw shopRequestError(path, error)
  }
  return parseShopHTTPResponse(result)
}

async function postShopAPIWithPressureRecovery(lane, shopToken, path, body, deadlineAt) {
  return withPressureRecovery(
    () => postShopAPI(lane, shopToken, path, body, deadlineAt),
    {
      state: lane.pressureState,
      deadlineAt,
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

async function rejectVerificationPage(page) {
  const state = await page.evaluate(() => ({
    title: document.title,
    text: (document.body?.innerText || '').slice(0, 300),
    hasCaptcha: Boolean(document.querySelector('#captcha-element, #aliyunCaptcha-sliding-slider')),
  }))
  if (isVerificationPageState(state)) {
    const error = new Error('shop API verification required: Alibaba Cloud ESA challenge')
    error.kind = 'verification'
    throw error
  }
}

async function initializeShopPage(page, recovery = false) {
  try {
    try {
      await page.goto('https://pay.ldxp.cn/', { waitUntil: 'domcontentloaded', timeout: 20_000 })
    } catch (error) {
      if (error.name !== 'TimeoutError') throw error
      console.log(`${new Date().toISOString()} shop session navigation timed out; continuing with the loaded document`)
    }
    await rejectVerificationPage(page)
    if (recovery) console.log(`${new Date().toISOString()} shop session recovered after verification backoff`)
  } catch (error) {
    if (error?.kind) throw error
    throw shopRequestError('/', error)
  }
}

function startJobHeartbeat(job) {
  let stopped = false
  let pending = Promise.resolve()
  const send = () => {
    if (stopped) return
    pending = pending
      .then(() => backend('/api/v1/public/account-import/products/sync-heartbeat', {
        method: 'POST',
        body: JSON.stringify({ shop_id: job.shop_id, attempt_id: job.attempt_id }),
      }))
      .catch((error) => {
        console.error(`${new Date().toISOString()} heartbeat failed for ${job.shop_name}: ${errorMessage(error)}`)
      })
  }
  const timer = setInterval(send, heartbeatMilliseconds)
  return async () => {
    stopped = true
    clearInterval(timer)
    await pending
  }
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
  const stopHeartbeat = startJobHeartbeat(job)
  try {
    const snapshot = await collectAuthoritativeSnapshot({
      shopToken: job.token,
      quoteSemaphore: lane.quoteSemaphore,
      post: (path, body) => postShopAPIWithPressureRecovery(lane, job.token, path, body, deadlineAt),
    })
    await stopHeartbeat()
    ensureJobDeadline(deadlineAt)
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
    await stopHeartbeat()
    await reportJobFailure(job, error)
    throw error
  }
}

function browserLaunchOptions() {
  return {
    executablePath: chromePath,
    headless: true,
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
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
    extraHTTPHeaders: { 'Accept-Language': 'zh-CN,zh;q=0.9,en;q=0.8' },
    ...(proxy ? { proxy } : {}),
  }
}

async function prepareLaneContext(context, recovery = false) {
  context.setDefaultTimeout(browserProtocolTimeoutMilliseconds)
  await context.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => undefined, configurable: true })
    Object.defineProperty(Navigator.prototype, 'languages', { get: () => ['zh-CN', 'zh', 'en'], configurable: true })
    if (!window.chrome) Object.defineProperty(window, 'chrome', { value: { runtime: {} } })
  })
  await context.route('**/*', async (route) => {
    if (['image', 'media', 'font'].includes(route.request().resourceType())) await route.abort()
    else await route.continue()
  })
  const existingPages = context.pages()
  for (const page of existingPages.slice(1)) {
    await page.close()
  }
  const page = existingPages[0] || await context.newPage()
  await initializeShopPage(page, recovery)
  return page
}

async function prepareLane(context, index, proxyPool) {
  const page = await prepareLaneContext(context)
  return {
    index,
    context,
    page,
    proxyPool,
    proxyIndex: 0,
    proxy: proxyPool[0],
    pressureState: { failureCount: 0 },
    quoteSemaphore: new Semaphore(1),
    requestLimiter: new TokenBucket(shopRequestsPerSecond, 1),
  }
}

async function recoverLaneAfterPressure(lane) {
  if (lane.proxyPool.length < 2) {
    await initializeShopPage(lane.page, true)
    return
  }

  const previousContext = lane.context
  const previousProxy = lane.proxy
  const nextProxyIndex = (lane.proxyIndex + 1) % lane.proxyPool.length
  const nextProxy = lane.proxyPool[nextProxyIndex]
  lane.context = null
  lane.page = null
  lane.proxyIndex = nextProxyIndex
  lane.proxy = nextProxy
  activeBrowserContexts[lane.index] = null

  let replacementContext
  try {
    await previousContext?.close().catch(() => {})
    if (stopping) throw new Error('product sync worker is stopping')
    if (!activeBrowser?.isConnected()) throw new Error('product sync browser is not connected')
    replacementContext = await activeBrowser.newContext(browserContextOptions(nextProxy))
    const replacementPage = await prepareLaneContext(replacementContext, true)
    if (stopping) throw new Error('product sync worker is stopping')
    lane.context = replacementContext
    lane.page = replacementPage
    activeBrowserContexts[lane.index] = replacementContext
  } catch (error) {
    await replacementContext?.close().catch(() => {})
    if (activeBrowserContexts[lane.index] === replacementContext) activeBrowserContexts[lane.index] = null
    const restartError = error instanceof Error ? error : new Error(String(error))
    restartError.restartBrowser = true
    throw restartError
  }

  console.log(`${new Date().toISOString()} lane ${lane.index + 1} rotated product sync proxy after pressure backoff: ${previousProxy?.server || 'direct'} -> ${nextProxy?.server || 'direct'}`)
  publishLaneStatus(lane, {
    proxy_server: nextProxy?.server || '',
    proxy_pool_position: nextProxyIndex + 1,
    proxy_rotated_at: new Date().toISOString(),
  })
}

async function runLane(lane) {
  if (lane.index > 0) await sleep(lane.index * 1_100)
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
        console.error(`${new Date().toISOString()} lane ${lane.index + 1} sync failed for ${job.shop_name}: ${errorMessage(error)}`)
        if (error?.restartBrowser) throw error
        if (!isPressureError(error)) {
          publishLaneStatus(lane, {
            state: 'error',
            active_shop_id: '',
            active_shop_name: '',
            last_error_at: new Date().toISOString(),
            last_error: errorMessage(error),
          })
        } else {
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
          await sleep(waitMilliseconds)
          if (!stopping) {
            try {
              await recoverLaneAfterPressure(lane)
            } catch (recoveryError) {
              console.error(`${new Date().toISOString()} lane ${lane.index + 1} pressure recovery failed: ${errorMessage(recoveryError)}`)
              if (recoveryError?.restartBrowser) throw recoveryError
            }
          }
          publishLaneStatus(lane, { state: 'idle', retry_at: '' })
        }
      }
      await sleep(activePollMilliseconds)
    } catch (error) {
      if (error?.restartBrowser) throw error
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

async function runBrowser() {
  const contexts = []
  const lanes = []
  let browser
  try {
    const usesRotatableContexts = laneProxyPools.some((pool) => pool.length > 1)
    if (syncConcurrency === 1 && !usesRotatableContexts) {
      fs.mkdirSync(chromeProfileDirectory, { recursive: true })
      for (const name of ['SingletonLock', 'SingletonCookie', 'SingletonSocket']) {
        fs.rmSync(`${chromeProfileDirectory}/${name}`, { force: true })
      }
      const context = await chromium.launchPersistentContext(chromeProfileDirectory, {
        ...browserLaunchOptions(),
        ...browserContextOptions(laneProxies[0]),
      })
      browser = context.browser()
      contexts.push(context)
    } else {
      browser = await chromium.launch(browserLaunchOptions())
      for (const proxy of laneProxies) {
        contexts.push(await browser.newContext(browserContextOptions(proxy)))
      }
    }
    activeBrowser = browser
    activeBrowserContexts = contexts
    for (let index = 0; index < contexts.length; index += 1) {
      lanes.push(await prepareLane(contexts[index], index, laneProxyPools[index]))
      publishLaneStatus(lanes[index], {
        state: 'idle',
        proxy_server: laneProxies[index]?.server || '',
        proxy_pool_position: 1,
        proxy_pool_size: laneProxyPools[index].length,
      })
    }
    publishStatus({
      browser_engine: 'playwright',
      sync_concurrency: syncConcurrency,
      quote_concurrency_per_lane: 1,
      request_rate_per_second_per_lane: shopRequestsPerSecond,
      browser_started_at: new Date().toISOString(),
    })
    await Promise.all(lanes.map((lane) => runLane(lane)))
  } finally {
    if (activeBrowser === browser) activeBrowser = undefined
    if (activeBrowserContexts === contexts) activeBrowserContexts = []
    await Promise.all(contexts.filter(Boolean).map((context) => context.close().catch(() => {})))
    await browser?.close().catch(() => {})
  }
}

async function main() {
  if (!backendToken) throw new Error('PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN is required')
  publishStatus({ state: 'starting' })
  console.log(`${new Date().toISOString()} product sync worker starting; proxy_lanes=${configuredProxies.length}; proxy_endpoints=${configuredProxyCount}`)
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
