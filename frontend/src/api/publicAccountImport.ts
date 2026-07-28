import { apiClient } from './client'

export interface PublicAccountImportGroup {
  id: number
  name: string
}

export interface PublicAccountImportMessage {
  index: number
  name?: string
  message: string
}

export interface PublicAccountImportItem {
  index: number
  name?: string
  action: 'created' | 'updated' | 'skipped' | 'failed' | string
  message?: string
}

export interface PublicAccountImportResult {
  total: number
  created: number
  updated: number
  skipped: number
  failed: number
  items?: PublicAccountImportItem[]
  warnings?: PublicAccountImportMessage[]
  errors?: PublicAccountImportMessage[]
}

export interface PublicAccountImportPayload {
  contents: string[]
  group_ids: number[]
}

export interface PublicAccountImportShop {
  id: string
  name: string
  url: string
  created_at: string
}

export interface PublicAccountImportShopSubmission {
  shop: PublicAccountImportShop
  created: boolean
}

export interface PublicAccountImportShopPayload {
  name: string
  url: string
}

export interface PublicAccountImportProduct {
  id: string
  shop_id: string
  shop_name: string
  shop_url: string
  name: string
  url: string
  image?: string
  category?: string
  goods_type: string
  price: number
  market_price?: number
  payable_price?: number
  unit_price?: number
  stock: number
  minimum_quantity: number
  quote_verified_at?: string
  updated_at: string
}

export type PublicAccountImportProductSyncState = 'idle' | 'queued' | 'refreshing' | 'failed'
export type PublicAccountImportProductSnapshotState = 'pending' | 'legacy' | 'fresh' | 'stale' | 'expired'

export interface PublicAccountImportProductSyncStatus {
  shop_id: string
  state: PublicAccountImportProductSyncState
  updated_at: string
  snapshot_state: PublicAccountImportProductSnapshotState
  snapshot_updated_at: string
  snapshot_expires_at: string
  retry_after_seconds: number
}

export interface PublicAccountImportProductRefreshResponse extends PublicAccountImportProductSyncStatus {
  accepted: boolean
}

export interface PublicAccountImportProductsResponse {
  products: PublicAccountImportProduct[]
  shop_count: number
  pending_shops: number
  queued_shops: number
  refreshing_shops: number
  failed_shops: number
  expired_shops: number
  refresh_seconds: number
  shop_sync_statuses: PublicAccountImportProductSyncStatus[]
}

export interface PublicAccountImportProductsETagResult {
  notModified: boolean
  etag: string | null
  data: PublicAccountImportProductsResponse | null
}

export async function getPublicAccountImportGroups(): Promise<PublicAccountImportGroup[]> {
  const { data } = await apiClient.get<{ groups: PublicAccountImportGroup[] }>(
    '/public/account-import/groups'
  )
  return data.groups || []
}

export async function submitPublicAccountImport(
  payload: PublicAccountImportPayload,
  idempotencyKey: string
): Promise<PublicAccountImportResult> {
  const { data } = await apiClient.post<PublicAccountImportResult>(
    '/public/account-import',
    payload,
    { headers: { 'Idempotency-Key': idempotencyKey } }
  )
  return data
}

export async function getPublicAccountImportShops(): Promise<PublicAccountImportShop[]> {
  const { data } = await apiClient.get<{ shops: PublicAccountImportShop[] }>(
    '/public/account-import/shops'
  )
  return data.shops || []
}

export async function submitPublicAccountImportShop(
  payload: PublicAccountImportShopPayload
): Promise<PublicAccountImportShopSubmission> {
  const { data } = await apiClient.post<PublicAccountImportShopSubmission>(
    '/public/account-import/shops',
    payload
  )
  return data
}

export async function getPublicAccountImportProducts(): Promise<PublicAccountImportProductsResponse> {
  const result = await getPublicAccountImportProductsWithETag()
  return result.data || normalizePublicAccountImportProductsResponse({})
}

export async function getPublicAccountImportProductsWithETag(
  etag: string | null = null
): Promise<PublicAccountImportProductsETagResult> {
  const headers: Record<string, string> = {}
  if (etag) headers['If-None-Match'] = etag
  const response = await apiClient.get<PublicAccountImportProductsResponse>(
    '/public/account-import/products',
    {
      headers,
      validateStatus: (status) => (status >= 200 && status < 300) || status === 304,
    }
  )
  const responseETag = typeof response.headers?.etag === 'string' ? response.headers.etag : etag
  if (response.status === 304) {
    return { notModified: true, etag: responseETag, data: null }
  }
  return {
    notModified: false,
    etag: responseETag,
    data: normalizePublicAccountImportProductsResponse(response.data),
  }
}

function normalizePublicAccountImportProductsResponse(
  data: Partial<PublicAccountImportProductsResponse>
): PublicAccountImportProductsResponse {
  return {
    products: data.products || [],
    shop_count: data.shop_count || 0,
    pending_shops: data.pending_shops || 0,
    queued_shops: data.queued_shops || 0,
    refreshing_shops: data.refreshing_shops || 0,
    failed_shops: data.failed_shops || 0,
		expired_shops: data.expired_shops || 0,
    refresh_seconds: data.refresh_seconds || 900,
    shop_sync_statuses: Array.isArray(data.shop_sync_statuses)
      ? data.shop_sync_statuses.map((status) => normalizePublicAccountImportProductSyncStatus(status))
      : [],
  }
}

export async function requestPublicAccountImportProductRefresh(
  shopId: string
): Promise<PublicAccountImportProductRefreshResponse> {
  const { data } = await apiClient.post<PublicAccountImportProductRefreshResponse>(
    '/public/account-import/products/refresh',
    { shop_id: shopId }
  )
  return {
    ...normalizePublicAccountImportProductSyncStatus(data, shopId),
    accepted: Boolean(data?.accepted),
  }
}

function normalizePublicAccountImportProductSyncStatus(
  value: Partial<PublicAccountImportProductSyncStatus> | null | undefined,
  fallbackShopId = ''
): PublicAccountImportProductSyncStatus {
  const state = value?.state
	const snapshotState = value?.snapshot_state
  return {
    shop_id: typeof value?.shop_id === 'string' ? value.shop_id : fallbackShopId,
    state: state === 'queued' || state === 'refreshing' || state === 'failed' ? state : 'idle',
    updated_at: typeof value?.updated_at === 'string' ? value.updated_at : '',
		snapshot_state: snapshotState === 'legacy' || snapshotState === 'fresh' || snapshotState === 'stale' || snapshotState === 'expired'
			? snapshotState
			: 'pending',
		snapshot_updated_at: typeof value?.snapshot_updated_at === 'string'
			? value.snapshot_updated_at
			: typeof value?.updated_at === 'string' ? value.updated_at : '',
		snapshot_expires_at: typeof value?.snapshot_expires_at === 'string' ? value.snapshot_expires_at : '',
    retry_after_seconds: normalizeNonNegativeInteger(value?.retry_after_seconds),
  }
}

function normalizeNonNegativeInteger(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : 0
}
