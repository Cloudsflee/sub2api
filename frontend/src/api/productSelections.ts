import { apiClient } from './client'

export const selectionStages = ['review', 'priority', 'trial', 'addon', 'hold', 'rejected', 'archived'] as const
export type SelectionStage = typeof selectionStages[number]
export const selectionChannels = ['private', 'taobao', 'xianyu', 'xiaohongshu'] as const
export type SelectionChannel = typeof selectionChannels[number]

export interface SelectionData {
  title: string
  product_id: string
  shop_id: string
  shop_name: string
  source_url: string
  category: string
  research_id: string
  stage: SelectionStage
  channels: SelectionChannel[]
  tags: string[]
  purchase_price: number | null
  price_basis: 'pending' | 'snapshot' | 'estimate'
  quote_verified_at: string
  minimum_quantity: number
  target_sale_price: number | null
  observed_sale_price: number | null
  specification: string
  customer: string
  official_comparison: string
  alternatives: string
  information_gap: string
  delivery_risk: string
  acquisition_plan: string
  economics: string
  repeat_assessment: string
  decision: string
  next_action: string
  notes: string
}

export interface SelectionRecord extends SelectionData {
  id: string
  revision: number
  request_id: string
  created_at: string
  updated_at: string
}

export interface SelectionResearchSample {
  id: string
  title: string
  shop_id: string
  shop_name: string
  source_url: string
  purchase_price: number
  quote_verified_at: string
  minimum_quantity: number
}

export interface SelectionResearchProfile {
  id: string
  name: string
  stage: SelectionStage
  offers: number
  position: string
  subtypes: string
  customer: string
  competition: string
  information: string
  operations: string
  economics: string
  repeat: string
  test: string
  channels: string[]
  examples: SelectionResearchSample[]
  references: { title: string; url: string; fact: string; status: string }[]
}

export interface SelectionResearch {
  version: string
  observed_at: string
  shops: number
  offers: number
  pending: number
  unresolved: number
  sample_count: number
  profiles: SelectionResearchProfile[]
}

export async function listProductSelections(): Promise<SelectionRecord[]> {
  const { data } = await apiClient.get<{ records: SelectionRecord[] }>('/user/product-selections')
  return data.records
}

export async function createProductSelection(record: SelectionData, requestID: string): Promise<{ record: SelectionRecord; created: boolean }> {
  const { data } = await apiClient.post<{ record: SelectionRecord; created: boolean }>('/user/product-selections', { request_id: requestID, record })
  return data
}

export async function updateProductSelection(id: string, revision: number, record: SelectionData): Promise<SelectionRecord> {
  const { data } = await apiClient.put<SelectionRecord>(`/user/product-selections/${encodeURIComponent(id)}`, { revision, record })
  return data
}

export async function getProductSelectionResearch(): Promise<SelectionResearch> {
  const { data } = await apiClient.get<SelectionResearch>('/admin/public-account-import/selection-research')
  return data
}
