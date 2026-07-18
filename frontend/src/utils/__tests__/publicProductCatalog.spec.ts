import { describe, expect, it } from 'vitest'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import {
  filterAndSortPublicProducts,
  livePublicProductAvailability,
  normalizeLivePublicProduct,
  publicProductGoodsKey,
} from '../publicProductCatalog'

const product: PublicAccountImportProduct = {
  id: 'product-1',
  shop_id: 'shop-1',
  shop_name: 'Shop',
  shop_url: 'https://pay.ldxp.cn/shop/example',
  name: 'Product',
  url: 'https://pay.ldxp.cn/item/abc123',
  goods_type: 'card',
  price: 1.3,
  market_price: 2,
  stock: 8,
  updated_at: '2026-07-14T09:00:00Z',
}

function createProduct(
  id: string,
  overrides: Partial<PublicAccountImportProduct> = {}
): PublicAccountImportProduct {
  return {
    ...product,
    id,
    name: `Product ${id}`,
    url: `https://pay.ldxp.cn/item/${id}`,
    ...overrides,
  }
}

describe('filterAndSortPublicProducts', () => {
  const matchingCatalog = [
    createProduct('product-name', {
      name: 'Alpha Cloud Bundle',
      shop_name: 'Cloud Store',
      category: 'Developer Tools',
      goods_type: 'card',
    }),
    createProduct('shop-only', {
      name: 'Alpha Basic',
      shop_name: 'Cloud Store',
    }),
    createProduct('metadata-only', {
      name: 'Budget Account',
      shop_name: 'K12 Shop',
      category: 'K12',
      goods_type: 'k12',
    }),
  ]

  it.each([
    { label: 'matches multiple keywords in the product name', query: 'alpha cloud', expected: ['product-name'] },
    { label: 'normalizes letter case', query: 'ALPHA CLOUD', expected: ['product-name'] },
    { label: 'normalizes full-width characters', query: 'ＡＬＰＨＡ　ＣＬＯＵＤ', expected: ['product-name'] },
    { label: 'collapses consecutive whitespace', query: '  alpha \t  cloud  ', expected: ['product-name'] },
    { label: 'deduplicates keywords', query: 'alpha alpha cloud', expected: ['product-name'] },
    { label: 'requires every keyword to match', query: 'alpha missing', expected: [] },
  ])('$label', ({ query, expected }) => {
    const result = filterAndSortPublicProducts(matchingCatalog, query, 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual(expected)
  })

  it('does not match shop names, categories, or product types', () => {
    const result = filterAndSortPublicProducts(matchingCatalog, 'k12', 'asc', new Map())
    expect(result).toEqual([])
  })

  it('excludes product names containing a minus-prefixed keyword', () => {
    const catalog = [
      createProduct('positive', { name: 'GPT Plus Account', price: 5 }),
      createProduct('negative', { name: 'GPT Free Account - 非PLUS', price: 1 }),
      createProduct('legitimate-negative-detail', { name: 'GPT Plus Account - 非日抛', price: 4 }),
    ]

    const result = filterAndSortPublicProducts(catalog, 'plus -非plus', 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual(['legitimate-negative-detail', 'positive'])
  })

  it('supports multiple exclusions and exclusion-only queries', () => {
    const catalog = [
      createProduct('free', { name: 'GPT Plus Free Trial', price: 1 }),
      createProduct('proxy', { name: 'GPT Plus Proxy', price: 2 }),
      createProduct('account', { name: 'GPT Plus Account', price: 3 }),
      createProduct('k12', { name: 'GPT K12 Account', price: 4 }),
    ]

    expect(filterAndSortPublicProducts(catalog, 'plus -free -proxy', 'asc', new Map()).map(({ id }) => id))
      .toEqual(['account'])
    expect(filterAndSortPublicProducts(catalog, '-free -proxy', 'asc', new Map()).map(({ id }) => id))
      .toEqual(['account', 'k12'])
  })

  it('normalizes full-width exclusion keywords and only excludes by product name', () => {
    const catalog = [
      createProduct('name-match', { name: 'GPT Plus FREE', price: 1 }),
      createProduct('shop-match', { name: 'GPT Plus Account', shop_name: 'Free Shop', price: 2 }),
    ]

    const result = filterAndSortPublicProducts(catalog, 'plus －ＦＲＥＥ', 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual(['shop-match'])
  })

  it('orders matches by price regardless of match completeness', () => {
    const catalog = [
      createProduct('name-exact', { name: 'K12', price: 100 }),
      createProduct('name-prefix', { name: 'K12 Team', price: 50 }),
      createProduct('name-substring', { name: 'GPT K12 Team', price: 20 }),
      createProduct('cheapest', { name: 'Budget K12 Account', price: 1 }),
    ]

    const result = filterAndSortPublicProducts(catalog, 'k12', 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual([
      'cheapest',
      'name-substring',
      'name-prefix',
      'name-exact',
    ])
  })

  it('uses current sort prices to order matches in either direction', () => {
    const catalog = [
      createProduct('low', { name: 'Match Zeta', price: 100 }),
      createProduct('high', { name: 'Match Alpha', price: 100 }),
      createProduct('middle', { name: 'Match Beta', price: 100 }),
    ]
    const sortPrices = new Map([
      ['low', 5],
      ['high', 30],
      ['middle', 10],
    ])

    expect(filterAndSortPublicProducts(catalog, 'match', 'desc', sortPrices).map(({ id }) => id))
      .toEqual(['high', 'middle', 'low'])
    expect(filterAndSortPublicProducts(catalog, 'match', 'asc', sortPrices).map(({ id }) => id))
      .toEqual(['low', 'middle', 'high'])
  })

  it('keeps price-first ordering when the normalized query is empty', () => {
    const catalog = [
      createProduct('lower-cached-price', { name: 'Zeta', price: 10 }),
      createProduct('same-price-b', { name: 'Item 10', price: 20 }),
      createProduct('same-price-a', { name: 'Item 2', price: 20 }),
    ]
    const sortPrices = new Map([['lower-cached-price', 30]])

    const result = filterAndSortPublicProducts(catalog, '  　\t ', 'desc', sortPrices)
    expect(result.map(({ id }) => id)).toEqual([
      'lower-cached-price',
      'same-price-a',
      'same-price-b',
    ])
  })

  it('excludes unavailable products and returns a stable new array without mutating its input', () => {
    const catalog = [
      createProduct('out-of-stock', { name: 'Shared Item 1', stock: 0, price: 10 }),
      createProduct('c', { name: 'Shared Item 10', price: 10 }),
      createProduct('b', { name: 'Shared Item 2', price: 10 }),
      createProduct('a', { name: 'Shared Item 2', price: 10 }),
    ]
    const originalValue = JSON.stringify(catalog)

    const result = filterAndSortPublicProducts(catalog, 'shared', 'asc', new Map())

    expect(result.map(({ id }) => id)).toEqual(['a', 'b', 'c'])
    expect(result).not.toBe(catalog)
    expect(JSON.stringify(catalog)).toBe(originalValue)
  })
})

describe('publicProductGoodsKey', () => {
  it('extracts a pay.ldxp.cn item key', () => {
    expect(publicProductGoodsKey('https://pay.ldxp.cn/item/abc123')).toBe('abc123')
  })

  it('rejects other hosts and paths', () => {
    expect(publicProductGoodsKey('https://example.com/item/abc123')).toBe('')
    expect(publicProductGoodsKey('https://pay.ldxp.cn/shop/abc123')).toBe('')
  })
})

describe('normalizeLivePublicProduct', () => {
  it('uses the live detail price and stock', () => {
    const verifiedAt = new Date('2026-07-14T10:00:00Z')
    expect(normalizeLivePublicProduct(product, {
      price: '0.75',
      market_price: '1',
      extend: { stock_count: 20 },
    }, verifiedAt)).toEqual({
      price: 0.75,
      marketPrice: 1,
      stock: 20,
      updatedAt: '2026-07-14T10:00:00.000Z',
    })
  })

  it('keeps cached stock when the detail response omits it', () => {
    expect(normalizeLivePublicProduct(product, { price: 1 })).toMatchObject({ stock: 8 })
  })

  it('rejects missing prices and unavailable stock', () => {
    expect(normalizeLivePublicProduct(product, { extend: { stock_count: 2 } })).toBeNull()
    expect(normalizeLivePublicProduct(product, { price: 1, extend: { stock_count: 0 } })).toBeNull()
  })
})

describe('livePublicProductAvailability', () => {
  it('recognizes available and explicitly unavailable products', () => {
    expect(livePublicProductAvailability({ code: 1, data: { status: 1, price: 1 } })).toBe('available')
    expect(livePublicProductAvailability({
      code: 0,
      msg: '商品未上架，如有疑问请联系商家',
      data: null,
    })).toBe('unavailable')
    expect(livePublicProductAvailability({
      code: 1,
      data: { status: 1, price: 1, extend: { stock_count: 0 } },
    })).toBe('unavailable')
  })

  it('does not mistake transient API failures for delisted products', () => {
    expect(livePublicProductAvailability({ code: 0, msg: '请求频繁，请稍后重试', data: null })).toBe('unknown')
    expect(livePublicProductAvailability({ code: 1, data: null })).toBe('unknown')
  })
})
