const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'shop-api.json'), 'utf8'))
const workloadFixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'sync-workload.json'), 'utf8'))
const {
  ShopSyncError,
  TokenBucket,
  browserResourceCounts,
  catalogProductState,
  closeContextThenCreate,
  isPressureError,
  isVerificationPageState,
  parsePositiveMilliseconds,
  parseProxyConfiguration,
  parseProxyConfigurations,
  parseRequestRatePerLane,
  parseShopHTTPResponse,
  parseSyncConcurrency,
  pressureBackoffMilliseconds,
  proxyLanesForConcurrency,
  proxyPoolsForConcurrency,
  quoteResult,
  selectPaymentChannel,
  shopRequestError,
  shopUnavailableMessage,
  simulatedTokenBucketDuration,
  takeRequestTokens,
  withPressureRecovery,
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

test('shopUnavailableMessage accepts only explicit permanent shop failures', () => {
  assert.equal(shopUnavailableMessage('店铺链接不存在'), true)
  assert.equal(shopUnavailableMessage('This shop does not exist'), true)
  assert.equal(shopUnavailableMessage('店铺接口请求失败'), false)
  assert.equal(shopUnavailableMessage('系统繁忙，请稍后重试'), false)
})

test('parseShopHTTPResponse classifies verification pages and HTTP 429/502/520 as pressure errors', () => {
  assert.throws(
    () => parseShopHTTPResponse(fixture.verificationHTTP),
    (error) => error instanceof ShopSyncError && error.kind === 'verification' && isPressureError(error)
  )
  assert.throws(
    () => parseShopHTTPResponse(fixture.rateLimitHTTP),
    (error) => error instanceof ShopSyncError && error.kind === 'rate_limit' && isPressureError(error)
  )
  for (const status of [502, 520]) {
    assert.throws(
      () => parseShopHTTPResponse({ status, contentType: 'application/json', payload: { code: 0 } }),
      (error) => error instanceof ShopSyncError && error.kind === 'network' && isPressureError(error)
    )
  }
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
    new Error('page.evaluate: AbortError: signal is aborted without reason'),
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

test('0.75 request/second lane and concurrent 4.5 request/second global buckets have no burst', async () => {
  let laneNow = 0
  const laneBucket = new TokenBucket(0.75, 1, {
    now: () => laneNow,
    sleep: async (milliseconds) => { laneNow += milliseconds },
  })
  await laneBucket.take()
  await laneBucket.take()
  assert.equal(laneNow, 1_334)

  let globalNow = 0
  const globalBucket = new TokenBucket(4.5, 1, {
    now: () => globalNow,
    sleep: async (milliseconds) => { globalNow += milliseconds },
  })
  const globalRequestTimes = await Promise.all(
    Array.from({ length: 6 }, () => globalBucket.take())
  )
  assert.deepEqual(globalRequestTimes, [0, 223, 446, 669, 892, 1_115])
})

test('requests take the lane token before the shared global token', async () => {
  const calls = []
  await takeRequestTokens(
    { take: async () => calls.push('lane') },
    { take: async () => calls.push('global') }
  )
  assert.deepEqual(calls, ['lane', 'global'])
})

test('six-lane 53-shop workload completes between 20 and 21 minutes without pressure', () => {
  const shopCounts = workloadFixture.shop_product_counts
  assert.equal(shopCounts.length, 53)
  assert.equal(shopCounts.reduce((sum, count) => sum + count, 0), 3981)
  assert.equal(Math.max(...shopCounts), 898)

  const laneAvailableAt = [0, 0, 0, 0, 0, 0]
  const laneCompletedAt = [0, 0, 0, 0, 0, 0]
  for (const productCount of shopCounts) {
    const laneIndex = laneAvailableAt.indexOf(Math.min(...laneAvailableAt))
    const requestCount = productCount + Math.ceil(productCount / 100) + 5
    laneCompletedAt[laneIndex] = laneAvailableAt[laneIndex]
      + simulatedTokenBucketDuration(requestCount, 0.75, 1)
    laneAvailableAt[laneIndex] = laneCompletedAt[laneIndex] + 10_000
  }
  const cycleMilliseconds = Math.max(...laneCompletedAt)
  assert.ok(cycleMilliseconds > 20 * 60_000)
  assert.ok(cycleMilliseconds < 21 * 60_000)
})

test('pressure backoff uses one, five, and fifteen minute tiers with bounded jitter', () => {
  assert.equal(pressureBackoffMilliseconds(1, () => 0.5), 60_000)
  assert.equal(pressureBackoffMilliseconds(2, () => 0.5), 300_000)
  assert.equal(pressureBackoffMilliseconds(3, () => 0.5), 900_000)
  assert.equal(pressureBackoffMilliseconds(10, () => 0.5), 900_000)
  assert.equal(pressureBackoffMilliseconds(1, () => 0), 54_000)
  assert.equal(pressureBackoffMilliseconds(1, () => 1), 66_000)
})

test('pressure recovery retries the active operation without discarding completed shop work', async () => {
  const waits = []
  const backoffs = []
  const recoveries = []
  const state = { failureCount: 0 }
  let attempts = 0
  const result = await withPressureRecovery(async () => {
    attempts += 1
    if (attempts === 1) throw new ShopSyncError('verification', 'HTTP 520')
    if (attempts === 2) throw new ShopSyncError('rate_limit', 'HTTP 429')
    return 'verified quote'
  }, {
    state,
    deadlineAt: 30 * 60_000,
    now: () => 0,
    random: () => 0.5,
    sleep: async (milliseconds) => waits.push(milliseconds),
    onBackoff: async (details) => backoffs.push(details.failureCount),
    recover: async (details) => recoveries.push(details.failureCount),
  })

  assert.equal(result, 'verified quote')
  assert.equal(attempts, 3)
  assert.equal(state.failureCount, 2)
  assert.deepEqual(waits, [60_000, 300_000])
  assert.deepEqual(backoffs, [1, 2])
  assert.deepEqual(recoveries, [1, 2])
})

test('pressure recovery stops before a backoff would exceed the shop deadline', async () => {
  let slept = false
  await assert.rejects(() => withPressureRecovery(
    async () => { throw new ShopSyncError('verification', 'HTTP 520') },
    {
      deadlineAt: 60_000,
      now: () => 0,
      random: () => 0.5,
      sleep: async () => { slept = true },
    }
  ), /before its deadline/)
  assert.equal(slept, false)
})

test('pressure recovery does not retry unknown validation failures', async () => {
  let attempts = 0
  await assert.rejects(() => withPressureRecovery(async () => {
    attempts += 1
    throw new ShopSyncError('unknown', 'invalid quote')
  }), /invalid quote/)
  assert.equal(attempts, 1)
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

test('parseProxyConfigurations supports six isolated proxy lanes and the legacy variable', () => {
  assert.deepEqual(parseProxyConfigurations(
    'http://proxy-a.internal:17891, http://proxy-b.internal:17892, http://proxy-c.internal:17893, http://proxy-d.internal:17894, http://proxy-e.internal:17895, http://proxy-f.internal:17896',
    'http://ignored.internal:8080'
  ).map((proxy) => proxy.server), [
    'http://proxy-a.internal:17891',
    'http://proxy-b.internal:17892',
    'http://proxy-c.internal:17893',
    'http://proxy-d.internal:17894',
    'http://proxy-e.internal:17895',
    'http://proxy-f.internal:17896',
  ])
  assert.equal(parseProxyConfigurations('', 'http://legacy.internal:8080')[0].server, 'http://legacy.internal:8080')
  assert.deepEqual(parseProxyConfigurations('', ''), [])
  assert.throws(
    () => parseProxyConfigurations('http://a:1,http://b:2,http://c:3,http://d:4,http://e:5,http://f:6,http://g:7'),
    /at most 6 proxies/
  )
})

test('proxyLanesForConcurrency rejects unsafe concurrency fallback to one exit IP', () => {
  const proxies = parseProxyConfigurations('http://proxy-a:17891,http://proxy-b:17892,http://proxy-c:17893,http://proxy-d:17894,http://proxy-e:17895,http://proxy-f:17896')
  for (let concurrency = 1; concurrency <= 6; concurrency += 1) {
    assert.deepEqual(proxyLanesForConcurrency(concurrency, proxies.slice(0, concurrency)), proxies.slice(0, concurrency))
  }
  assert.deepEqual(proxyLanesForConcurrency(1, []), [null])
  assert.throws(() => proxyLanesForConcurrency(6, []), /requires 6/)
  assert.throws(() => proxyLanesForConcurrency(6, proxies.slice(0, 5)), /count must match/)
  assert.throws(() => proxyLanesForConcurrency(6, [proxies[0], proxies[1], proxies[2], proxies[3], proxies[4], proxies[0]]), /must be distinct/)
})

test('proxyPoolsForConcurrency assigns three lane-local fallbacks to six primary lanes', () => {
  const primary = parseProxyConfigurations('http://proxy-a:17891,http://proxy-b:17892,http://proxy-c:17893,http://proxy-d:17894,http://proxy-e:17895,http://proxy-f:17896')
  const fallback = parseProxyConfigurations(
    'http://proxy-g:17897,http://proxy-h:17898,http://proxy-i:17899',
    '',
    'PRODUCT_SYNC_PROXY_FALLBACK_URLS'
  )
  assert.deepEqual(
    proxyPoolsForConcurrency(6, primary, fallback).map((pool) => pool.map((proxy) => proxy.server)),
    [
      ['http://proxy-a:17891', 'http://proxy-g:17897'],
      ['http://proxy-b:17892', 'http://proxy-h:17898'],
      ['http://proxy-c:17893', 'http://proxy-i:17899'],
      ['http://proxy-d:17894'],
      ['http://proxy-e:17895'],
      ['http://proxy-f:17896'],
    ]
  )
  assert.deepEqual(proxyPoolsForConcurrency(1, [], []), [[null]])
  assert.throws(
    () => proxyPoolsForConcurrency(2, primary.slice(0, 2), fallback),
    /cannot exceed/
  )
  assert.throws(
    () => proxyPoolsForConcurrency(6, primary, [fallback[0], fallback[1], primary[0]]),
    /all product sync proxy endpoints must be distinct/
  )
  assert.throws(
    () => proxyPoolsForConcurrency(1, [], fallback.slice(0, 1)),
    /requires PRODUCT_SYNC_PROXY_URLS/
  )
})

test('lane context rotation closes the old context before creating its replacement', async () => {
  const events = []
  const replacement = { id: 'replacement' }
  const result = await closeContextThenCreate({
    close: async () => {
      events.push('close-started')
      await Promise.resolve()
      events.push('close-finished')
    },
  }, async () => {
    events.push('create')
    return replacement
  })
  assert.equal(result, replacement)
  assert.deepEqual(events, ['close-started', 'close-finished', 'create'])
})

test('browserResourceCounts reports six live contexts and one page per lane', () => {
  const contexts = Array.from({ length: 6 }, () => ({
    pages: () => [{ isClosed: () => false }, { isClosed: () => true }],
  }))
  assert.deepEqual(browserResourceCounts(contexts), { contextCount: 6, pageCount: 6 })
})

test('isVerificationPageState detects the Alibaba ESA challenge', () => {
  assert.equal(isVerificationPageState({
    title: 'Verification',
    text: 'Please slide to verify',
    hasCaptcha: true,
  }), true)
  assert.equal(isVerificationPageState({ title: 'Shop', text: 'Products', hasCaptcha: false }), false)
})

test('timing and bounded concurrency configuration are validated', () => {
  assert.equal(parsePositiveMilliseconds(undefined, 20_000, 'TIMEOUT'), 20_000)
  assert.equal(parsePositiveMilliseconds('1500.9', 20_000, 'TIMEOUT'), 1500)
  assert.throws(() => parsePositiveMilliseconds('0', 20_000, 'TIMEOUT'), /positive number/)
  assert.equal(parseSyncConcurrency(undefined), 1)
  assert.equal(parseSyncConcurrency('1'), 1)
  assert.equal(parseSyncConcurrency('2'), 2)
  assert.equal(parseSyncConcurrency('3'), 3)
  assert.equal(parseSyncConcurrency('4'), 4)
  assert.equal(parseSyncConcurrency('5'), 5)
  assert.equal(parseSyncConcurrency('6'), 6)
  assert.throws(() => parseSyncConcurrency('0'), /between 1 and 6/)
  assert.throws(() => parseSyncConcurrency('7'), /between 1 and 6/)
  assert.equal(parseRequestRatePerLane(undefined), 1)
  assert.equal(parseRequestRatePerLane('0.1'), 0.1)
  assert.equal(parseRequestRatePerLane('0.75'), 0.75)
  assert.equal(parseRequestRatePerLane('1'), 1)
  assert.throws(() => parseRequestRatePerLane('0.09'), /between 0.1 and 1/)
  assert.throws(() => parseRequestRatePerLane('1.01'), /between 0.1 and 1/)
})
