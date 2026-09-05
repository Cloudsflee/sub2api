import type { PublicAccountImportProductSyncStatus } from '@/api/publicAccountImport'

export interface TrackedPublicAccountImportProductSyncStatus extends PublicAccountImportProductSyncStatus {
  retry_until_ms: number
}

export const PUBLIC_SHOP_CANONICAL_ORIGIN = 'https://wzyp.cn'
const PUBLIC_SHOP_HOSTNAMES = new Set(['wzyp.cn', 'pay.ldxp.cn'])

export function isPublicShopHostname(value: string): boolean {
  return PUBLIC_SHOP_HOSTNAMES.has(value.toLowerCase())
}

export function supportsPublicShopProductSync(value: string): boolean {
  try {
    const parsed = new URL(value)
    if (parsed.protocol !== 'https:' || !isPublicShopHostname(parsed.hostname)) return false
    const parts = parsed.pathname.replace(/^\/+|\/+$/g, '').split('/')
    if ((parts.length !== 2 && parts.length !== 3) || parts[0] !== 'shop') return false
    return parts.slice(1).every((part) => {
      const value = decodeURIComponent(part).trim()
      return value.length > 0 && !value.includes('/')
    })
  } catch {
    return false
  }
}

export function trackPublicShopProductSyncStatus(
  status: PublicAccountImportProductSyncStatus,
  now = Date.now()
): TrackedPublicAccountImportProductSyncStatus {
  return {
    ...status,
    retry_until_ms: status.retry_after_seconds > 0
      ? now + status.retry_after_seconds * 1000
      : 0,
  }
}

export function publicShopProductSyncRetryAfter(
  status: TrackedPublicAccountImportProductSyncStatus | undefined,
  now = Date.now()
): number {
  if (!status?.retry_until_ms) return 0
  return Math.max(0, Math.ceil((status.retry_until_ms - now) / 1000))
}

export function publicShopProductRefreshDisabled(
  status: TrackedPublicAccountImportProductSyncStatus | undefined,
  requesting: boolean,
  now = Date.now()
): boolean {
  return requesting
    || status?.state === 'queued'
    || status?.state === 'refreshing'
    || publicShopProductSyncRetryAfter(status, now) > 0
}
