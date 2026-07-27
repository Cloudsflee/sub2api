import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post },
}))

import {
  getPublicAccountImportProducts,
  requestPublicAccountImportProductRefresh,
} from '@/api/publicAccountImport'

describe('public account import product API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
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
        refresh_seconds: 900,
        shop_sync_statuses: [
          { shop_id: 'one', state: 'queued', updated_at: '2026-07-27T01:00:00Z', retry_after_seconds: 12.9 },
          { shop_id: 'two', state: 'unknown', updated_at: 123, retry_after_seconds: -1 },
        ],
      },
    })

    const catalog = await getPublicAccountImportProducts()

    expect(get).toHaveBeenCalledWith('/public/account-import/products')
    expect(catalog).toMatchObject({
      shop_count: 2,
      pending_shops: 1,
      queued_shops: 1,
      refreshing_shops: 0,
      failed_shops: 0,
    })
    expect(catalog.shop_sync_statuses).toEqual([
      { shop_id: 'one', state: 'queued', updated_at: '2026-07-27T01:00:00Z', retry_after_seconds: 12 },
      { shop_id: 'two', state: 'idle', updated_at: '', retry_after_seconds: 0 },
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
      retry_after_seconds: 4,
    })
  })
})
