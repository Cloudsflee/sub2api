import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

const productNameCollator = new Intl.Collator(undefined, { numeric: true })

export type LivePublicProductAvailability = 'available' | 'unavailable' | 'unknown'

function normalizeSearchText(value: string | undefined): string {
  return (value || '')
    .normalize('NFKC')
    .toLowerCase()
    .replace(/\s+/g, ' ')
    .trim()
}

function normalizeMinimumQuantity(value: unknown): number {
  const parsed = normalizeAPINumber(value)
  return parsed !== null && Number.isInteger(parsed) && parsed > 0 ? parsed : 1
}

function normalizeAPINumber(value: unknown): number | null {
  if (value === null || value === undefined || typeof value === 'boolean') return null
  if (typeof value === 'string' && value.trim() === '') return null
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : null
}

function normalizeLiveStatus(value: unknown): LivePublicProductAvailability {
  if (value === true || value === 1 || value === '1') return 'available'
  if (value === false || value === 0 || value === '0') return 'unavailable'
  const normalized = String(value ?? '').trim().toLocaleLowerCase()
  if (['active', 'enabled', 'on_sale', 'onsale', 'selling'].includes(normalized)) return 'available'
  if (['inactive', 'disabled', 'off_sale', 'offsale', 'sold_out', 'deleted'].includes(normalized)) return 'unavailable'
  return 'unknown'
}

export function publicProductMinimumQuantity(product: PublicAccountImportProduct): number {
  return normalizeMinimumQuantity(product.minimum_quantity)
}

export function publicProductPayablePrice(product: PublicAccountImportProduct): number {
  const payablePrice = normalizeAPINumber(product.payable_price)
  return payablePrice !== null && payablePrice >= 0 ? payablePrice : product.price
}

export function publicProductUnitPrice(product: PublicAccountImportProduct): number {
  const unitPrice = normalizeAPINumber(product.unit_price)
  if (unitPrice !== null && unitPrice >= 0) return unitPrice
  const payablePrice = normalizeAPINumber(product.payable_price)
  if (payablePrice !== null && payablePrice >= 0) {
    return payablePrice / publicProductMinimumQuantity(product)
  }
  return product.price
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
	priceOrder: 'desc' | 'asc'
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
		const leftPrice = publicProductUnitPrice(left)
		const rightPrice = publicProductUnitPrice(right)
    const priceDifference = priceOrder === 'desc' ? rightPrice - leftPrice : leftPrice - rightPrice
    if (priceDifference !== 0) return priceDifference

    const nameDifference = productNameCollator.compare(left.name, right.name)
    if (nameDifference !== 0) return nameDifference
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
  })

  return matchedProducts
}

export function livePublicProductAvailability(
	response: any
): LivePublicProductAvailability {
  const data = response?.data
  if (response?.code === 1 && data) {
    const rawStatus = data.status
		if (rawStatus === undefined || rawStatus === null || rawStatus === '') return 'unknown'
		const status = normalizeLiveStatus(rawStatus)
		if (status !== 'available') return status

    const rawStock = data.extend?.stock_count
		const rawMinimumQuantity = data.extend?.limit_count
		const stock = normalizeAPINumber(rawStock)
		const minimumQuantity = normalizeAPINumber(rawMinimumQuantity)
		if (stock === null || minimumQuantity === null || !Number.isInteger(stock) || stock < 0
			|| !Number.isInteger(minimumQuantity) || minimumQuantity < 1) return 'unknown'
		if (stock < minimumQuantity) return 'unavailable'
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

export function livePublicProductMinimumQuantity(data: any): number | null {
  const stock = normalizeAPINumber(data?.extend?.stock_count)
  const minimumQuantity = normalizeAPINumber(data?.extend?.limit_count)
  if (stock === null || minimumQuantity === null || !Number.isInteger(stock) || stock < 0
    || !Number.isInteger(minimumQuantity) || minimumQuantity < 1 || stock < minimumQuantity) {
    return null
  }
  return minimumQuantity
}

export function livePublicProductQuoteAvailability(response: any): LivePublicProductAvailability {
  if (response?.code !== 1) {
    return livePublicProductAvailability(response)
  }
  if (!response.data || typeof response.data !== 'object') return 'unknown'
  if (response.data.status !== undefined) {
    const status = normalizeLiveStatus(response.data.status)
    if (status !== 'available') return status
  }
  const totalAmount = normalizeAPINumber(response.data.total_amount)
  return totalAmount !== null && totalAmount >= 0 ? 'available' : 'unknown'
}

export function selectLivePublicProductPaymentChannel(channels: any[]): any | null {
  if (!Array.isArray(channels)) return null
  const valid = channels.filter((channel) => {
    if (!channel || (typeof channel.id !== 'number' && typeof channel.id !== 'string')) return false
    if (String(channel.id).trim() === '' || channel.enabled === false || channel.disabled === true) return false
    return channel.status === undefined || normalizeLiveStatus(channel.status) === 'available'
  })
  return valid.find((channel) => (
    channel.is_default === true || Number(channel.is_default) === 1
    || channel.default === true || Number(channel.default) === 1
    || channel.isDefault === true || channel.selected === true
  )) || valid[0] || null
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
