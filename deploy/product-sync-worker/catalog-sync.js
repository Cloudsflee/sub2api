const {
  Semaphore,
  ShopSyncError,
  catalogProductState,
  mapWithConcurrency,
  normalizeNonNegativeInteger,
  quoteResult,
  selectPaymentChannel,
  shopClosedForTransactionsMessage,
  shopUnavailableMessage,
} = require('./worker-utils')

const PRODUCT_SCHEMA_VERSION = 2
const GOODS_TYPES = ['card', 'article', 'resource', 'equity']
const PAGE_SIZE = 100
const MAX_PRODUCTS = 1000
const QUOTE_CONCURRENCY = 1
const CLOSED_SHOP_RETRY_MILLISECONDS = 60 * 60_000

async function collectProductQuote(options) {
  const { shopToken, goodsKey, product = {}, post, now = () => new Date(), quoteSemaphore = new Semaphore(1), signal } = options || {}
  if (!shopToken || !goodsKey || typeof post !== 'function') throw new Error('shopToken, goodsKey and post are required')
  const detailPayload = await post('/shopApi/Shop/goodsInfo', { goods_key: goodsKey, trade_no: '' })
  const data = requireSuccessfulPayload(detailPayload, 'goods info')
  const item = data.goods || data.item || data.product || data.goods_info || data
  const detailGoodsKey = String(item.goods_key || data.goods_key || '').trim()
  if (detailGoodsKey && detailGoodsKey !== String(goodsKey).trim()) {
    throw new ShopSyncError('unknown', 'goods info identity does not match the requested product')
  }
  const merged = {
    ...product,
    ...item,
    goods_key: goodsKey,
    // Details occasionally omit the public link; preserve the cached identity.
    link: item.link || item.url || product.link || product.url,
  }
  const state = catalogProductState(merged, product.goods_type || item.goods_type || 'card')
  if (state.state === 'unknown') throw new ShopSyncError('unknown', state.reason)
  if (state.state === 'unavailable') return { unavailable: true, goods_key: goodsKey }
  const goodsType = state.product.goods_type || product.goods_type || 'card'
  const inventoryless = ['article', 'resource', 'equity'].includes(goodsType)
  const detailMinimum = item.minimum_quantity ?? item.limit_count ?? item.extend?.limit_count
  const detailStock = item.stock ?? item.stock_count ?? item.extend?.stock_count
  const minimumRaw = detailMinimum ?? (inventoryless ? 1 : product.minimum_quantity)
  const parsedMinimum = minimumRaw == null || minimumRaw === '' ? 1 : normalizeNonNegativeInteger(minimumRaw)
  if (parsedMinimum === null) throw new ShopSyncError('unknown', 'goods info minimum quantity is invalid')
  const minimum = Math.max(parsedMinimum, 1)
  const stockRaw = detailStock ?? (inventoryless ? 1 : product.stock)
  const stock = stockRaw == null || stockRaw === '' ? 1 : normalizeNonNegativeInteger(stockRaw)
  if (stock === null) throw new ShopSyncError('unknown', 'goods info stock is invalid')
  if (stock < minimum) return { unavailable: true, goods_key: goodsKey }
  const token = String(
    data.user?.token || data.shop?.token || data.user_token || data.shop_token || data.token
      || item.user?.token || item.shop?.token || item.shop_token || item.token || ''
  ).trim()
  if (!token) throw new ShopSyncError('unknown', 'goods info token is missing')
  const channelsPayload = await post('/shopApi/Shop/getUserChannel', { token })
  if (!channelsPayload || channelsPayload.code !== 1 || !Array.isArray(channelsPayload.data)) throw new ShopSyncError('unknown', 'payment channel response is invalid')
  const channel = selectPaymentChannel(channelsPayload.data); if (!channel) throw new ShopSyncError('unknown', 'shop has no valid payment channel')
  const quotePayload = await quoteSemaphore.run(() => post('/shopApi/Shop/getGoodsPrice', { goods_key: goodsKey, quantity: minimum, coupon_code: '', channel_id: channel.id }), signal)
  const quote = quoteResult(quotePayload)
  if (quote.state === 'unavailable') return { unavailable: true, goods_key: goodsKey }
  if (quote.state !== 'available') throw new ShopSyncError('unknown', quote.reason)
  const total = Number(quote.totalAmount); if (!Number.isFinite(total) || total < 0) throw new ShopSyncError('unknown', 'quote total is invalid')
  return {
    ...state.product,
    goods_key: goodsKey,
    goods_type: goodsType,
    stock,
    minimum_quantity: minimum,
    payable_price: total,
    quote_verified_at: verifiedAtISO(now),
  }
}

function requireSuccessfulPayload(payload, label) {
  if (!payload || payload.code !== 1 || !payload.data || typeof payload.data !== 'object') {
    throw new ShopSyncError('unknown', String(payload?.msg || `${label} response is invalid`))
  }
  return payload.data
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
    signal,
  } = options || {}
  if (!String(shopToken || '').trim() || typeof post !== 'function') {
    throw new Error('shopToken and post are required')
  }

  const infoPayload = await post('/shopApi/Shop/info', { token: shopToken, category_key: null })
  if (infoPayload?.code !== 1 && shopClosedForTransactionsMessage(infoPayload?.msg)) {
    const error = new ShopSyncError('shop_closed', String(infoPayload?.msg || 'shop transactions are closed'))
    error.retryAfterMilliseconds = CLOSED_SHOP_RETRY_MILLISECONDS
    throw error
  }
  if (infoPayload?.code !== 1 && shopUnavailableMessage(infoPayload?.msg)) {
    return {
      schema_version: PRODUCT_SCHEMA_VERSION,
      source_product_count: 0,
      sellable_product_count: 0,
      unavailable_product_count: 0,
      products: [],
    }
  }
  requireSuccessfulPayload(infoPayload, 'shop info')
  const candidates = []
  let unavailableCount = 0
  let sourceProductCount = 0
  const seenGoodsKeys = new Set()

  for (const goodsType of GOODS_TYPES) {
    let fetchedCount = 0
    let current = 1
    let expectedCount = null
    do {
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
      const reportedTotal = normalizeNonNegativeInteger(data.total)
      if (reportedTotal === null) {
        throw new ShopSyncError('unknown', `${goodsType} catalog has no valid total`)
      }
      if (expectedCount === null) {
        expectedCount = reportedTotal
        sourceProductCount += expectedCount
        if (sourceProductCount > MAX_PRODUCTS) {
          throw new ShopSyncError('unknown', `shop contains more than ${MAX_PRODUCTS} products`)
        }
      } else if (reportedTotal !== expectedCount) {
        throw new ShopSyncError('unknown', `${goodsType} catalog count changed during synchronization`)
      }
      const remainingCount = expectedCount - fetchedCount
      if (data.list.length > PAGE_SIZE || data.list.length > remainingCount || (remainingCount > 0 && data.list.length === 0)) {
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
    } while (fetchedCount < expectedCount)
    if (fetchedCount !== expectedCount) throw new ShopSyncError('unknown', `${goodsType} catalog is incomplete`)
  }

  if (seenGoodsKeys.size !== sourceProductCount) {
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
  }, signal))
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
  CLOSED_SHOP_RETRY_MILLISECONDS,
  MAX_PRODUCTS,
  PRODUCT_SCHEMA_VERSION,
  QUOTE_CONCURRENCY,
  collectAuthoritativeSnapshot,
  collectProductQuote,
}
