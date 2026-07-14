import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

export interface LivePublicProductSnapshot {
  price: number
  marketPrice?: number
  stock: number
  updatedAt: string
}

export type LivePublicProductAvailability = 'available' | 'unavailable' | 'unknown'

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
