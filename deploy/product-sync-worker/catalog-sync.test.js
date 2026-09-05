const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'shop-api.json'), 'utf8'))
const {
  CLOSED_SHOP_RETRY_MILLISECONDS,
  GOODS_TYPES,
  collectAuthoritativeSnapshot,
} = require('./catalog-sync')
const { Semaphore } = require('./worker-utils')

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
      url: 'https://wzyp.cn/item/available-card',
      image: 'https://qn.ldxp.cn/example.png',
      category: 'Accounts',
      goods_type: 'card',
      price: 1.25,
      market_price: 2,
      stock: 8,
      minimum_quantity: 1,
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
    quantity: 1,
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

test('collectAuthoritativeSnapshot publishes an empty snapshot when the shop explicitly does not exist', async () => {
  const calls = []
  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'deleted-shop-token',
    post: async (requestPath, body) => {
      calls.push({ path: requestPath, body })
      return { code: 0, msg: '店铺链接不存在', data: null }
    },
  })

  assert.deepEqual(snapshot, {
    schema_version: 2,
    source_product_count: 0,
    sellable_product_count: 0,
    unavailable_product_count: 0,
    products: [],
  })
  assert.deepEqual(calls, [{
    path: '/shopApi/Shop/info',
    body: { token: 'deleted-shop-token', category_key: null },
  }])
})

test('collectAuthoritativeSnapshot defers a transaction-closed shop without publishing an empty snapshot', async () => {
  const calls = []
  await assert.rejects(async () => collectAuthoritativeSnapshot({
    shopToken: 'closed-shop-token',
    post: async (requestPath, body) => {
      calls.push({ path: requestPath, body })
      return { code: 0, msg: '商家已被关闭交易，有疑问请联系平台客服', data: null }
    },
  }), (error) => {
    assert.equal(error.kind, 'shop_closed')
    assert.equal(error.retryAfterMilliseconds, CLOSED_SHOP_RETRY_MILLISECONDS)
    return true
  })
  assert.deepEqual(calls, [{
    path: '/shopApi/Shop/info',
    body: { token: 'closed-shop-token', category_key: null },
  }])
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

test('collectAuthoritativeSnapshot quotes inventoryless product types with quantity one', async () => {
  const articleList = {
    code: 1,
    data: { total: 1, list: [fixture.inventorylessGoods[0]] },
  }
  const quoteBodies = []
  const snapshot = await collectAuthoritativeSnapshot({
    shopToken: 'shop-token',
    post: async (requestPath, body) => {
      if (requestPath.endsWith('/info')) return fixture.info
      if (requestPath.endsWith('/goodsList')) return goodsListForType(body, { article: articleList })
      if (requestPath.endsWith('/getUserChannel')) return fixture.channels
      if (requestPath.endsWith('/getGoodsPrice')) {
        quoteBodies.push(body)
        return fixture.quote
      }
      throw new Error(`unexpected request ${requestPath}`)
    },
  })

  assert.equal(snapshot.source_product_count, 1)
  assert.equal(snapshot.sellable_product_count, 1)
  assert.equal(snapshot.products[0].stock, 1)
  assert.equal(snapshot.products[0].minimum_quantity, 1)
  assert.deepEqual(quoteBodies, [{
    goods_key: 'paid-article',
    quantity: 1,
    coupon_code: '',
    channel_id: 30,
  }])
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

test('collectAuthoritativeSnapshot processes quotes sequentially', async () => {
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
  assert.equal(maximumActiveQuotes, 1)
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

test('56-shop deterministic load covers 1596 source and 877 sellable products with two global quotes', async () => {
  const shopCount = 56
  const sourceTotal = 1596
  const sellableTotal = 877
  const baseAvailable = fixture.goodsList.data.list[0]
  const baseUnavailable = fixture.goodsList.data.list[1]
  const quoteSemaphore = new Semaphore(2)
  let activeQuotes = 0
  let maximumActiveQuotes = 0

  const sourceCounts = Array.from({ length: shopCount }, (_, index) => (
    Math.floor(sourceTotal / shopCount) + (index < sourceTotal % shopCount ? 1 : 0)
  ))
  const sellableCounts = Array.from({ length: shopCount }, (_, index) => (
    Math.floor(sellableTotal / shopCount) + (index < sellableTotal % shopCount ? 1 : 0)
  ))

  const snapshots = await Promise.all(sourceCounts.map(async (sourceCount, shopIndex) => {
    const sellableCount = sellableCounts[shopIndex]
    const items = Array.from({ length: sourceCount }, (_, productIndex) => {
      const available = productIndex < sellableCount
      const goodsKey = `shop-${shopIndex}-goods-${productIndex}`
      return {
        ...clone(available ? baseAvailable : baseUnavailable),
        goods_key: goodsKey,
        link: `https://pay.ldxp.cn/item/${goodsKey}`,
      }
    })
    return collectAuthoritativeSnapshot({
      shopToken: `shop-${shopIndex}`,
      quoteSemaphore,
      post: async (requestPath, body) => {
        if (requestPath.endsWith('/info')) return fixture.info
        if (requestPath.endsWith('/goodsList')) {
          if (body.goods_type !== 'card') return goodsListForType(body)
          const offset = (body.current - 1) * body.pageSize
          return { code: 1, data: { total: sourceCount, list: items.slice(offset, offset + body.pageSize) } }
        }
        if (requestPath.endsWith('/getUserChannel')) return fixture.channels
        if (requestPath.endsWith('/getGoodsPrice')) {
          activeQuotes += 1
          maximumActiveQuotes = Math.max(maximumActiveQuotes, activeQuotes)
          await new Promise((resolve) => setImmediate(resolve))
          activeQuotes -= 1
          return fixture.quote
        }
        throw new Error(`unexpected request ${requestPath}`)
      },
    })
  }))

  assert.equal(snapshots.reduce((sum, snapshot) => sum + snapshot.source_product_count, 0), sourceTotal)
  assert.equal(snapshots.reduce((sum, snapshot) => sum + snapshot.sellable_product_count, 0), sellableTotal)
  assert.equal(maximumActiveQuotes, 2)
  assert.ok(snapshots.every((snapshot) => snapshot.source_product_count > 0))
})
