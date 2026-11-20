import type {
  PublicAccountImportShop,
  PublicAccountImportProductSyncStatus,
} from '@/api/publicAccountImport'
import { supportsPublicShopProductSync } from '@/utils/publicShopProductSync'

export const PUBLIC_SHOP_SIZE_TIERS = ['201-1000', '51-200', '11-50', '1-10'] as const
export const PUBLIC_SHOP_OTHER_TIERS = ['unavailable', 'empty', 'closed', 'pending', 'unsupported'] as const
export const PUBLIC_SHOP_TIERS = [...PUBLIC_SHOP_SIZE_TIERS, ...PUBLIC_SHOP_OTHER_TIERS] as const
export type PublicShopTier = typeof PUBLIC_SHOP_TIERS[number]
export type PublicShopTierFilter = PublicShopTier | 'all'
export type PublicShopSortOrder = 'desc' | 'asc'

export interface PublicShopCatalogEntry {
  shop: PublicAccountImportShop
  tier: PublicShopTier
  effectiveCount: number | null
}

export function buildPublicShopCatalog(
  shops: readonly PublicAccountImportShop[],
  statuses: Readonly<Record<string, PublicAccountImportProductSyncStatus | undefined>>,
): PublicShopCatalogEntry[] {
  return shops.map(shop => {
    const schedule = statuses[shop.id]?.schedule
    const count = schedule?.effective_count
    return {
      shop,
      // Keep the confirmed tier during reclassification and pressure pauses.
      // Unsupported sources are not awaiting an automatic catalog check.
      tier: supportsPublicShopProductSync(shop.url) ? (schedule?.tier ?? 'pending') : 'unsupported',
      effectiveCount: typeof count === 'number' && Number.isInteger(count) && count >= 0 ? count : null,
    }
  })
}

function tierRank(tier: PublicShopTier, order: PublicShopSortOrder): number {
  const index = PUBLIC_SHOP_SIZE_TIERS.findIndex(value => value === tier)
  if (index >= 0) return order === 'desc' ? index : PUBLIC_SHOP_SIZE_TIERS.length - 1 - index
  return PUBLIC_SHOP_SIZE_TIERS.length + PUBLIC_SHOP_OTHER_TIERS.findIndex(value => value === tier)
}

function trustRank(shop: PublicAccountImportShop): number {
  if (shop.trust_level === 'trusted') return 0
  if (shop.trust_level === 'untrusted') return 2
  return 1
}

export function filterAndSortPublicShops(
  entries: readonly PublicShopCatalogEntry[],
  tier: PublicShopTierFilter = 'all',
  order: PublicShopSortOrder = 'desc',
): PublicShopCatalogEntry[] {
  return entries.filter(entry => tier === 'all' || entry.tier === tier).sort((left, right) => {
    const rank = tierRank(left.tier, order) - tierRank(right.tier, order)
    if (rank) return rank
    if (PUBLIC_SHOP_SIZE_TIERS.some(value => value === left.tier)) {
      // Unknown counts stay last even when ascending; never treat them as zero.
      if (left.effectiveCount === null && right.effectiveCount !== null) return 1
      if (right.effectiveCount === null && left.effectiveCount !== null) return -1
      if (left.effectiveCount !== null && right.effectiveCount !== null && left.effectiveCount !== right.effectiveCount) {
        return (left.effectiveCount - right.effectiveCount) * (order === 'desc' ? -1 : 1)
      }
    }
    // Preserve the previous trust ordering only inside equal tier/count groups.
    // Stable sorting keeps the original order when all three keys are equal.
    return trustRank(left.shop) - trustRank(right.shop)
  })
}
