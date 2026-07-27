import type { PublicAccountImportProductSyncStatus } from '@/api/publicAccountImport'

export interface TrackedPublicAccountImportProductSyncStatus extends PublicAccountImportProductSyncStatus {
  retry_until_ms: number
}

export function supportsPublicShopProductSync(value: string): boolean {
  try {
    const parsed = new URL(value)
    if (parsed.protocol !== 'https:' || parsed.hostname.toLowerCase() !== 'pay.ldxp.cn') return false
    const parts = parsed.pathname.replace(/^\/+|\/+$/g, '').split('/')
    if (parts.length !== 2 || parts[0] !== 'shop') return false
    const token = decodeURIComponent(parts[1]).trim()
    return token.length > 0 && !token.includes('/')
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
