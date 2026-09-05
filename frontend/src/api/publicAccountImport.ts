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

/** Authenticated OpenAI-compatible upstream account import payload. */
export interface PublicAccountImportUpstreamPayload {
  name?: string
  base_url: string
  api_key: string
  group_ids: number[]
}

export type PublicAccountImportShopTrustLevel = 'trusted' | 'neutral' | 'untrusted'

export interface PublicAccountImportShop {
  id: string
  name: string
  url: string
  created_at: string
  trust_level: PublicAccountImportShopTrustLevel
}

export interface PublicAccountImportShopSubmission {
  shop: PublicAccountImportShop
  created: boolean
}

export interface PublicAccountImportShopPayload {
  name: string
  url: string
}

export interface PublicAccountImportShopDeletion {
  id: string
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
export type PublicAccountImportProductSyncLaneAvailability = 'available' | 'unavailable'
export type PublicAccountImportProductSyncPressureState = 'clear' | 'pressured' | 'recovering' | 'silent' | 'unknown'

export interface PublicAccountImportProductSyncWorkerLaneStatus {
  lane: number
  availability: PublicAccountImportProductSyncLaneAvailability
  reason: string
  state: string
  updated_at?: string
  retry_at?: string
  challenge_state?: string
  egress_id?: string
  egress_circuit_open_until?: string
}

export interface PublicAccountImportProductSyncEgressCircuit {
  egress_id: string
  circuit_open_until: string
}

export interface PublicAccountImportProductSyncWAFResponseFingerprint {
  observed_at: string
  status: number
  api_path: string
  server?: string
  x_tengine_error?: string
  body_length: number
  body_sha256: string
  title?: string
  egress_id: string
}

export interface PublicAccountImportProductSyncWorkerStatus {
  availability: PublicAccountImportProductSyncLaneAvailability
  reason: string
  updated_at: string
  configured_lane_count: number
  expected_lane_count: number
  available_lane_count: number
  unavailable_lane_count: number
  adaptive_rate_per_second: number
  global_pressure_state: PublicAccountImportProductSyncPressureState
  global_pressure_until: string
  global_silence_until: string
  last_pressure_kind: string
  egress_circuits: PublicAccountImportProductSyncEgressCircuit[]
  waf_response_fingerprint: PublicAccountImportProductSyncWAFResponseFingerprint | null
  lanes: PublicAccountImportProductSyncWorkerLaneStatus[]
}

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
  worker_status?: PublicAccountImportProductSyncWorkerStatus
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

/**
 * Import one OpenAI-compatible URL + API key account for the signed-in user.
 * The API key is write-only and is never present in the result type.
 */
export async function submitPublicAccountImportUpstream(
  payload: PublicAccountImportUpstreamPayload,
  idempotencyKey: string
): Promise<PublicAccountImportResult> {
  const { data } = await apiClient.post<PublicAccountImportResult>(
    '/user/account-import/upstream',
    payload,
    { headers: { 'Idempotency-Key': idempotencyKey } }
  )
  return data
}

export async function getPublicAccountImportShops(): Promise<PublicAccountImportShop[]> {
  const { data } = await apiClient.get<{ shops: PublicAccountImportShop[] }>(
    '/public/account-import/shops'
  )
  return (data.shops || []).map(normalizePublicAccountImportShop)
}

export async function submitPublicAccountImportShop(
  payload: PublicAccountImportShopPayload
): Promise<PublicAccountImportShopSubmission> {
  const { data } = await apiClient.post<PublicAccountImportShopSubmission>(
    '/public/account-import/shops',
    payload
  )
  return { ...data, shop: normalizePublicAccountImportShop(data.shop) }
}

export async function updatePublicAccountImportShopTrustLevel(
  shopId: string,
  trustLevel: PublicAccountImportShopTrustLevel
): Promise<PublicAccountImportShop> {
  const { data } = await apiClient.patch<PublicAccountImportShop>(
    `/admin/public-account-import/shops/${encodeURIComponent(shopId)}`,
    { trust_level: trustLevel }
  )
  return normalizePublicAccountImportShop(data)
}

export async function deletePublicAccountImportShop(
  shopId: string
): Promise<PublicAccountImportShopDeletion> {
  const { data } = await apiClient.delete<PublicAccountImportShopDeletion>(
    `/admin/public-account-import/shops/${encodeURIComponent(shopId)}`
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
    worker_status: normalizePublicAccountImportProductSyncWorkerStatus(data.worker_status),
  }
}

function normalizePublicAccountImportProductSyncWorkerStatus(
  value: Partial<PublicAccountImportProductSyncWorkerStatus> | null | undefined
): PublicAccountImportProductSyncWorkerStatus {
  const expectedLaneCount = Math.min(normalizePositiveInteger(value?.expected_lane_count, 6), 6)
  const rawLanes = Array.isArray(value?.lanes) ? value.lanes : []
  const lanes = Array.from({ length: expectedLaneCount }, (_, index) => {
    const raw = rawLanes[index]
    const availability = raw?.availability === 'available' ? 'available' : 'unavailable'
    return {
      lane: normalizePositiveInteger(raw?.lane, index + 1),
      availability,
      reason: typeof raw?.reason === 'string' ? raw.reason : '',
      state: typeof raw?.state === 'string' ? raw.state : 'unknown',
      updated_at: typeof raw?.updated_at === 'string' ? raw.updated_at : '',
      retry_at: typeof raw?.retry_at === 'string' ? raw.retry_at : '',
      challenge_state: typeof raw?.challenge_state === 'string' ? raw.challenge_state : '',
      egress_id: typeof raw?.egress_id === 'string' ? raw.egress_id : '',
      egress_circuit_open_until: typeof raw?.egress_circuit_open_until === 'string'
        ? raw.egress_circuit_open_until
        : '',
    } satisfies PublicAccountImportProductSyncWorkerLaneStatus
  })
  const availability = value?.availability === 'available' ? 'available' : 'unavailable'
  return {
    availability,
    reason: typeof value?.reason === 'string' ? value.reason : '',
    updated_at: typeof value?.updated_at === 'string' ? value.updated_at : '',
    configured_lane_count: normalizeNonNegativeInteger(value?.configured_lane_count),
    expected_lane_count: expectedLaneCount,
    available_lane_count: normalizeNonNegativeInteger(value?.available_lane_count),
    unavailable_lane_count: normalizeNonNegativeInteger(value?.unavailable_lane_count),
    adaptive_rate_per_second: normalizeAdaptiveRate(value?.adaptive_rate_per_second),
    global_pressure_state: normalizeProductSyncPressureState(value?.global_pressure_state),
    global_pressure_until: typeof value?.global_pressure_until === 'string' ? value.global_pressure_until : '',
    global_silence_until: typeof value?.global_silence_until === 'string' ? value.global_silence_until : '',
    last_pressure_kind: typeof value?.last_pressure_kind === 'string' ? value.last_pressure_kind : '',
    egress_circuits: Array.isArray(value?.egress_circuits)
      ? value.egress_circuits
        .filter((circuit) => typeof circuit?.egress_id === 'string' && typeof circuit?.circuit_open_until === 'string')
        .slice(0, 12)
        .map((circuit) => ({ egress_id: circuit.egress_id, circuit_open_until: circuit.circuit_open_until }))
      : [],
    waf_response_fingerprint: normalizeWAFResponseFingerprint(value?.waf_response_fingerprint),
    lanes,
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

function normalizeAdaptiveRate(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed >= 0.1 && parsed <= 10
    ? Math.round(parsed * 100) / 100
    : 0
}

function normalizeProductSyncPressureState(value: unknown): PublicAccountImportProductSyncPressureState {
  return value === 'clear' || value === 'pressured' || value === 'recovering' || value === 'silent'
    ? value
    : 'unknown'
}

function normalizeWAFResponseFingerprint(
  value: Partial<PublicAccountImportProductSyncWAFResponseFingerprint> | null | undefined
): PublicAccountImportProductSyncWAFResponseFingerprint | null {
  if (!value || Number(value.status) !== 403 || typeof value.api_path !== 'string'
    || typeof value.body_sha256 !== 'string' || typeof value.egress_id !== 'string') return null
  return {
    observed_at: typeof value.observed_at === 'string' ? value.observed_at : '',
    status: 403,
    api_path: value.api_path,
    server: typeof value.server === 'string' ? value.server : '',
    x_tengine_error: typeof value.x_tengine_error === 'string' ? value.x_tengine_error : '',
    body_length: normalizeNonNegativeInteger(value.body_length),
    body_sha256: value.body_sha256,
    title: typeof value.title === 'string' ? value.title : '',
    egress_id: value.egress_id,
  }
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback
}

function normalizePublicAccountImportShop(
  shop: PublicAccountImportShop
): PublicAccountImportShop {
  return {
    ...shop,
    trust_level: normalizePublicAccountImportShopTrustLevel(shop?.trust_level),
  }
}

function normalizePublicAccountImportShopTrustLevel(
  value: unknown
): PublicAccountImportShopTrustLevel {
  return value === 'trusted' || value === 'untrusted' ? value : 'neutral'
}
