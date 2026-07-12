const puppeteer = require('puppeteer-core')

const backendURL = process.env.BACKEND_URL || 'http://sub2api:8080'
const pollMilliseconds = Number(process.env.POLL_MILLISECONDS || 10000)
const chromePath = process.env.CHROME_PATH || '/usr/bin/chromium-browser'
const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

async function backend(path, options = {}) {
  const response = await fetch(`${backendURL}${path}`, {
    ...options,
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
  })
  if (!response.ok) throw new Error(`backend ${path} returned HTTP ${response.status}`)
  const payload = await response.json()
  if (payload.code !== 0) throw new Error(payload.message || `backend ${path} failed`)
  return payload.data
}

async function collectProducts(page, token) {
  return page.evaluate(async (shopToken) => {
    const post = async (path, body) => {
      const response = await fetch(path, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify(body),
      })
      const contentType = response.headers.get('content-type') || ''
      if (!response.ok || !contentType.includes('application/json')) {
        throw new Error(`shop API verification required: HTTP ${response.status}`)
      }
      const payload = await response.json()
      if (payload.code !== 1) throw new Error(payload.msg || 'shop API request failed')
      return payload.data
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
  }, token)
}

async function syncJob(page, job) {
  let lastError
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      await page.goto(job.shop_url, { waitUntil: 'domcontentloaded', timeout: 45000 })
      await sleep(attempt * 3000)
      const products = await collectProducts(page, job.token)
      const result = await backend('/api/v1/public/account-import/products/sync', {
        method: 'POST',
        body: JSON.stringify({ shop_id: job.shop_id, products }),
      })
      console.log(`${new Date().toISOString()} synced ${job.shop_name}: ${result.accepted} products`)
      return
    } catch (error) {
      lastError = error
      await sleep(attempt * 5000)
    }
  }
  throw lastError
}

async function runBrowser() {
  const browser = await puppeteer.launch({
    executablePath: chromePath,
    headless: true,
    userDataDir: '/data/chrome-profile',
    args: [
      '--no-sandbox',
      '--disable-dev-shm-usage',
      '--disable-gpu',
      '--disable-background-timer-throttling',
      '--disable-backgrounding-occluded-windows',
      '--disable-renderer-backgrounding',
      '--disable-blink-features=AutomationControlled',
      '--no-first-run',
    ],
  })
  const pages = await browser.pages()
  const page = pages[0] || await browser.newPage()
  await page.setViewport({ width: 1024, height: 768 })
  await page.setUserAgent('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36')
  await page.setRequestInterception(true)
  page.on('request', (request) => {
    if (['image', 'media', 'font'].includes(request.resourceType())) request.abort()
    else request.continue()
  })

  while (browser.connected) {
    try {
      const data = await backend('/api/v1/public/account-import/products/sync-job')
      if (data.job) await syncJob(page, data.job)
    } catch (error) {
      console.error(`${new Date().toISOString()} sync failed: ${error.message}`)
    }
    await sleep(pollMilliseconds)
  }
}

async function main() {
  while (true) {
    try {
      await runBrowser()
    } catch (error) {
      console.error(`${new Date().toISOString()} browser restart: ${error.message}`)
      await sleep(15000)
    }
  }
}

main()
