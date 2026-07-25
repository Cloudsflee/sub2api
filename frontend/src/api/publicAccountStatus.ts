import { apiClient } from './client'
import type { GroupPlatform } from '@/types'

export type PublicAccountStatusCategory =
  | 'error'
  | 'inactive'
  | 'expired'
  | 'overloaded'
  | 'rate_limited'
  | 'temporarily_unavailable'
  | 'quota_exhausted'
  | 'paused'
  | 'model_limited'
  | 'available'

export interface PublicAccountStatusSummary {
  total: number
  statuses: Record<PublicAccountStatusCategory, number>
}

export interface PublicAccountStatusGroup {
  id: number
  name: string
  description?: string
  platform: GroupPlatform
  status: string
  status_summary: PublicAccountStatusSummary
}

export interface PublicWindowStats {
  requests: number
  tokens: number
  cost: number
  standard_cost: number
  user_cost: number
}

export interface PublicUsageProgress {
  utilization: number
  resets_at?: string | null
  remaining_seconds: number
  window_stats?: PublicWindowStats
  used_requests?: number
  limit_requests?: number
}

export interface PublicAPIKeyQuotaWindow {
  limit: number
  used: number
  reset_at?: string | null
}

export interface PublicAPIKeyQuota {
  total?: PublicAPIKeyQuotaWindow
  daily?: PublicAPIKeyQuotaWindow
  weekly?: PublicAPIKeyQuotaWindow
}

export interface PublicModelQuota {
  utilization: number
  reset_time: string
}

export interface PublicAICredit {
  credit_type?: string
  amount?: number
  minimum_balance?: number
}

export interface PublicGrokQuotaWindow {
  limit?: number
  remaining?: number
  reset_unix?: number
  reset_at?: string
}

export interface PublicGrokBillingProduct {
  product: string
  usage_percent?: number
}

export interface PublicGrokBilling {
  period_type?: string
  usage_percent?: number
  period_start?: string
  period_end?: string
  product_usage?: PublicGrokBillingProduct[]
  monthly_limit_cents?: number
  used_cents?: number
  included_used_cents?: number
  billing_period_start?: string
  billing_period_end?: string
  used_percent?: number
  plan?: string
  status_code?: number
  weekly_status_code?: number
  monthly_status_code?: number
  source?: string
  fetched_at?: string
  updated_at?: string
  weekly_updated_at?: string
  monthly_updated_at?: string
  partial?: boolean
  failed_windows?: string[]
}

export interface PublicGrokUsage {
  request_quota?: PublicGrokQuotaWindow
  token_quota?: PublicGrokQuotaWindow
  retry_after_seconds?: number
  entitlement_status?: string
  snapshot_state?: string
  last_quota_probe_at?: string
  last_headers_seen_at?: string
  free_token_limit?: number
  local_usage?: PublicWindowStats
  local_usage_24h?: PublicWindowStats
  local_usage_7d?: PublicWindowStats
  local_usage_monthly?: PublicWindowStats
  billing?: PublicGrokBilling
}

export interface PublicAccountUsageSnapshot {
  source?: string
  updated_at?: string | null
  five_hour?: PublicUsageProgress
  seven_day?: PublicUsageProgress
  seven_day_sonnet?: PublicUsageProgress
  seven_day_fable?: PublicUsageProgress
  gemini_shared_daily?: PublicUsageProgress
  gemini_pro_daily?: PublicUsageProgress
  gemini_flash_daily?: PublicUsageProgress
  gemini_shared_minute?: PublicUsageProgress
  gemini_pro_minute?: PublicUsageProgress
  gemini_flash_minute?: PublicUsageProgress
  api_key_quota?: PublicAPIKeyQuota
  model_quotas?: Record<string, PublicModelQuota>
  subscription_tier?: string
  ai_credits?: PublicAICredit[]
  grok?: PublicGrokUsage
}

export interface PublicAccountStatusAccount {
  name: string
  platform: GroupPlatform
  type: string
  status: PublicAccountStatusCategory
  recovery_at?: string | null
  current_concurrency: number
  max_concurrency: number
  last_used_at?: string | null
  updated_at: string
  expires_at?: string | null
  usage?: PublicAccountUsageSnapshot
}

export interface PublicAccountStatusPage {
  items: PublicAccountStatusAccount[]
  total: number
  page: number
  page_size: number
  pages: number
}

export async function listPublicAccountStatusGroups(
  signal?: AbortSignal
): Promise<PublicAccountStatusGroup[]> {
  const { data } = await apiClient.get<PublicAccountStatusGroup[]>(
    '/public/account-status/groups',
    { signal }
  )
  return data
}

export async function listPublicAccountStatusAccounts(
  groupId: number,
  page = 1,
  pageSize = 20,
  signal?: AbortSignal
): Promise<PublicAccountStatusPage> {
  const { data } = await apiClient.get<PublicAccountStatusPage>(
    `/public/account-status/groups/${groupId}/accounts`,
    {
      params: { page, page_size: pageSize },
      signal
    }
  )
  return data
}
