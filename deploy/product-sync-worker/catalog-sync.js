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
const crypto = require('node:crypto')
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
  const goodsType = String(item.goods_type || product.goods_type || 'card').trim().toLowerCase()
  const inventoryless = ['article', 'resource', 'equity'].includes(goodsType)
  const merged = {
    ...product,
    ...item,
    goods_key: goodsKey,
    goods_type: goodsType,
    // Details occasionally omit the public link; preserve the cached identity.
    link: item.link || item.url || product.link || product.url,
    // Detail responses and cached jobs use flat inventory fields, while the
    // catalog validator expects extend. Resolve the same quote quantity before
    // validating so valid card details are not rejected before payment lookup.
    extend: {
      ...item.extend,
      stock_count: item.stock ?? item.stock_count ?? item.extend?.stock_count ?? (inventoryless ? 1 : product.stock),
      limit_count: item.minimum_quantity ?? item.limit_count ?? item.extend?.limit_count ?? (inventoryless ? 1 : product.minimum_quantity),
    },
  }
  const state = catalogProductState(merged, goodsType)
  if (state.state === 'unknown') throw new ShopSyncError('unknown', state.reason)
  if (state.state === 'unavailable') return { unavailable: true, goods_key: goodsKey }
  const { stock, minimum_quantity: minimum } = state.product
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
    catalogOnly = false,
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
    if (catalogOnly) return { schema_version: 1, complete: true, closed: true, source_product_count: 0, products: [] }
    const error = new ShopSyncError('shop_closed', String(infoPayload?.msg || 'shop transactions are closed'))
    error.retryAfterMilliseconds = CLOSED_SHOP_RETRY_MILLISECONDS
    throw error
  }
  if (infoPayload?.code !== 1 && shopUnavailableMessage(infoPayload?.msg)) {
    if (catalogOnly) return { schema_version: 1, complete: true, closed: true, source_product_count: 0, products: [] }
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
  const catalog = []
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
          throw new ShopSyncError('catalog_limit', `shop contains more than ${MAX_PRODUCTS} products`)
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
        if (catalogOnly) catalog.push({
          ...(state.product || { goods_key: state.goodsKey }),
          sellable: state.state !== 'unavailable',
          source_version: catalogMetadataVersion(item),
        })
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

  if (catalogOnly) return { schema_version: 1, complete: true, closed: false, source_product_count: sourceProductCount, products: catalog }

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

function catalogMetadataVersion(item) {
  function stable(value) {
    if (Array.isArray(value)) return value.map(stable)
    if (!value || typeof value !== 'object') return value
    return Object.fromEntries(Object.keys(value).sort().map(key => [key, stable(value[key])]))
  }
  // Preserve unknown price conditions in the digest. Cosmetic metadata has no
  // effect on a payment quote; all other source fields are conservative inputs.
  const { name, image, link, category, description, ...conditions } = item
  return crypto.createHash('sha256').update(JSON.stringify(stable(conditions))).digest('hex')
}

async function collectCatalog(options) {
  return collectAuthoritativeSnapshot({ ...options, catalogOnly: true })
}

async function collectQuoteBatch(options) {
  const { shopToken, products, post, signal, quoteSemaphore = new Semaphore(1),
    now = () => new Date(), clock = () => performance.now(), maxSeconds = 30 } = options
  if (!Array.isArray(products) || products.length < 1 || products.length > 20) throw new Error('quote batch must contain 1-20 products')
  const started = clock()
  const results = []
  try {
    const channels = await post('/shopApi/Shop/getUserChannel', { token: shopToken })
    if (channels?.code !== 1 || !Array.isArray(channels.data)) throw new ShopSyncError('unknown', 'payment channel response is invalid')
    const channel = selectPaymentChannel(channels.data)
    if (!channel) throw new ShopSyncError('unknown', 'shop has no valid payment channel')
    for (const product of products) {
      if (clock() - started >= Math.min(30, maxSeconds) * 1000) break
      const payload = await quoteSemaphore.run(() => post('/shopApi/Shop/getGoodsPrice', {
        goods_key: product.goods_key, quantity: product.minimum_quantity, coupon_code: '', channel_id: channel.id,
      }), signal)
      const quote = quoteResult(payload)
      if (quote.state !== 'available' && quote.state !== 'unavailable') throw new ShopSyncError('unknown', quote.reason)
      results.push({ goods_key: product.goods_key, metadata_version: product.metadata_version,
        payable_price: quote.state === 'available' ? quote.totalAmount : null,
        unavailable: quote.state === 'unavailable', quote_verified_at: verifiedAtISO(now) })
    }
    return { products: results }
  } catch (error) {
    // The caller publishes completed results before propagating pressure to the
    // existing circuit/recovery path. Lost leases are never published.
    return { products: results, failure_kind: error.kind || 'unknown', error: String(error.message || error),
      retry_after_seconds: Math.max(0, Math.ceil((error.retryAfterMilliseconds || 0) / 1000)), failure: error }
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
  collectCatalog,
  collectQuoteBatch,
  catalogMetadataVersion,
}
