const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'shop-api.json'), 'utf8'))
const { catalogCounts, collectAuthoritativeSnapshot } = require('./catalog-sync')

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

test('catalogCounts rejects blank or null counts instead of publishing an empty shop', () => {
  assert.throws(() => catalogCounts({ card_count: null, article_count: 0, resource_count: 0, equity_count: 0 }), /card_count/)
  assert.throws(() => catalogCounts({ card_count: '', article_count: 0, resource_count: 0, equity_count: 0 }), /card_count/)
  assert.deepEqual(catalogCounts({ card_count: '0', article_count: 0, resource_count: 0, equity_count: 0 }), {
    card: 0,
    article: 0,
    resource: 0,
    equity: 0,
  })
})

test('collectAuthoritativeSnapshot publishes one complete verified shop snapshot', async () => {
  const calls = []
  const post = async (requestPath, body) => {
    calls.push({ path: requestPath, body })
    if (requestPath.endsWith('/info')) return clone(fixture.info)
    if (requestPath.endsWith('/goodsList')) return clone(fixture.goodsList)
    if (requestPath.endsWith('/getUserChannel')) return clone(fixture.channels)
    if (requestPath.endsWith('/getGoodsPrice')) return clone(fixture.quote)
    throw new Error(`unexpected request ${requestPath}`)
  }

  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post,
    now: () => new Date('2026-07-28T02:00:00Z'),
  })

  assert.deepEqual(snapshot, {
    schema_version: 2,
    source_product_count: 3,
    sellable_product_count: 1,
    unavailable_product_count: 2,
    products: [{
      goods_key: 'available-card',
      name: 'Available card',
      url: 'https://pay.ldxp.cn/item/available-card',
      image: 'https://qn.ldxp.cn/example.png',
      category: 'Accounts',
      goods_type: 'card',
      price: 1.25,
      market_price: 2,
      stock: 8,
      minimum_quantity: 2,
      payable_price: 2.2,
      quote_verified_at: '2026-07-28T02:00:00.000Z',
    }],
  })
  assert.equal(calls.filter(({ path: value }) => value.endsWith('/getUserChannel')).length, 1)
  assert.deepEqual(calls.find(({ path: value }) => value.endsWith('/getGoodsPrice')).body, {
    goods_key: 'available-card',
    quantity: 2,
    coupon_code: '',
    channel_id: 30,
  })
})

test('collectAuthoritativeSnapshot accepts an explicitly empty or entirely unavailable shop', async () => {
  const emptyInfo = clone(fixture.info)
  emptyInfo.data.card_count = 0
  const empty = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => {
      assert.ok(requestPath.endsWith('/info'))
      return emptyInfo
    },
  })
  assert.deepEqual(empty, {
    schema_version: 2,
    source_product_count: 0,
    sellable_product_count: 0,
    unavailable_product_count: 0,
    products: [],
  })

  const unavailableInfo = clone(fixture.info)
  unavailableInfo.data.card_count = 1
  const unavailableList = clone(fixture.goodsList)
  unavailableList.data.total = 1
  unavailableList.data.list = [fixture.goodsList.data.list[1]]
  const unavailable = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => requestPath.endsWith('/info') ? unavailableInfo : unavailableList,
  })
  assert.equal(unavailable.source_product_count, 1)
  assert.equal(unavailable.unavailable_product_count, 1)
  assert.deepEqual(unavailable.products, [])
})

test('collectAuthoritativeSnapshot treats an explicit quote rejection as unavailable', async () => {
  const info = clone(fixture.info)
  info.data.card_count = 1
  const list = clone(fixture.goodsList)
  list.data.total = 1
  list.data.list = [fixture.goodsList.data.list[0]]
  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => {
      if (requestPath.endsWith('/info')) return info
      if (requestPath.endsWith('/goodsList')) return list
      if (requestPath.endsWith('/getUserChannel')) return fixture.channels
      return fixture.soldOutQuote
    },
  })
  assert.equal(snapshot.sellable_product_count, 0)
  assert.equal(snapshot.unavailable_product_count, 1)
  assert.deepEqual(snapshot.products, [])
})

test('collectAuthoritativeSnapshot aborts the whole shop on missing fields and unknown quotes', async () => {
  const missingStatusList = clone(fixture.goodsList)
  delete missingStatusList.data.list[0].status
  await assert.rejects(() => collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => requestPath.endsWith('/info') ? fixture.info : missingStatusList,
  }), /valid status/)

  const info = clone(fixture.info)
  info.data.card_count = 1
  const list = clone(fixture.goodsList)
  list.data.total = 1
  list.data.list = [fixture.goodsList.data.list[0]]
  await assert.rejects(() => collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => {
      if (requestPath.endsWith('/info')) return info
      if (requestPath.endsWith('/goodsList')) return list
      if (requestPath.endsWith('/getUserChannel')) return fixture.channels
      return { code: 1, data: {} }
    },
  }), /total_amount/)
})

test('collectAuthoritativeSnapshot limits quote concurrency to two', async () => {
  const count = 6
  const info = clone(fixture.info)
  info.data.card_count = count
  const base = fixture.goodsList.data.list[0]
  const list = clone(fixture.goodsList)
  list.data.total = count
  list.data.list = Array.from({ length: count }, (_, index) => ({
    ...clone(base),
    goods_key: `goods-${index}`,
    link: `https://pay.ldxp.cn/item/goods-${index}`,
  }))
  let activeQuotes = 0
  let maximumActiveQuotes = 0
  let channelCalls = 0
  await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath) => {
      if (requestPath.endsWith('/info')) return info
      if (requestPath.endsWith('/goodsList')) return list
      if (requestPath.endsWith('/getUserChannel')) {
        channelCalls += 1
        return fixture.channels
      }
      activeQuotes += 1
      maximumActiveQuotes = Math.max(maximumActiveQuotes, activeQuotes)
      await new Promise((resolve) => setTimeout(resolve, 2))
      activeQuotes -= 1
      return fixture.quote
    },
  })
  assert.equal(channelCalls, 1)
  assert.equal(maximumActiveQuotes, 2)
})
