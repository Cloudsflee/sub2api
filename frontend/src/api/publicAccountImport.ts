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
  action: 'created' | 'skipped' | 'failed' | string
  message?: string
}

export interface PublicAccountImportResult {
  total: number
  created: number
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
