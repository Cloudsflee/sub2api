const { chromium } = require('playwright-core')
const fs = require('node:fs')
const {
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseSyncConcurrency,
} = require('./worker-utils')

const backendURL = process.env.BACKEND_URL || 'http://sub2api:8080'
const pollMilliseconds = parsePositiveMilliseconds(process.env.POLL_MILLISECONDS, 10000, 'POLL_MILLISECONDS')
const verificationCooldownMilliseconds = parsePositiveMilliseconds(process.env.VERIFICATION_COOLDOWN_MILLISECONDS, 900000, 'VERIFICATION_COOLDOWN_MILLISECONDS')
const shopRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.SHOP_REQUEST_TIMEOUT_MILLISECONDS, 20000, 'SHOP_REQUEST_TIMEOUT_MILLISECONDS')
const browserProtocolTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS, 45000, 'BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS')
const backendRequestTimeoutMilliseconds = parsePositiveMilliseconds(process.env.BACKEND_REQUEST_TIMEOUT_MILLISECONDS, 10000, 'BACKEND_REQUEST_TIMEOUT_MILLISECONDS')
const syncConcurrency = parseSyncConcurrency(process.env.PRODUCT_SYNC_CONCURRENCY)
const chromePath = process.env.CHROME_PATH || '/usr/bin/chromium-browser'
const chromeProfileDirectory = process.env.CHROME_PROFILE_DIRECTORY || '/data/chrome-profile'
const statusFile = process.env.STATUS_FILE || '/data/status.json'
const proxy = parseProxyConfiguration(process.env.PRODUCT_SYNC_PROXY_URL)
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))
const isVerificationError = (error) => String(error?.message || error).includes('verification required')
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
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    signal: options.signal || AbortSignal.timeout(backendRequestTimeoutMilliseconds),
  })
  if (!response.ok) throw new Error(`backend ${path} returned HTTP ${response.status}`)
  const payload = await response.json()
  if (payload.code !== 0) throw new Error(payload.message || `backend ${path} failed`)
  return payload.data
}

async function collectProducts(page, token) {
  return page.evaluate(async ({ shopToken, requestTimeoutMilliseconds }) => {
    const post = async (path, body) => {
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), requestTimeoutMilliseconds)
      try {
        const response = await fetch(path, {
          method: 'POST',
          credentials: 'include',
          headers: {
            'Content-Type': 'application/json',
            Accept: 'application/json',
            Visitorid: `sub2api${shopToken.replace(/[^a-zA-Z0-9]/g, '').slice(0, 24)}`,
          },
          body: JSON.stringify(body),
          signal: controller.signal,
        })
        const contentType = response.headers.get('content-type') || ''
        if (!response.ok || !contentType.includes('application/json')) {
          throw new Error(`shop API verification required: HTTP ${response.status}`)
        }
        const payload = await response.json()
        if (payload.code !== 1) throw new Error(payload.msg || 'shop API request failed')
        return payload.data
      } finally {
        clearTimeout(timer)
      }
    }

    const info = await post('/shopApi/Shop/info', { token: shopToken, category_key: null })
    const counts = {
      card: Number(info.card_count || 0),
      article: Number(info.article_count || 0),
      resource: Number(info.resource_count || 0),
      equity: Number(info.equity_count || 0),
    }
    const products = []
    for (const [goodsType, count] of Object.entries(counts)) {
      if (count <= 0) continue
      for (let current = 1; current <= 10; current += 1) {
        const data = await post('/shopApi/Shop/goodsList', {
          token: shopToken,
          keywords: '',
          category_id: 0,
          goods_type: goodsType,
          current,
          pageSize: 100,
        })
        const list = Array.isArray(data.list) ? data.list : []
        for (const item of list) {
          const stock = Number(item.extend?.stock_count || 0)
          if (stock <= 0) continue
          products.push({
            goods_key: String(item.goods_key || ''),
            name: String(item.name || ''),
            url: String(item.link || ''),
            image: String(item.image || ''),
            category: String(item.category?.name || ''),
            goods_type: String(item.goods_type || goodsType),
            price: Number(item.price || 0),
            market_price: Number(item.market_price || 0),
            stock,
          })
        }
        const total = Number(data.total || 0)
        if (list.length < 100 || current * 100 >= total) break
      }
    }
    return products
  }, { shopToken: token, requestTimeoutMilliseconds: shopRequestTimeoutMilliseconds })
}

async function rejectVerificationPage(page) {
  const state = await page.evaluate(() => ({
    title: document.title,
    text: (document.body?.innerText || '').slice(0, 300),
    hasCaptcha: Boolean(document.querySelector('#captcha-element, #aliyunCaptcha-sliding-slider')),
  }))
  if (isVerificationPageState(state)) {
    throw new Error('shop API verification required: Alibaba Cloud ESA challenge')
  }
}

async function syncJob(page, job) {
  let lastError
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      try {
        await page.goto(job.shop_url, { waitUntil: 'domcontentloaded', timeout: 20000 })
      } catch (error) {
        if (error.name !== 'TimeoutError') throw error
        console.log(`${new Date().toISOString()} navigation still loading for ${job.shop_name}; trying the API directly`)
      }
      await sleep(attempt * 3000)
      await rejectVerificationPage(page)
      const products = await collectProducts(page, job.token)
      const result = await backend('/api/v1/public/account-import/products/sync', {
        method: 'POST',
        body: JSON.stringify({ shop_id: job.shop_id, attempt_id: job.attempt_id, products }),
      })
      console.log(`${new Date().toISOString()} synced ${job.shop_name}: ${result.accepted} products`)
      publishStatus({
        last_success_at: new Date().toISOString(),
        last_shop_id: job.shop_id,
        last_error: '',
      })
      return
    } catch (error) {
      lastError = error
      if (isVerificationError(error)) throw error
      await sleep(attempt * 5000)
    }
  }
  throw lastError
}

async function syncJobWithFailureReport(page, job) {
  try {
    await syncJob(page, job)
  } catch (error) {
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
    throw error
  }
}

async function runBrowser() {
  fs.mkdirSync(chromeProfileDirectory, { recursive: true })
  for (const name of ['SingletonLock', 'SingletonCookie', 'SingletonSocket']) {
    fs.rmSync(`${chromeProfileDirectory}/${name}`, { force: true, recursive: true })
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
  const pages = []
  for (let index = 0; index < syncConcurrency; index += 1) {
    pages.push(existingPages[index] || await context.newPage())
  }
  const browser = context.browser()
  publishStatus({
    state: 'idle',
    active_jobs: 0,
    browser_engine: 'playwright',
    sync_concurrency: syncConcurrency,
    browser_started_at: new Date().toISOString(),
  })

  while (browser?.isConnected() && !stopping) {
    let nextPoll = pollMilliseconds
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
      if (jobs.length > 0) {
        const results = await Promise.allSettled(jobs.map((job, index) => syncJobWithFailureReport(pages[index], job)))
        const failures = results
          .map((result, index) => ({ result, job: jobs[index] }))
          .filter(({ result }) => result.status === 'rejected')
        for (const { result, job } of failures) {
          console.error(`${new Date().toISOString()} sync failed for ${job.shop_name}: ${errorMessage(result.reason)}`)
        }
        const verificationFailure = failures.find(({ result }) => isVerificationError(result.reason))
        if (verificationFailure) throw verificationFailure.result.reason
        if (failures.length > 0) throw failures[0].result.reason
        publishStatus({ state: 'idle', active_jobs: 0 })
      }
    } catch (error) {
      const message = errorMessage(error)
      console.error(`${new Date().toISOString()} sync batch failed: ${message}`)
      publishStatus({
        state: isVerificationError(error) ? 'blocked' : 'error',
        active_jobs: 0,
        last_error_at: new Date().toISOString(),
        last_error: message,
      })
      if (isVerificationError(error)) {
        nextPoll = verificationCooldownMilliseconds
        console.log(`${new Date().toISOString()} pausing product sync after shop verification challenge`)
      }
    }
    await sleep(nextPoll)
  }
  if (activeBrowserContext === context) activeBrowserContext = undefined
}

async function main() {
  publishStatus({ state: 'starting' })
  console.log(`${new Date().toISOString()} product sync worker starting; proxy=${proxy ? 'enabled' : 'disabled'}`)
  while (!stopping) {
    try {
      await runBrowser()
    } catch (error) {
      console.error(`${new Date().toISOString()} browser restart: ${error.message}`)
      await sleep(15000)
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
