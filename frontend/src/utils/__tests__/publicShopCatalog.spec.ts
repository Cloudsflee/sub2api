import { describe, expect, it } from 'vitest'
import type { PublicAccountImportProductSyncStatus, PublicAccountImportShop, PublicAccountImportShopSchedule } from '@/api/publicAccountImport'
import { buildPublicShopCatalog, filterAndSortPublicShops } from '@/utils/publicShopCatalog'

const shop = (id: string, trust: PublicAccountImportShop['trust_level'] = 'neutral'): PublicAccountImportShop => ({
  id, name: id, url: `https://wzyp.cn/shop/${id}`, created_at: '', trust_level: trust,
})
const status = (id: string, tier: PublicAccountImportShopSchedule['tier'], count: number | null): PublicAccountImportProductSyncStatus => ({
  shop_id: id, state: 'idle', updated_at: '', snapshot_state: 'pending', snapshot_updated_at: '',
  snapshot_expires_at: '', retry_after_seconds: 0,
  schedule: { tier, state: 'active', effective_count: count, catalog_seconds: 60, quote_seconds: 3600, tier_pending: false },
})

describe('public shop catalog ordering', () => {
  it('groups by confirmed size tiers before count and trust, without changing inputs', () => {
    const shops = [shop('small', 'trusted'), shop('large-low', 'trusted'), shop('medium'), shop('large-high', 'untrusted'), shop('boundary')]
    const statuses = {
      small: status('small', '1-10', 10), medium: status('medium', '51-200', 150),
      'large-low': status('large-low', '201-1000', 300), 'large-high': status('large-high', '201-1000', 800),
      boundary: status('boundary', '11-50', 201),
    }
    statuses.boundary.schedule!.tier_pending = true
    statuses['large-high'].schedule!.state = 'pressure_paused'
    const entries = buildPublicShopCatalog(shops, statuses)
    expect(filterAndSortPublicShops(entries).map(e => e.shop.id)).toEqual(['large-high', 'large-low', 'medium', 'boundary', 'small'])
    expect(entries.map(e => e.shop.id)).toEqual(shops.map(s => s.id))
    expect(filterAndSortPublicShops(entries, '11-50').map(e => e.shop.id)).toEqual(['boundary'])
  })

  it('reverses size tiers and counts while leaving non-size states after them', () => {
    const shops = ['large', 'small-two', 'small-one', 'empty', 'unavailable', 'closed', 'pending'].map(id => shop(id))
    shops.push({ ...shop('unsupported'), url: 'https://example.com/shop' })
    const statuses = {
      large: status('large', '201-1000', 500), 'small-two': status('small-two', '1-10', 2),
      'small-one': status('small-one', '1-10', 1), empty: status('empty', 'empty', 0),
      unavailable: status('unavailable', 'unavailable', 0), closed: status('closed', 'closed', 0),
    }
    const entries = buildPublicShopCatalog(shops, statuses)
    expect(filterAndSortPublicShops(entries, 'all', 'asc').map(e => e.shop.id)).toEqual([
      'small-one', 'small-two', 'large', 'unavailable', 'empty', 'closed', 'pending', 'unsupported',
    ])
    expect(filterAndSortPublicShops(entries, 'pending').map(e => e.shop.id)).toEqual(['pending'])
    expect(filterAndSortPublicShops(entries, 'unsupported').map(e => e.shop.id)).toEqual(['unsupported'])
  })

  it.each(['asc', 'desc'] as const)('keeps unknown counts last in %s order and retains stable trust ties', order => {
    const shops = [shop('unknown', 'trusted'), shop('neutral'), shop('trusted-a', 'trusted'), shop('trusted-b', 'trusted'), shop('untrusted', 'untrusted')]
    const statuses = Object.fromEntries(shops.map(s => [s.id, status(s.id, '11-50', s.id === 'unknown' ? null : 20)]))
    const entries = buildPublicShopCatalog(shops, statuses)
    expect(filterAndSortPublicShops(entries, 'all', order).map(e => e.shop.id)).toEqual(['trusted-a', 'trusted-b', 'neutral', 'untrusted', 'unknown'])
  })
})
