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
    createProduct('cross-field', {
      name: 'Alpha Bundle',
      shop_name: 'Cloud Store',
      category: 'Developer Tools',
      goods_type: 'card',
    }),
    createProduct('partial-match', {
      name: 'Alpha Basic',
      shop_name: 'Local Store',
      category: undefined,
      goods_type: 'article',
    }),
    createProduct('optional-field', {
      name: 'Cloud Resource',
      shop_name: 'Other Store',
      category: undefined,
      goods_type: 'resource',
    }),
  ]

  it.each([
    { label: 'matches keywords across product and shop names', query: 'alpha cloud', expected: ['cross-field'] },
    { label: 'normalizes letter case', query: 'ALPHA CLOUD', expected: ['cross-field'] },
    { label: 'normalizes full-width characters', query: 'ＡＬＰＨＡ　ＣＬＯＵＤ', expected: ['cross-field'] },
    { label: 'collapses consecutive whitespace', query: '  alpha \t  cloud  ', expected: ['cross-field'] },
    { label: 'deduplicates keywords', query: 'alpha alpha cloud', expected: ['cross-field'] },
    { label: 'supports an omitted optional category', query: 'cloud resource', expected: ['optional-field'] },
    { label: 'requires every keyword to match', query: 'alpha missing', expected: [] },
  ])('$label', ({ query, expected }) => {
    const result = filterAndSortPublicProducts(matchingCatalog, query, 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual(expected)
  })

  it.each([
    ['card', '卡密'],
    ['article', '文章'],
    ['resource', '资源'],
    ['equity', '权益'],
  ] as const)('matches the %s product type through its Chinese alias', (goodsType, alias) => {
    const catalog = ['card', 'article', 'resource', 'equity'].map((type) => createProduct(type, {
      name: `Catalog ${type}`,
      goods_type: type,
    }))

    const result = filterAndSortPublicProducts(catalog, alias, 'asc', new Map())
    expect(result.map(({ id }) => id)).toEqual([goodsType])
  })

  it('orders matches by field relevance and full-name match quality', () => {
    const catalog = [
      createProduct('type', { name: 'Gamma', goods_type: 'target', price: 100 }),
      createProduct('category', { name: 'Beta', category: 'target', price: 100 }),
      createProduct('shop', { name: 'Alpha', shop_name: 'target', price: 100 }),
      createProduct('name-substring', { name: 'Best Target Pack', price: 100 }),
      createProduct('name-prefix', { name: 'Target Pack', price: 100 }),
      createProduct('name-exact', { name: 'TARGET', price: 100 }),
    ]

    const result = filterAndSortPublicProducts(catalog, 'target', 'desc', new Map())
    expect(result.map(({ id }) => id)).toEqual([
      'name-exact',
      'name-prefix',
      'name-substring',
      'shop',
      'category',
      'type',
    ])
  })

  it('uses current sort prices to break equal-relevance ties in either direction', () => {
    const catalog = [
      createProduct('low', { name: 'Zeta', shop_name: 'match', price: 100 }),
      createProduct('high', { name: 'Alpha', shop_name: 'match', price: 100 }),
      createProduct('middle', { name: 'Beta', shop_name: 'match', price: 100 }),
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
      createProduct('out-of-stock', { name: 'Item 1', shop_name: 'shared', stock: 0, price: 10 }),
      createProduct('c', { name: 'Item 10', shop_name: 'shared', price: 10 }),
      createProduct('b', { name: 'Item 2', shop_name: 'shared', price: 10 }),
      createProduct('a', { name: 'Item 2', shop_name: 'shared', price: 10 }),
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
