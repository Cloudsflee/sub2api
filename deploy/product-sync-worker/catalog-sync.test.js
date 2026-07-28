const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'shop-api.json'), 'utf8'))
const { GOODS_TYPES, collectAuthoritativeSnapshot } = require('./catalog-sync')

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

function goodsListForType(body, lists = {}) {
  return clone(lists[body.goods_type] || fixture.emptyGoodsList)
}

test('collectAuthoritativeSnapshot uses goodsList totals and publishes one complete verified shop snapshot', async () => {
  const calls = []
  const mismatchedInfo = clone(fixture.info)
  mismatchedInfo.data.card_count = 4
  const post = async (requestPath, body) => {
    calls.push({ path: requestPath, body })
    if (requestPath.endsWith('/info')) return mismatchedInfo
    if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: fixture.goodsList })
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
  assert.deepEqual(
    calls.filter(({ path: value }) => value.endsWith('/goodsList')).map(({ body }) => body.goods_type),
    GOODS_TYPES
  )
  assert.deepEqual(calls.find(({ path: value }) => value.endsWith('/getGoodsPrice')).body, {
    goods_key: 'available-card',
    quantity: 2,
    coupon_code: '',
    channel_id: 30,
  })
})

test('collectAuthoritativeSnapshot accepts an explicitly empty or entirely unavailable shop', async () => {
  const empty = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return { code: 1, data: {} }
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body)
      throw new Error(`unexpected request ${requestPath}`)
    },
  })
  assert.deepEqual(empty, {
    schema_version: 2,
    source_product_count: 0,
    sellable_product_count: 0,
    unavailable_product_count: 0,
    products: [],
  })

  const unavailableList = clone(fixture.goodsList)
  unavailableList.data.total = 1
  unavailableList.data.list = [fixture.goodsList.data.list[1]]
  const unavailable = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: unavailableList })
      throw new Error(`unexpected request ${requestPath}`)
    },
  })
  assert.equal(unavailable.source_product_count, 1)
  assert.equal(unavailable.unavailable_product_count, 1)
  assert.deepEqual(unavailable.products, [])
})

test('collectAuthoritativeSnapshot treats an explicit quote rejection as unavailable', async () => {
  const list = clone(fixture.goodsList)
  list.data.total = 1
  list.data.list = [fixture.goodsList.data.list[0]]
  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: list })
      if (requestPath.endsWith('/getUserChannel')) return fixture.channels
      return fixture.soldOutQuote
    },
  })
  assert.equal(snapshot.sellable_product_count, 0)
  assert.equal(snapshot.unavailable_product_count, 1)
  assert.deepEqual(snapshot.products, [])
})

test('collectAuthoritativeSnapshot aborts the whole shop on invalid explicit status and unknown quotes', async () => {
  const invalidStatusList = clone(fixture.goodsList)
  invalidStatusList.data.list[0].status = ''
  await assert.rejects(() => collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: invalidStatusList })
      throw new Error(`unexpected request ${requestPath}`)
    },
  }), /valid status/)

  const list = clone(fixture.goodsList)
  list.data.total = 1
  list.data.list = [fixture.goodsList.data.list[0]]
  await assert.rejects(() => collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: list })
      if (requestPath.endsWith('/getUserChannel')) return fixture.channels
      return { code: 1, data: {} }
    },
  }), /total_amount/)
})

test('collectAuthoritativeSnapshot limits quote concurrency to two', async () => {
  const count = 6
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
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { card: list })
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

test('collectAuthoritativeSnapshot fully paginates stable list totals', async () => {
  const total = 101
  const base = fixture.goodsList.data.list[1]
  const items = Array.from({ length: total }, (_, index) => ({
    ...clone(base),
    goods_key: `sold-out-${index}`,
    link: `https://pay.ldxp.cn/item/sold-out-${index}`,
  }))
  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (!requestPath.endsWith('/goodsList')) throw new Error(`unexpected request ${requestPath}`)
      if (body.goods_type !== 'card') return goodsListForType(body)
      const offset = (body.current - 1) * body.pageSize
      return { code: 1, data: { total, list: items.slice(offset, offset + body.pageSize) } }
    },
  })
  assert.deepEqual(snapshot, {
    schema_version: 2,
    source_product_count: total,
    sellable_product_count: 0,
    unavailable_product_count: total,
    products: [],
  })
})

test('collectAuthoritativeSnapshot rejects a list total that changes during pagination', async () => {
  const base = fixture.goodsList.data.list[1]
  const firstPage = Array.from({ length: 100 }, (_, index) => ({
    ...clone(base),
    goods_key: `sold-out-${index}`,
    link: `https://pay.ldxp.cn/item/sold-out-${index}`,
  }))
  await assert.rejects(() => collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (body.goods_type !== 'card') return goodsListForType(body)
      if (body.current === 1) return { code: 1, data: { total: 101, list: firstPage } }
      return { code: 1, data: { total: 100, list: [] } }
    },
  }), /count changed during synchronization/)
})
