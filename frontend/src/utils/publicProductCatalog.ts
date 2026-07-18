import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

const PRODUCT_TYPE_ALIASES: Readonly<Record<string, string>> = {
  card: '卡密',
  article: '文章',
  resource: '资源',
  equity: '权益',
}

const productNameCollator = new Intl.Collator(undefined, { numeric: true })

interface ScoredPublicProduct {
  product: PublicAccountImportProduct
  relevance: number
}

export interface LivePublicProductSnapshot {
  price: number
  marketPrice?: number
  stock: number
  updatedAt: string
}

export type LivePublicProductAvailability = 'available' | 'unavailable' | 'unknown'

function normalizeSearchText(value: string | undefined): string {
  return (value || '')
    .normalize('NFKC')
    .toLowerCase()
    .replace(/\s+/g, ' ')
    .trim()
}

function textMatchScore(
  value: string,
  keyword: string,
  exactScore: number,
  prefixScore: number,
  substringScore: number
): number {
  if (!value.includes(keyword)) return 0
  if (value === keyword) return exactScore
  return value.startsWith(keyword) ? prefixScore : substringScore
}

function publicProductRelevance(
  product: PublicAccountImportProduct,
  normalizedQuery: string,
  keywords: string[]
): number | null {
  const name = normalizeSearchText(product.name)
  const shopName = normalizeSearchText(product.shop_name)
  const category = normalizeSearchText(product.category)
  const goodsType = normalizeSearchText(product.goods_type)
  const goodsTypeAlias = PRODUCT_TYPE_ALIASES[goodsType] || ''
  let relevance = 0

  for (const keyword of keywords) {
    const bestKeywordScore = Math.max(
      textMatchScore(name, keyword, 500, 400, 300),
      textMatchScore(shopName, keyword, 240, 200, 160),
      textMatchScore(category, keyword, 140, 110, 80),
      textMatchScore(goodsType, keyword, 60, 40, 20),
      textMatchScore(goodsTypeAlias, keyword, 60, 40, 20)
    )
    if (bestKeywordScore === 0) return null
    relevance += bestKeywordScore
  }

  return relevance + textMatchScore(name, normalizedQuery, 10_000, 8_000, 6_000)
}

export function filterAndSortPublicProducts(
  products: PublicAccountImportProduct[],
  query: string,
  priceOrder: 'desc' | 'asc',
  sortPrices: ReadonlyMap<string, number>
): PublicAccountImportProduct[] {
  const normalizedQuery = normalizeSearchText(query)
  const keywords = [...new Set(normalizedQuery.split(' ').filter(Boolean))]
  const matchedProducts: ScoredPublicProduct[] = []

  for (const product of products) {
    if (product.stock <= 0) continue
    const relevance = normalizedQuery
      ? publicProductRelevance(product, normalizedQuery, keywords)
      : 0
    if (relevance !== null) matchedProducts.push({ product, relevance })
  }

  matchedProducts.sort((left, right) => {
    if (normalizedQuery) {
      const relevanceDifference = right.relevance - left.relevance
      if (relevanceDifference !== 0) return relevanceDifference
    }

    const leftPrice = sortPrices.get(left.product.id) ?? left.product.price
    const rightPrice = sortPrices.get(right.product.id) ?? right.product.price
    const priceDifference = priceOrder === 'desc' ? rightPrice - leftPrice : leftPrice - rightPrice
    if (priceDifference !== 0) return priceDifference

    const nameDifference = productNameCollator.compare(left.product.name, right.product.name)
    if (nameDifference !== 0) return nameDifference
    return left.product.id < right.product.id ? -1 : left.product.id > right.product.id ? 1 : 0
  })

  return matchedProducts.map(({ product }) => product)
}

export function livePublicProductAvailability(response: any): LivePublicProductAvailability {
  const data = response?.data
  if (response?.code === 1 && data) {
    const rawStatus = data.status
    if (rawStatus !== undefined && rawStatus !== null && Number(rawStatus) !== 1) return 'unavailable'

    const rawStock = data.extend?.stock_count
    if (rawStock !== undefined && rawStock !== null && rawStock !== '') {
      const stock = Number(rawStock)
      if (Number.isFinite(stock) && stock <= 0) return 'unavailable'
    }
    return 'available'
  }

  const message = String(response?.msg || '').toLocaleLowerCase()
  const explicitlyUnavailable = /商品.*(?:不存在|未上架|下架|售罄|删除)/.test(message)
    || /(?:product|goods).*(?:not found|unavailable|off shelf|sold out|deleted)/.test(message)
  return response?.code === 0 && data == null && explicitlyUnavailable ? 'unavailable' : 'unknown'
}

export function publicProductGoodsKey(productURL: string): string {
  try {
    const parsed = new URL(productURL)
    const match = parsed.hostname.toLocaleLowerCase() === 'pay.ldxp.cn'
      ? parsed.pathname.match(/^\/item\/([^/]+)\/?$/)
      : null
    return match ? decodeURIComponent(match[1]) : ''
  } catch {
    return ''
  }
}

export function normalizeLivePublicProduct(
  product: PublicAccountImportProduct,
  data: any,
  verifiedAt = new Date()
): LivePublicProductSnapshot | null {
  if (!data || data.price === undefined || data.price === null) return null
  const price = Number(data.price)
  const rawMarketPrice = Number(data.market_price)
  const rawStock = data.extend?.stock_count
  const reportedStock = rawStock === undefined || rawStock === null || rawStock === ''
    ? Number.NaN
    : Number(rawStock)
  const stock = Number.isFinite(reportedStock) ? reportedStock : product.stock
  if (!Number.isFinite(price) || price < 0 || price > 1_000_000 || stock <= 0) return null

  const marketPrice = Number.isFinite(rawMarketPrice) && rawMarketPrice >= 0 && rawMarketPrice <= 1_000_000
    ? rawMarketPrice
    : product.market_price
  return {
    price,
    marketPrice,
    stock,
    updatedAt: verifiedAt.toISOString(),
  }
}
