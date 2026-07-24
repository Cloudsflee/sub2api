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
  stock: number
  minimum_quantity: number
  updated_at: string
}

export interface PublicAccountImportProductsResponse {
  products: PublicAccountImportProduct[]
  shop_count: number
  pending_shops: number
  queued_shops: number
  refreshing_shops: number
  failed_shops: number
  refresh_seconds: number
}

export interface PublicAccountImportProductSyncJob {
  shop_id: string
  shop_name: string
  shop_url: string
  token: string
  attempt_id: string
}

export interface PublicAccountImportProductSyncItem {
  goods_key: string
  name: string
  url: string
  image: string
  category: string
  goods_type: string
  price: number
  market_price: number
  stock: number
  minimum_quantity: number
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
  const { data } = await apiClient.get<PublicAccountImportProductsResponse>(
    '/public/account-import/products'
  )
  return {
    products: data.products || [],
    shop_count: data.shop_count || 0,
    pending_shops: data.pending_shops || 0,
    queued_shops: data.queued_shops || 0,
    refreshing_shops: data.refreshing_shops || 0,
    failed_shops: data.failed_shops || 0,
    refresh_seconds: data.refresh_seconds || 900,
  }
}

export async function getPublicAccountImportProductSyncJob(): Promise<PublicAccountImportProductSyncJob | null> {
  const { data } = await apiClient.get<{ job?: PublicAccountImportProductSyncJob | null }>(
    '/public/account-import/products/sync-job'
  )
  return data.job || null
}

export async function submitPublicAccountImportProductSync(
  shopId: string,
  products: PublicAccountImportProductSyncItem[],
  attemptId = ''
): Promise<void> {
  await apiClient.post('/public/account-import/products/sync', {
    shop_id: shopId,
    attempt_id: attemptId,
    products,
  })
}
