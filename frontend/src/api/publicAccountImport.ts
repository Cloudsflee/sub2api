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
