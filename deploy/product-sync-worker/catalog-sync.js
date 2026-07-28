const {
  Semaphore,
  ShopSyncError,
  catalogProductState,
  mapWithConcurrency,
  normalizeNonNegativeInteger,
  quoteResult,
  selectPaymentChannel,
} = require('./worker-utils')

const PRODUCT_SCHEMA_VERSION = 2
const GOODS_TYPES = ['card', 'article', 'resource', 'equity']
const PAGE_SIZE = 100
const MAX_PRODUCTS = 1000
const QUOTE_CONCURRENCY = 2

function requireSuccessfulPayload(payload, label) {
  if (!payload || payload.code !== 1 || !payload.data || typeof payload.data !== 'object') {
    throw new ShopSyncError('unknown', String(payload?.msg || `${label} response is invalid`))
  }
  return payload.data
}

function catalogCounts(info) {
  const counts = {}
  for (const goodsType of GOODS_TYPES) {
    const value = normalizeNonNegativeInteger(info?.[`${goodsType}_count`])
    if (value === null) {
      throw new ShopSyncError('unknown', `shop info has no valid ${goodsType}_count`)
    }
    counts[goodsType] = value
  }
  const total = Object.values(counts).reduce((sum, value) => sum + value, 0)
  if (total > MAX_PRODUCTS) throw new ShopSyncError('unknown', `shop contains more than ${MAX_PRODUCTS} products`)
  return counts
}

function verifiedAtISO(now) {
  const date = new Date(now())
  if (!Number.isFinite(date.getTime())) throw new ShopSyncError('unknown', 'worker clock is invalid')
  return date.toISOString()
}

async function collectAuthoritativeSnapshot(options) {
  const {
    shopToken,
    post,
    now = () => new Date(),
    quoteSemaphore = new Semaphore(QUOTE_CONCURRENCY),
  } = options || {}
  if (!String(shopToken || '').trim() || typeof post !== 'function') {
    throw new Error('shopToken and post are required')
  }

  const infoPayload = await post('/shopApi/Shop/info', { token: shopToken, category_key: null })
  const info = requireSuccessfulPayload(infoPayload, 'shop info')
  const counts = catalogCounts(info)
  const candidates = []
  let unavailableCount = 0
  const seenGoodsKeys = new Set()

  for (const goodsType of GOODS_TYPES) {
    const expectedCount = counts[goodsType]
    if (expectedCount === 0) continue
    let fetchedCount = 0
    let current = 1
    while (fetchedCount < expectedCount) {
      if (current > Math.ceil(MAX_PRODUCTS / PAGE_SIZE)) {
        throw new ShopSyncError('unknown', `${goodsType} catalog pagination exceeded the product limit`)
      }
      const listPayload = await post('/shopApi/Shop/goodsList', {
        token: shopToken,
        keywords: '',
        category_id: 0,
        goods_type: goodsType,
        current,
        pageSize: PAGE_SIZE,
      })
      const data = requireSuccessfulPayload(listPayload, `${goodsType} catalog`)
      if (!Array.isArray(data.list)) throw new ShopSyncError('unknown', `${goodsType} catalog has no list`)
      const reportedTotal = Number(data.total)
      if (!Number.isInteger(reportedTotal) || reportedTotal !== expectedCount) {
        throw new ShopSyncError('unknown', `${goodsType} catalog count changed during synchronization`)
      }
      if (data.list.length === 0 || data.list.length > PAGE_SIZE || fetchedCount + data.list.length > expectedCount) {
        throw new ShopSyncError('unknown', `${goodsType} catalog pagination is incomplete`)
      }

      for (const item of data.list) {
        const state = catalogProductState(item, goodsType)
        if (state.state === 'unknown') throw new ShopSyncError('unknown', state.reason)
        if (seenGoodsKeys.has(state.goodsKey)) {
          throw new ShopSyncError('unknown', `catalog contains duplicate goods_key ${state.goodsKey}`)
        }
        seenGoodsKeys.add(state.goodsKey)
        if (state.state === 'unavailable') unavailableCount += 1
        else candidates.push(state.product)
      }
      fetchedCount += data.list.length
      current += 1
    }
    if (fetchedCount !== expectedCount) throw new ShopSyncError('unknown', `${goodsType} catalog is incomplete`)
  }

  const sourceProductCount = seenGoodsKeys.size
  const expectedSourceCount = Object.values(counts).reduce((sum, value) => sum + value, 0)
  if (sourceProductCount !== expectedSourceCount) {
    throw new ShopSyncError('unknown', 'shop catalog source count is incomplete')
  }

  if (candidates.length === 0) {
    return {
      schema_version: PRODUCT_SCHEMA_VERSION,
      source_product_count: sourceProductCount,
      sellable_product_count: 0,
      unavailable_product_count: unavailableCount,
      products: [],
    }
  }

  const channelPayload = await post('/shopApi/Shop/getUserChannel', { token: shopToken })
  if (!channelPayload || channelPayload.code !== 1 || !Array.isArray(channelPayload.data)) {
    throw new ShopSyncError('unknown', String(channelPayload?.msg || 'payment channel response is invalid'))
  }
  const channel = selectPaymentChannel(channelPayload.data)
  if (!channel) throw new ShopSyncError('unknown', 'shop has no valid payment channel')

  const quotedProducts = await mapWithConcurrency(candidates, QUOTE_CONCURRENCY, (product) => quoteSemaphore.run(async () => {
    const payload = await post('/shopApi/Shop/getGoodsPrice', {
      goods_key: product.goods_key,
      quantity: product.minimum_quantity,
      coupon_code: '',
      channel_id: channel.id,
    })
    const quote = quoteResult(payload)
    if (quote.state === 'unavailable') {
      unavailableCount += 1
      return null
    }
    if (quote.state !== 'available') throw new ShopSyncError('unknown', quote.reason)
    return {
      ...product,
      payable_price: quote.totalAmount,
      quote_verified_at: verifiedAtISO(now),
    }
  }))
  const products = quotedProducts.filter(Boolean)
  if (products.length + unavailableCount !== sourceProductCount) {
    throw new ShopSyncError('unknown', 'quoted product counts do not cover the complete catalog')
  }
  return {
    schema_version: PRODUCT_SCHEMA_VERSION,
    source_product_count: sourceProductCount,
    sellable_product_count: products.length,
    unavailable_product_count: unavailableCount,
    products,
  }
}

module.exports = {
  GOODS_TYPES,
  MAX_PRODUCTS,
  PRODUCT_SCHEMA_VERSION,
  QUOTE_CONCURRENCY,
  catalogCounts,
  collectAuthoritativeSnapshot,
}
