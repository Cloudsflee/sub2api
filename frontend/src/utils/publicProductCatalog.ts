import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

const productNameCollator = new Intl.Collator(undefined, { numeric: true })

export interface LivePublicProductSnapshot {
  price: number
  marketPrice?: number
  stock: number
  minimumQuantity: number
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

function normalizeMinimumQuantity(value: unknown): number {
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : 1
}

export function publicProductMinimumQuantity(product: PublicAccountImportProduct): number {
  return normalizeMinimumQuantity(product.minimum_quantity)
}

function publicProductNameMatches(
  product: PublicAccountImportProduct,
  includedKeywords: string[],
  excludedKeywords: string[]
): boolean {
  const name = normalizeSearchText(product.name)
  return includedKeywords.every((keyword) => name.includes(keyword))
    && excludedKeywords.every((keyword) => !name.includes(keyword))
}

export function filterAndSortPublicProducts(
  products: PublicAccountImportProduct[],
  query: string,
  priceOrder: 'desc' | 'asc',
  sortPrices: ReadonlyMap<string, number>
): PublicAccountImportProduct[] {
  const normalizedQuery = normalizeSearchText(query)
  const includedKeywords = new Set<string>()
  const excludedKeywords = new Set<string>()
  for (const keyword of normalizedQuery.split(' ').filter(Boolean)) {
    if (keyword.startsWith('-') && keyword.length > 1) {
      excludedKeywords.add(keyword.slice(1))
    } else if (keyword !== '-') {
      includedKeywords.add(keyword)
    }
  }

  const included = [...includedKeywords]
  const excluded = [...excludedKeywords]
  const matchedProducts = products.filter((product) => product.stock >= publicProductMinimumQuantity(product)
    && publicProductNameMatches(product, included, excluded))

  matchedProducts.sort((left, right) => {
    const leftPrice = sortPrices.get(left.id) ?? left.price
    const rightPrice = sortPrices.get(right.id) ?? right.price
    const priceDifference = priceOrder === 'desc' ? rightPrice - leftPrice : leftPrice - rightPrice
    if (priceDifference !== 0) return priceDifference

    const nameDifference = productNameCollator.compare(left.name, right.name)
    if (nameDifference !== 0) return nameDifference
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
  })

  return matchedProducts
}

export function livePublicProductAvailability(
  response: any,
  fallbackStock?: number
): LivePublicProductAvailability {
  const data = response?.data
  if (response?.code === 1 && data) {
    const rawStatus = data.status
    if (rawStatus !== undefined && rawStatus !== null && Number(rawStatus) !== 1) return 'unavailable'

    const rawStock = data.extend?.stock_count
    let stock = Number(fallbackStock)
    if (rawStock !== undefined && rawStock !== null && rawStock !== '') {
      stock = Number(rawStock)
    }
    const minimumQuantity = normalizeMinimumQuantity(data.extend?.limit_count)
    if (Number.isFinite(stock) && stock < minimumQuantity) return 'unavailable'
    return 'available'
  }

  const message = String(response?.msg || '').toLocaleLowerCase()
  const explicitlyUnavailable = /商品.*(?:不存在|未上架|下架|售罄|删除)/.test(message)
    || /(?:库存|存货).*(?:不足|不够|售罄|无货)/.test(message)
    || /(?:低于|小于).*成本价.*(?:无法|不能|不可)?.*购买/.test(message)
    || /(?:无法|不能|不可).*购买.*(?:库存|成本价)/.test(message)
    || /(?:product|goods).*(?:not found|unavailable|off shelf|sold out|deleted)/.test(message)
    || /(?:insufficient|not enough|out of) stock/.test(message)
    || /(?:below|lower than).*(?:cost|cost price)/.test(message)
  return response?.code === 0 && explicitlyUnavailable ? 'unavailable' : 'unknown'
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
  const minimumQuantity = normalizeMinimumQuantity(
    data.extend?.limit_count ?? product.minimum_quantity
  )
  if (!Number.isFinite(price) || price < 0 || price > 1_000_000 || stock < minimumQuantity) return null

  const marketPrice = Number.isFinite(rawMarketPrice) && rawMarketPrice >= 0 && rawMarketPrice <= 1_000_000
    ? rawMarketPrice
    : product.market_price
  return {
    price,
    marketPrice,
    stock,
    minimumQuantity,
    updatedAt: verifiedAt.toISOString(),
  }
}
