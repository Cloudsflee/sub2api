import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, patch, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  del: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, patch, delete: del },
}))

import {
  deletePublicAccountImportShop,
  getPublicAccountImportShops,
  getPublicAccountImportProducts,
	getPublicAccountImportProductsWithETag,
  requestPublicAccountImportProductRefresh,
  updatePublicAccountImportShopTrustLevel,
} from '@/api/publicAccountImport'

describe('public account import product API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    patch.mockReset()
    del.mockReset()
  })

  it('normalizes public trust levels and uses the administrator shop paths for writes', async () => {
    const shop = {
      id: 'shop/one', name: 'Shop', url: 'https://example.com/shop',
      created_at: '2026-07-31T00:00:00Z', trust_level: 'invalid',
    }
    get.mockResolvedValue({ data: { shops: [shop] } })
    patch.mockResolvedValue({ data: { ...shop, trust_level: 'trusted' } })
    del.mockResolvedValue({ data: { id: shop.id } })

    await expect(getPublicAccountImportShops()).resolves.toEqual([{ ...shop, trust_level: 'neutral' }])
    await expect(updatePublicAccountImportShopTrustLevel(shop.id, 'trusted')).resolves.toMatchObject({ trust_level: 'trusted' })
    await expect(deletePublicAccountImportShop(shop.id)).resolves.toEqual({ id: shop.id })

    expect(patch).toHaveBeenCalledWith('/admin/public-account-import/shops/shop%2Fone', {
      trust_level: 'trusted',
    })
    expect(del).toHaveBeenCalledWith('/admin/public-account-import/shops/shop%2Fone')
  })

  it('normalizes per-shop product sync statuses while preserving catalog counts', async () => {
    get.mockResolvedValue({
      data: {
        products: [],
        shop_count: 2,
        pending_shops: 1,
        queued_shops: 1,
        refreshing_shops: 0,
        failed_shops: 0,
			expired_shops: 1,
        refresh_seconds: 900,
        shop_sync_statuses: [
				{
					shop_id: 'one', state: 'queued', updated_at: '2026-07-27T01:00:00Z', retry_after_seconds: 12.9,
					snapshot_state: 'stale', snapshot_updated_at: '2026-07-27T01:00:00Z', snapshot_expires_at: '2026-07-27T01:30:00Z',
				},
          { shop_id: 'two', state: 'unknown', updated_at: 123, retry_after_seconds: -1 },
        ],
      },
    })

    const catalog = await getPublicAccountImportProducts()

		expect(get).toHaveBeenCalledWith('/public/account-import/products', expect.objectContaining({ headers: {} }))
    expect(catalog).toMatchObject({
      shop_count: 2,
      pending_shops: 1,
      queued_shops: 1,
      refreshing_shops: 0,
      failed_shops: 0,
			expired_shops: 1,
    })
    expect(catalog.shop_sync_statuses).toEqual([
			{
				shop_id: 'one', state: 'queued', updated_at: '2026-07-27T01:00:00Z', retry_after_seconds: 12,
				snapshot_state: 'stale', snapshot_updated_at: '2026-07-27T01:00:00Z', snapshot_expires_at: '2026-07-27T01:30:00Z',
			},
			{
				shop_id: 'two', state: 'idle', updated_at: '', retry_after_seconds: 0,
				snapshot_state: 'pending', snapshot_updated_at: '', snapshot_expires_at: '',
			},
    ])
  })

  it('keeps old product responses compatible when shop statuses are absent', async () => {
    get.mockResolvedValue({ data: { products: [] } })

    const catalog = await getPublicAccountImportProducts()

    expect(catalog.shop_sync_statuses).toEqual([])
    expect(catalog.refresh_seconds).toBe(900)
  })

  it('posts one shop id and normalizes the refresh response', async () => {
    post.mockResolvedValue({
      data: {
        accepted: 1,
        state: 'invalid',
        retry_after_seconds: '4.8',
      },
    })

    const status = await requestPublicAccountImportProductRefresh('shop-one')

    expect(post).toHaveBeenCalledWith('/public/account-import/products/refresh', {
      shop_id: 'shop-one',
    })
    expect(status).toEqual({
      accepted: true,
      shop_id: 'shop-one',
      state: 'idle',
      updated_at: '',
			snapshot_state: 'pending',
			snapshot_updated_at: '',
			snapshot_expires_at: '',
      retry_after_seconds: 4,
    })
  })

	it('uses If-None-Match and preserves a 304 response', async () => {
		get.mockResolvedValue({ status: 304, headers: { etag: '"catalog"' }, data: '' })

		const result = await getPublicAccountImportProductsWithETag('"catalog"')

		expect(get).toHaveBeenCalledWith('/public/account-import/products', expect.objectContaining({
			headers: { 'If-None-Match': '"catalog"' },
		}))
		expect(result).toEqual({ notModified: true, etag: '"catalog"', data: null })
	})
})
