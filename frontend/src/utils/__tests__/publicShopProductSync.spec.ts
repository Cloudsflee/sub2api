import { describe, expect, it } from 'vitest'

import {
  publicShopProductRefreshDisabled,
  publicShopProductSyncRetryAfter,
  supportsPublicShopProductSync,
  trackPublicShopProductSyncStatus,
} from '@/utils/publicShopProductSync'

describe('public shop product synchronization helpers', () => {
	const pendingSnapshot = {
		snapshot_state: 'pending' as const,
		snapshot_updated_at: '',
		snapshot_expires_at: '',
	}
  it('supports only pay.ldxp.cn shop links', () => {
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/shop/token')).toBe(true)
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/shop/token/')).toBe(true)
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/shop/7HZ37ZCG/g47fr5')).toBe(true)
    expect(supportsPublicShopProductSync('http://pay.ldxp.cn/shop/token')).toBe(false)
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/item/token')).toBe(false)
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/shop/token/category/extra')).toBe(false)
    expect(supportsPublicShopProductSync('https://pay.ldxp.cn/shop/token%2Fextra')).toBe(false)
    expect(supportsPublicShopProductSync('https://example.com/shop/token')).toBe(false)
  })

  it('disables requests independently for request, queue, running, and cooldown states', () => {
    const now = 1_000_000
    const idle = trackPublicShopProductSyncStatus({
			shop_id: 'idle', state: 'idle', updated_at: '', retry_after_seconds: 0, ...pendingSnapshot,
    }, now)
    const queued = trackPublicShopProductSyncStatus({
			shop_id: 'queued', state: 'queued', updated_at: '', retry_after_seconds: 0, ...pendingSnapshot,
    }, now)
    const refreshing = trackPublicShopProductSyncStatus({
			shop_id: 'refreshing', state: 'refreshing', updated_at: '', retry_after_seconds: 0, ...pendingSnapshot,
    }, now)
    const failed = trackPublicShopProductSyncStatus({
			shop_id: 'failed', state: 'failed', updated_at: '', retry_after_seconds: 0, ...pendingSnapshot,
    }, now)
    const cooling = trackPublicShopProductSyncStatus({
			shop_id: 'cooling', state: 'idle', updated_at: '', retry_after_seconds: 5, ...pendingSnapshot,
    }, now)

    expect(publicShopProductRefreshDisabled(idle, false, now)).toBe(false)
    expect(publicShopProductRefreshDisabled(idle, true, now)).toBe(true)
    expect(publicShopProductRefreshDisabled(queued, false, now)).toBe(true)
    expect(publicShopProductRefreshDisabled(refreshing, false, now)).toBe(true)
    expect(publicShopProductRefreshDisabled(failed, false, now)).toBe(false)
    expect(publicShopProductRefreshDisabled(cooling, false, now)).toBe(true)
    expect(publicShopProductRefreshDisabled(cooling, false, now + 5_000)).toBe(false)
  })

  it('counts a server cooldown down locally', () => {
    const status = trackPublicShopProductSyncStatus({
			shop_id: 'shop', state: 'idle', updated_at: '', retry_after_seconds: 5, ...pendingSnapshot,
    }, 10_000)

    expect(publicShopProductSyncRetryAfter(status, 10_000)).toBe(5)
    expect(publicShopProductSyncRetryAfter(status, 12_001)).toBe(3)
    expect(publicShopProductSyncRetryAfter(status, 15_000)).toBe(0)
  })
})
