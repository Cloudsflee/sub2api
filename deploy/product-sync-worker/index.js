const { chromium } = require('playwright-core')
const fs = require('node:fs')
const { collectAuthoritativeSnapshot } = require('./catalog-sync')
const {
  Semaphore,
  TokenBucket,
  isPressureError,
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  shopRequestError,
} = require('./worker-utils')

const backendURL = process.env.BACKEND_URL || 'http://sub2api:8080'
const backendToken = String(process.env.PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN || '').trim()
const idlePollMilliseconds = 10_000
const activePollMilliseconds = 1_000
const heartbeatMilliseconds = 30_000
const maxJobMilliseconds = 20 * 60_000
const shopRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.SHOP_REQUEST_TIMEOUT_MILLISECONDS, 20_000, 'SHOP_REQUEST_TIMEOUT_MILLISECONDS')
const browserProtocolTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS, 45_000, 'BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS')
const backendRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BACKEND_REQUEST_TIMEOUT_MILLISECONDS, 10_000, 'BACKEND_REQUEST_TIMEOUT_MILLISECONDS')
const syncConcurrency = parseSyncConcurrency(process.env.PRODUCT_SYNC_CONCURRENCY)
const chromePath = process.env.CHROME_PATH || '/usr/bin/chromium-browser'
const chromeProfileDirectory = process.env.CHROME_PROFILE_DIRECTORY || '/data/chrome-profile'
const statusFile = process.env.STATUS_FILE || '/data/status.json'
const proxy = parseProxyConfiguration(process.env.PRODUCT_SYNC_PROXY_URL)
const shopRequestLimiter = new TokenBucket(3, 2)
const quoteSemaphore = new Semaphore(2)
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))
let workerStatus = {
  state: 'starting',
  proxy_enabled: Boolean(proxy),
  started_at: new Date().toISOString(),
}
let activeBrowserContext
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

async function postShopAPI(page, shopToken, path, body, deadlineAt) {
  ensureJobDeadline(deadlineAt)
  await shopRequestLimiter.take()
  ensureJobDeadline(deadlineAt)
  let result
  try {
    result = await page.evaluate(async ({ requestPath, requestBody, requestTimeoutMilliseconds, visitorID }) => {
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
    await page.goto('https://pay.ldxp.cn/', { waitUntil: 'domcontentloaded', timeout: 20_000 })
  } catch (error) {
    if (error.name !== 'TimeoutError') throw error
    console.log(`${new Date().toISOString()} shop session navigation timed out; continuing with the loaded document`)
  }
  await rejectVerificationPage(page)
  if (recovery) console.log(`${new Date().toISOString()} shop session recovered after verification backoff`)
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

async function syncJob(page, job) {
  const deadlineAt = Date.now() + maxJobMilliseconds
  const stopHeartbeat = startJobHeartbeat(job)
  try {
    const snapshot = await collectAuthoritativeSnapshot({
      shopToken: job.token,
      quoteSemaphore,
      post: (path, body) => postShopAPI(page, job.token, path, body, deadlineAt),
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

async function runBrowser() {
  fs.mkdirSync(chromeProfileDirectory, { recursive: true })
  for (const name of ['SingletonLock', 'SingletonCookie', 'SingletonSocket']) {
    fs.rmSync(`${chromeProfileDirectory}/${name}`, { force: true })
  }
  const context = await chromium.launchPersistentContext(chromeProfileDirectory, {
    executablePath: chromePath,
    headless: true,
    timeout: browserProtocolTimeoutMilliseconds,
    viewport: { width: 1024, height: 768 },
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    colorScheme: 'light',
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
    extraHTTPHeaders: { 'Accept-Language': 'zh-CN,zh;q=0.9,en;q=0.8' },
    ...(proxy ? { proxy } : {}),
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
  })
  activeBrowserContext = context
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
  for (const page of existingPages.slice(syncConcurrency)) {
    await page.close()
  }
  const pages = existingPages.slice(0, syncConcurrency)
  while (pages.length < syncConcurrency) {
    pages.push(await context.newPage())
  }
  try {
    await Promise.all(pages.map((page) => initializeShopPage(page)))
  } catch (error) {
    if (activeBrowserContext === context) activeBrowserContext = undefined
    await context.close().catch(() => {})
    throw error
  }
  const browser = context.browser()
  let pressureFailures = 0
  publishStatus({
    state: 'idle',
    active_jobs: 0,
    browser_engine: 'playwright',
    sync_concurrency: syncConcurrency,
    quote_concurrency: 2,
    request_rate_per_second: 3,
    browser_started_at: new Date().toISOString(),
  })

  while (browser?.isConnected() && !stopping) {
    try {
      const data = await backend(`/api/v1/public/account-import/products/sync-job?limit=${syncConcurrency}`)
      const jobs = Array.isArray(data.jobs) && data.jobs.length > 0
        ? data.jobs.slice(0, syncConcurrency)
        : data.job ? [data.job] : []
      publishStatus({
        state: jobs.length > 0 ? 'syncing' : 'idle',
        active_jobs: jobs.length,
        last_poll_at: new Date().toISOString(),
      })
      if (jobs.length === 0) {
        await sleep(idlePollMilliseconds)
        continue
      }

      const results = await Promise.allSettled(jobs.map((job, index) => syncJob(pages[index], job)))
      const failures = results
        .map((result, index) => ({ result, job: jobs[index] }))
        .filter(({ result }) => result.status === 'rejected')
      for (const { result, job } of failures) {
        console.error(`${new Date().toISOString()} sync failed for ${job.shop_name}: ${errorMessage(result.reason)}`)
      }
      const pressureFailure = failures.find(({ result }) => isPressureError(result.reason))
      if (pressureFailure) {
        pressureFailures += 1
        const waitMilliseconds = pressureBackoffMilliseconds(pressureFailures)
        const reason = pressureFailure.result.reason
        publishStatus({
          state: 'blocked',
          active_jobs: 0,
          pressure_failure_count: pressureFailures,
          retry_at: new Date(Date.now() + waitMilliseconds).toISOString(),
          last_error_at: new Date().toISOString(),
          last_error: errorMessage(reason),
        })
        console.log(`${new Date().toISOString()} applying product sync pressure backoff for ${Math.round(waitMilliseconds / 1000)} seconds`)
        await sleep(waitMilliseconds)
        if (reason?.kind === 'verification' && !stopping) {
          try {
            await Promise.all(pages.map((page) => initializeShopPage(page, true)))
          } catch (recoveryError) {
            console.error(`${new Date().toISOString()} verification recovery failed: ${errorMessage(recoveryError)}`)
          }
        }
        continue
      }

      if (failures.length === 0) pressureFailures = 0
      publishStatus({
        state: failures.length > 0 ? 'error' : 'idle',
        active_jobs: 0,
        ...(failures.length > 0 ? {
          last_error_at: new Date().toISOString(),
          last_error: errorMessage(failures[0].result.reason),
        } : {}),
      })
      await sleep(activePollMilliseconds)
    } catch (error) {
      console.error(`${new Date().toISOString()} sync polling failed: ${errorMessage(error)}`)
      publishStatus({
        state: 'error',
        active_jobs: 0,
        last_error_at: new Date().toISOString(),
        last_error: errorMessage(error),
      })
      await sleep(idlePollMilliseconds)
    }
  }
  if (activeBrowserContext === context) activeBrowserContext = undefined
}

async function main() {
  if (!backendToken) throw new Error('PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN is required')
  publishStatus({ state: 'starting' })
  console.log(`${new Date().toISOString()} product sync worker starting; proxy=${proxy ? 'enabled' : 'disabled'}`)
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
    await activeBrowserContext?.close()
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
