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
  simulatedTokenBucketDuration,
} = require('./worker-utils')

test('catalogProductState validates status, stock, and minimum quantity from real list shapes', () => {
  const [available, soldOut, disabled] = fixture.goodsList.data.list
  assert.equal(catalogProductState(available, 'card').state, 'candidate')
  assert.equal(catalogProductState(soldOut, 'card').state, 'unavailable')
  assert.equal(catalogProductState(disabled, 'card').state, 'unavailable')
  assert.equal(catalogProductState({ ...available, status: undefined }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, status: '' }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, status: 'unexpected' }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, extend: { ...available.extend, status: 0 } }, 'card').state, 'unavailable')
  assert.equal(catalogProductState({ ...available, extend: {} }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, extend: { ...available.extend, stock_count: null } }, 'card').state, 'unknown')
  assert.equal(catalogProductState({ ...available, price: '' }, 'card').state, 'unknown')
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

test('TokenBucket enforces three requests per second with a capacity of two', async () => {
  let now = 0
  const waits = []
  const bucket = new TokenBucket(3, 2, {
    now: () => now,
    sleep: async (milliseconds) => {
      waits.push(milliseconds)
      now += milliseconds
    },
  })
  await bucket.take()
  await bucket.take()
  assert.equal(now, 0)
  await bucket.take()
  assert.ok(now >= 333 && now <= 334)
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
  assert.ok(simulatedTokenBucketDuration(2725) > 15 * 60_000)
  assert.ok(simulatedTokenBucketDuration(2725) < 16 * 60_000)
  assert.ok(simulatedTokenBucketDuration(647) < 4 * 60_000)
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
  assert.equal(parseSyncConcurrency(undefined), 2)
  assert.equal(parseSyncConcurrency('2'), 2)
  assert.throws(() => parseSyncConcurrency('1'), /must be 2/)
  assert.throws(() => parseSyncConcurrency('5'), /must be 2/)
})
