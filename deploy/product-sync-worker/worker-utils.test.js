const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'shop-api.json'), 'utf8'))
const {
  ShopSyncError,
  TokenBucket,
  catalogProductState,
  isPressureError,
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  quoteResult,
  selectPaymentChannel,
  shopRequestError,
  simulatedTokenBucketDuration,
} = require('./worker-utils')

test('catalogProductState validates status, stock, and minimum quantity from real list shapes', () => {
  const [available, soldOut, disabled] = fixture.goodsList.data.list
  const availableState = catalogProductState(available, 'card')
  assert.equal(availableState.state, 'candidate')
  assert.equal(availableState.product.minimum_quantity, 1)
  assert.equal(catalogProductState(soldOut, 'card').state, 'unavailable')
  assert.equal(catalogProductState(disabled, 'card').state, 'unavailable')
  assert.equal(catalogProductState({ ...available, status: undefined }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, status: '' }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, status: 'unexpected' }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, extend: { ...available.extend, status: 0 } }, 'card').state, 'unavailable')
  assert.equal(catalogProductState({ ...available, extend: {} }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, extend: { ...available.extend, stock_count: null } }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, extend: { ...available.extend, limit_count: -1 } }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, price: '' }, 'card').state, 'unknown')
})

test('catalogProductState normalizes inventoryless product types to one quoted unit', () => {
  for (const item of fixture.inventorylessGoods) {
    const state = catalogProductState(item, item.goods_type)
    assert.equal(state.state, 'candidate')
    assert.equal(state.product.stock, 1)
    assert.equal(state.product.minimum_quantity, 1)
  }

  const article = fixture.inventorylessGoods[0]
  assert.equal(catalogProductState({ ...article, goods_type: 'card' }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...article, extend: { stock_count: 1 } }, 'article').state, 'unknown')
  assert.equal(catalogProductState({ ...article, extend: { stock_count: null, limit_count: null } }, 'article').state, 'unknown')
})

test('selectPaymentChannel prefers an explicit valid default and falls back to the first valid channel', () => {
  assert.equal(selectPaymentChannel(fixture.channels.data).id, 30)
  assert.equal(selectPaymentChannel(fixture.channels.data.slice(0, 2)).id, 20)
  assert.equal(selectPaymentChannel([{ id: 1, status: 0 }]), null)
  assert.equal(selectPaymentChannel([{ id: 1, status: null }]), null)
})

test('quoteResult accepts only finite non-negative total_amount values', () => {
  assert.deepEqual(quoteResult(fixture.quote), { state: 'available', totalAmount: 2.2 })
  assert.deepEqual(quoteResult(fixture.soldOutQuote), { state: 'unavailable' })
  assert.equal(quoteResult({ code: 1, data: { total_amount: 'NaN' } }).state, 'unknown')
  assert.equal(quoteResult({ code: 1, data: { total_amount: null } }).state, 'unknown')
  assert.equal(quoteResult({ code: 1, data: { total_amount: '' } }).state, 'unknown')
  assert.equal(quoteResult({ code: 1, data: {} }).state, 'unknown')
})

test('parseShopHTTPResponse classifies verification pages and HTTP 429 as pressure errors', () => {
  assert.throws(
    () => parseShopHTTPResponse(fixture.verificationHTTP),
    (error) => error instanceof ShopSyncError && error.kind === 'verification' && isPressureError(error)
  )
  assert.throws(
    () => parseShopHTTPResponse(fixture.rateLimitHTTP),
    (error) => error instanceof ShopSyncError && error.kind === 'rate_limit' && isPressureError(error)
  )
  assert.deepEqual(parseShopHTTPResponse({
    status: 200,
    contentType: 'application/json; charset=utf-8',
    payload: fixture.quote,
  }), fixture.quote)
})

test('shopRequestError classifies browser transport failures as pressure errors', () => {
  for (const error of [
    new TypeError('Failed to fetch'),
    Object.assign(new Error('signal is aborted without reason'), { name: 'AbortError' }),
    new Error('page.evaluate: NetworkError when attempting to fetch resource'),
    new Error('net::ERR_PROXY_CONNECTION_FAILED'),
  ]) {
    const wrapped = shopRequestError('/shopApi/Shop/goodsList', error)
    assert.ok(wrapped instanceof ShopSyncError)
    assert.equal(wrapped.kind, 'network')
    assert.equal(isPressureError(wrapped), true)
  }

  const applicationError = shopRequestError('/shopApi/Shop/goodsList', new Error('execution context was destroyed'))
  assert.equal(applicationError.kind, 'unknown')
  assert.equal(isPressureError(applicationError), false)
})

test('TokenBucket enforces one request per second without a burst', async () => {
  let now = 0
  const waits = []
  const bucket = new TokenBucket(1, 1, {
    now: () => now,
    sleep: async (milliseconds) => {
      waits.push(milliseconds)
      now += milliseconds
    },
  })
  await bucket.take()
  assert.equal(now, 0)
  await bucket.take()
  assert.equal(now, 1_000)
  assert.equal(waits.length, 1)
})

test('55-shop pressure simulation stays bounded for 2725 products and a 647-product shop', () => {
  const shopCounts = [647]
  let remaining = 2725 - 647
  for (let index = 0; index < 54; index += 1) {
    const count = Math.floor(remaining / (54 - index))
    shopCounts.push(count)
    remaining -= count
  }
  assert.equal(shopCounts.length, 55)
  assert.equal(shopCounts.reduce((sum, count) => sum + count, 0), 2725)
  assert.equal(Math.max(...shopCounts), 647)
  assert.ok(simulatedTokenBucketDuration(2725) > 45 * 60_000)
  assert.ok(simulatedTokenBucketDuration(2725) < 46 * 60_000)
  assert.ok(simulatedTokenBucketDuration(647) > 10 * 60_000)
  assert.ok(simulatedTokenBucketDuration(647) < 11 * 60_000)
})

test('pressure backoff uses one, five, and fifteen minute tiers with bounded jitter', () => {
  assert.equal(pressureBackoffMilliseconds(1, () => 0.5), 60_000)
  assert.equal(pressureBackoffMilliseconds(2, () => 0.5), 300_000)
  assert.equal(pressureBackoffMilliseconds(3, () => 0.5), 900_000)
  assert.equal(pressureBackoffMilliseconds(10, () => 0.5), 900_000)
  assert.equal(pressureBackoffMilliseconds(1, () => 0), 54_000)
  assert.equal(pressureBackoffMilliseconds(1, () => 1), 66_000)
})

test('parseProxyConfiguration removes credentials from the Chromium proxy argument', () => {
  assert.deepEqual(parseProxyConfiguration('http://user:p%40ss@proxy.example:8080'), {
    server: 'http://proxy.example:8080',
    username: 'user',
    password: 'p@ss',
  })
})

test('parseProxyConfiguration supports an unauthenticated SOCKS5 proxy', () => {
  assert.deepEqual(parseProxyConfiguration('socks5://127.0.0.1:1080'), {
    server: 'socks5://127.0.0.1:1080',
    username: '',
    password: '',
  })
})

test('parseProxyConfiguration rejects unsafe or unsupported forms', () => {
  assert.throws(() => parseProxyConfiguration('ftp://proxy.example:21'), /must use http/)
  assert.throws(() => parseProxyConfiguration('http://proxy.example/path'), /only proxy credentials/)
  assert.throws(() => parseProxyConfiguration('socks5://user:pass@proxy.example:1080'), /authenticated SOCKS5/)
})

test('isVerificationPageState detects the Alibaba ESA challenge', () => {
  assert.equal(isVerificationPageState({
    title: 'Verification',
    text: 'Please slide to verify',
    hasCaptcha: true,
  }), true)
  assert.equal(isVerificationPageState({ title: 'Shop', text: 'Products', hasCaptcha: false }), false)
})

test('timing and fixed concurrency configuration are validated', () => {
  assert.equal(parsePositiveMilliseconds(undefined, 20_000, 'TIMEOUT'), 20_000)
  assert.equal(parsePositiveMilliseconds('1500.9', 20_000, 'TIMEOUT'), 1500)
  assert.throws(() => parsePositiveMilliseconds('0', 20_000, 'TIMEOUT'), /positive number/)
  assert.equal(parseSyncConcurrency(undefined), 1)
  assert.equal(parseSyncConcurrency('1'), 1)
  assert.throws(() => parseSyncConcurrency('2'), /must be 1/)
  assert.throws(() => parseSyncConcurrency('5'), /must be 1/)
})
