import { describe, expect, it } from 'vitest'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import {
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
