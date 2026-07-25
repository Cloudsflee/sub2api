<template>
  <div class="bg-gray-50 px-4 py-5 dark:bg-dark-900/60 sm:px-6">
    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('publicAccountStatus.usage.snapshot') }}
      </h3>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
        <span v-if="usage.source">
          {{ t('publicAccountStatus.usage.source') }}: {{ usage.source }}
        </span>
        <span v-if="usage.updated_at">
          {{ t('publicAccountStatus.usage.sampledAt') }}: {{ formatTime(usage.updated_at) }}
        </span>
      </div>
    </div>

    <section v-if="windowRows.length" class="usage-section">
      <h4 class="usage-section-title">{{ t('publicAccountStatus.usage.windows') }}</h4>
      <div class="grid gap-3 lg:grid-cols-2 xl:grid-cols-3">
        <div
          v-for="window in windowRows"
          :key="window.key"
          class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="flex items-center justify-between gap-3">
            <span class="text-sm font-medium text-gray-800 dark:text-dark-100">{{ window.label }}</span>
            <span class="font-mono text-sm font-semibold text-gray-900 dark:text-white">
              {{ formatPercent(window.value.utilization) }}
            </span>
          </div>
          <div class="mt-3 h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
            <div
              class="h-full rounded-full transition-[width]"
              :class="progressClass(window.value.utilization)"
              :style="{ width: progressWidth(window.value.utilization) }"
            ></div>
          </div>
          <div class="mt-3 space-y-1 text-xs text-gray-500 dark:text-dark-400">
            <div v-if="window.value.resets_at" class="flex justify-between gap-3">
              <span>{{ t('publicAccountStatus.usage.resetsAt') }}</span>
              <span class="text-right font-medium text-gray-700 dark:text-dark-200">
                {{ formatTime(window.value.resets_at) }}
              </span>
            </div>
            <div
              v-if="window.value.limit_requests"
              class="flex justify-between gap-3"
            >
              <span>{{ t('publicAccountStatus.usage.requests') }}</span>
              <span class="font-medium text-gray-700 dark:text-dark-200">
                {{
                  t('publicAccountStatus.usage.requestUsage', {
                    used: formatCount(window.value.used_requests ?? 0),
                    limit: formatCount(window.value.limit_requests)
                  })
                }}
              </span>
            </div>
          </div>
          <div
            v-if="window.value.window_stats"
            class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 border-t border-gray-100 pt-3 text-xs dark:border-dark-800"
          >
            <UsageMetric
              :label="t('publicAccountStatus.usage.requests')"
              :value="formatCount(window.value.window_stats.requests)"
            />
            <UsageMetric
              :label="t('publicAccountStatus.usage.tokens')"
              :value="formatCount(window.value.window_stats.tokens)"
            />
            <UsageMetric
              :label="t('publicAccountStatus.usage.cost')"
              :value="formatMoney(window.value.window_stats.cost)"
            />
            <UsageMetric
              :label="t('publicAccountStatus.usage.standardCost')"
              :value="formatMoney(window.value.window_stats.standard_cost)"
            />
            <UsageMetric
              class="col-span-2"
              :label="t('publicAccountStatus.usage.userCost')"
              :value="formatMoney(window.value.window_stats.user_cost)"
            />
          </div>
        </div>
      </div>
    </section>

    <section v-if="apiQuotaRows.length" class="usage-section">
      <h4 class="usage-section-title">{{ t('publicAccountStatus.usage.apiKeyQuota') }}</h4>
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div
          v-for="quota in apiQuotaRows"
          :key="quota.key"
          class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="flex items-center justify-between gap-3 text-sm">
            <span class="font-medium text-gray-700 dark:text-dark-200">{{ quota.label }}</span>
            <span class="font-mono font-semibold text-gray-900 dark:text-white">
              {{ formatMoney(quota.value.used) }} / {{ formatMoney(quota.value.limit) }}
            </span>
          </div>
          <div class="mt-3 h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
            <div
              class="h-full rounded-full"
              :class="progressClass(quotaPercent(quota.value))"
              :style="{ width: progressWidth(quotaPercent(quota.value)) }"
            ></div>
          </div>
          <div v-if="quota.value.reset_at" class="mt-2 text-xs text-gray-500 dark:text-dark-400">
            {{ t('publicAccountStatus.usage.resetsAt') }}: {{ formatTime(quota.value.reset_at) }}
          </div>
        </div>
      </div>
    </section>

    <section v-if="modelQuotaRows.length" class="usage-section">
      <h4 class="usage-section-title">{{ t('publicAccountStatus.usage.modelQuotas') }}</h4>
      <div class="divide-y divide-gray-100 border-y border-gray-200 dark:divide-dark-800 dark:border-dark-700">
        <div
          v-for="quota in modelQuotaRows"
          :key="quota.name"
          class="grid gap-2 py-3 text-sm sm:grid-cols-[minmax(0,1fr)_8rem_minmax(10rem,auto)] sm:items-center"
        >
          <span class="break-all font-medium text-gray-800 dark:text-dark-100">{{ quota.name }}</span>
          <span class="font-mono text-gray-700 dark:text-dark-200">{{ formatPercent(quota.value.utilization) }}</span>
          <span class="text-gray-500 dark:text-dark-400">
            {{ t('publicAccountStatus.usage.resetsAt') }}: {{ formatTime(quota.value.reset_time) }}
          </span>
        </div>
      </div>
    </section>

    <section v-if="usage.subscription_tier || usage.ai_credits?.length" class="usage-section">
      <h4 class="usage-section-title">{{ t('publicAccountStatus.usage.subscription') }}</h4>
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div
          v-if="usage.subscription_tier"
          class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.usage.tier') }}</div>
          <div class="mt-1 font-semibold text-gray-900 dark:text-white">{{ usage.subscription_tier }}</div>
        </div>
        <div
          v-for="(credit, index) in usage.ai_credits"
          :key="`${credit.credit_type || 'credit'}-${index}`"
          class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="text-xs text-gray-500 dark:text-dark-400">
            {{ credit.credit_type || t('publicAccountStatus.usage.aiCredits') }}
          </div>
          <div class="mt-1 font-mono font-semibold text-gray-900 dark:text-white">
            {{ t('publicAccountStatus.usage.creditBalance', { amount: formatNumberValue(credit.amount) }) }}
          </div>
          <div v-if="credit.minimum_balance !== undefined" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('publicAccountStatus.usage.minimumBalance', { amount: formatNumberValue(credit.minimum_balance) }) }}
          </div>
        </div>
      </div>
    </section>

    <section v-if="usage.grok" class="usage-section">
      <h4 class="usage-section-title">{{ t('publicAccountStatus.usage.grok') }}</h4>

      <div v-if="grokQuotaRows.length" class="grid gap-3 sm:grid-cols-2">
        <div
          v-for="quota in grokQuotaRows"
          :key="quota.key"
          class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="text-sm font-medium text-gray-800 dark:text-dark-100">{{ quota.label }}</div>
          <div class="mt-1 font-mono text-sm font-semibold text-gray-900 dark:text-white">
            {{ formatGrokQuota(quota.value) }}
          </div>
          <div v-if="grokResetTime(quota.value)" class="mt-2 text-xs text-gray-500 dark:text-dark-400">
            {{ t('publicAccountStatus.usage.resetsAt') }}: {{ grokResetTime(quota.value) }}
          </div>
        </div>
      </div>

      <div
        v-if="grokMetadata.length"
        class="mt-3 grid gap-x-8 gap-y-2 border-y border-gray-200 py-3 text-xs dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-3"
      >
        <UsageMetric
          v-for="item in grokMetadata"
          :key="item.label"
          :label="item.label"
          :value="item.value"
        />
      </div>

      <div v-if="grokLocalRows.length" class="mt-4">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div
            v-for="row in grokLocalRows"
            :key="row.key"
            class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
          >
            <div class="mb-3 text-sm font-medium text-gray-800 dark:text-dark-100">{{ row.label }}</div>
            <div class="grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
              <UsageMetric :label="t('publicAccountStatus.usage.requests')" :value="formatCount(row.value.requests)" />
              <UsageMetric :label="t('publicAccountStatus.usage.tokens')" :value="formatCount(row.value.tokens)" />
              <UsageMetric :label="t('publicAccountStatus.usage.cost')" :value="formatMoney(row.value.cost)" />
              <UsageMetric :label="t('publicAccountStatus.usage.standardCost')" :value="formatMoney(row.value.standard_cost)" />
              <UsageMetric class="col-span-2" :label="t('publicAccountStatus.usage.userCost')" :value="formatMoney(row.value.user_cost)" />
            </div>
          </div>
        </div>
      </div>

      <div v-if="usage.grok.billing" class="mt-5 border-t border-gray-200 pt-4 dark:border-dark-700">
        <h5 class="mb-3 text-xs font-semibold uppercase text-gray-500 dark:text-dark-400">
          {{ t('publicAccountStatus.usage.billing') }}
        </h5>
        <div class="grid gap-x-8 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-3">
          <UsageMetric
            v-for="item in billingMetadata"
            :key="item.label"
            :label="item.label"
            :value="item.value"
          />
        </div>
        <div
          v-if="usage.grok.billing.product_usage?.length"
          class="mt-4 divide-y divide-gray-100 border-y border-gray-200 dark:divide-dark-800 dark:border-dark-700"
        >
          <div
            v-for="product in usage.grok.billing.product_usage"
            :key="product.product"
            class="flex items-center justify-between gap-4 py-2.5 text-xs"
          >
            <span class="break-all text-gray-700 dark:text-dark-200">{{ product.product }}</span>
            <span class="font-mono font-medium text-gray-900 dark:text-white">
              {{ product.usage_percent === undefined ? '-' : formatPercent(product.usage_percent) }}
            </span>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatCurrency, formatDateTimeToMinute, formatNumber } from '@/utils/format'
import type {
  PublicAPIKeyQuotaWindow,
  PublicAccountUsageSnapshot,
  PublicGrokQuotaWindow,
  PublicUsageProgress,
  PublicWindowStats
} from '@/api/publicAccountStatus'

const props = defineProps<{ usage: PublicAccountUsageSnapshot }>()
const { t } = useI18n()

const UsageMetric = defineComponent({
  name: 'UsageMetric',
  props: {
    label: { type: String, required: true },
    value: { type: String, required: true }
  },
  setup(metricProps, { attrs }) {
    return () =>
      h('div', { ...attrs, class: ['min-w-0', attrs.class] }, [
        h('div', { class: 'truncate text-gray-500 dark:text-dark-400', title: metricProps.label }, metricProps.label),
        h('div', { class: 'mt-0.5 break-words font-mono font-medium text-gray-900 dark:text-white' }, metricProps.value)
      ])
  }
})

const windowDefinitions = [
  ['five_hour', 'fiveHour'],
  ['seven_day', 'sevenDay'],
  ['seven_day_sonnet', 'sevenDaySonnet'],
  ['seven_day_fable', 'sevenDayFable'],
  ['gemini_shared_daily', 'geminiSharedDaily'],
  ['gemini_pro_daily', 'geminiProDaily'],
  ['gemini_flash_daily', 'geminiFlashDaily'],
  ['gemini_shared_minute', 'geminiSharedMinute'],
  ['gemini_pro_minute', 'geminiProMinute'],
  ['gemini_flash_minute', 'geminiFlashMinute']
] as const

const windowRows = computed(() =>
  windowDefinitions.flatMap(([key, label]) => {
    const value = props.usage[key] as PublicUsageProgress | undefined
    return value
      ? [{ key, label: t(`publicAccountStatus.usage.${label}`), value }]
      : []
  })
)

const apiQuotaRows = computed(() => {
  const quota = props.usage.api_key_quota
  if (!quota) return []
  return [
    { key: 'total', label: t('publicAccountStatus.usage.totalQuota'), value: quota.total },
    { key: 'daily', label: t('publicAccountStatus.usage.dailyQuota'), value: quota.daily },
    { key: 'weekly', label: t('publicAccountStatus.usage.weeklyQuota'), value: quota.weekly }
  ].filter((item): item is { key: string; label: string; value: PublicAPIKeyQuotaWindow } => Boolean(item.value))
})

const modelQuotaRows = computed(() =>
  Object.entries(props.usage.model_quotas ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => ({ name, value }))
)

const grokQuotaRows = computed(() => {
  const grok = props.usage.grok
  if (!grok) return []
  return [
    { key: 'requests', label: t('publicAccountStatus.usage.requestQuota'), value: grok.request_quota },
    { key: 'tokens', label: t('publicAccountStatus.usage.tokenQuota'), value: grok.token_quota }
  ].filter((item): item is { key: string; label: string; value: PublicGrokQuotaWindow } => Boolean(item.value))
})

const grokMetadata = computed(() => {
  const grok = props.usage.grok
  if (!grok) return []
  return compactMetadata([
    [t('publicAccountStatus.usage.retryAfterLabel'), grok.retry_after_seconds === undefined ? '' : t('publicAccountStatus.usage.retryAfter', { seconds: grok.retry_after_seconds })],
    [t('publicAccountStatus.usage.entitlement'), grok.entitlement_status],
    [t('publicAccountStatus.usage.snapshotState'), grok.snapshot_state],
    [t('publicAccountStatus.usage.lastProbe'), formatTime(grok.last_quota_probe_at)],
    [t('publicAccountStatus.usage.lastHeaders'), formatTime(grok.last_headers_seen_at)],
    [t('publicAccountStatus.usage.freeTokenLimit'), grok.free_token_limit === undefined ? '' : formatCount(grok.free_token_limit)]
  ])
})

const grokLocalRows = computed(() => {
  const grok = props.usage.grok
  if (!grok) return []
  return [
    { key: 'today', label: t('publicAccountStatus.usage.localToday'), value: grok.local_usage },
    { key: '24h', label: t('publicAccountStatus.usage.local24h'), value: grok.local_usage_24h },
    { key: '7d', label: t('publicAccountStatus.usage.local7d'), value: grok.local_usage_7d },
    { key: 'monthly', label: t('publicAccountStatus.usage.localMonthly'), value: grok.local_usage_monthly }
  ].filter((item): item is { key: string; label: string; value: PublicWindowStats } => Boolean(item.value))
})

const billingMetadata = computed(() => {
  const billing = props.usage.grok?.billing
  if (!billing) return []
  const period = joinPeriod(billing.period_start, billing.period_end)
  const billingPeriod = joinPeriod(billing.billing_period_start, billing.billing_period_end)
  return compactMetadata([
    [t('publicAccountStatus.usage.plan'), billing.plan],
    [t('publicAccountStatus.usage.billingPeriod'), period || billingPeriod || billing.period_type],
    [t('publicAccountStatus.usage.usedPercent'), percentageValue(billing.used_percent ?? billing.usage_percent)],
    [t('publicAccountStatus.usage.monthlyLimit'), centsValue(billing.monthly_limit_cents)],
    [t('publicAccountStatus.usage.usedAmount'), centsValue(billing.used_cents)],
    [t('publicAccountStatus.usage.includedUsed'), centsValue(billing.included_used_cents)],
    [t('publicAccountStatus.usage.statusCode'), numberValue(billing.status_code)],
    [`${t('publicAccountStatus.usage.statusCode')} (weekly)`, numberValue(billing.weekly_status_code)],
    [`${t('publicAccountStatus.usage.statusCode')} (monthly)`, numberValue(billing.monthly_status_code)],
    [t('publicAccountStatus.usage.sourceLabel'), billing.source],
    [t('publicAccountStatus.usage.sampledAt'), formatTime(billing.fetched_at || billing.updated_at)],
    [t('publicAccountStatus.usage.weeklyUpdated'), formatTime(billing.weekly_updated_at)],
    [t('publicAccountStatus.usage.monthlyUpdated'), formatTime(billing.monthly_updated_at)],
    [t('publicAccountStatus.usage.partial'), billing.partial ? t('publicAccountStatus.usage.partial') : ''],
    [t('publicAccountStatus.usage.failedWindows'), billing.failed_windows?.join(', ')]
  ])
})

function compactMetadata(items: Array<[string, string | undefined]>): Array<{ label: string; value: string }> {
  return items.flatMap(([label, value]) => value ? [{ label, value }] : [])
}

function formatTime(value?: string | null): string {
  return value ? formatDateTimeToMinute(value) : ''
}

function formatCount(value: number): string {
  return formatNumber(value)
}

function formatNumberValue(value?: number): string {
  return value === undefined ? '-' : new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(value)
}

function formatMoney(value: number): string {
  return formatCurrency(value)
}

function formatPercent(value: number): string {
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value)}%`
}

function percentageValue(value?: number): string {
  return value === undefined ? '' : formatPercent(value)
}

function numberValue(value?: number): string {
  return value === undefined ? '' : String(value)
}

function centsValue(value?: number): string {
  return value === undefined ? '' : formatMoney(value / 100)
}

function joinPeriod(start?: string, end?: string): string {
  const startValue = formatTime(start)
  const endValue = formatTime(end)
  return startValue && endValue ? `${startValue} - ${endValue}` : startValue || endValue
}

function progressWidth(value: number): string {
  return `${Math.min(Math.max(value, 0), 100)}%`
}

function progressClass(value: number): string {
  if (value >= 100) return 'bg-red-500'
  if (value >= 80) return 'bg-amber-500'
  return 'bg-emerald-500'
}

function quotaPercent(quota: PublicAPIKeyQuotaWindow): number {
  return quota.limit > 0 ? (quota.used / quota.limit) * 100 : 0
}

function formatGrokQuota(quota: PublicGrokQuotaWindow): string {
  if (quota.remaining !== undefined && quota.limit !== undefined) {
    return t('publicAccountStatus.usage.remainingLimit', {
      remaining: formatCount(quota.remaining),
      limit: formatCount(quota.limit)
    })
  }
  if (quota.remaining !== undefined) return formatCount(quota.remaining)
  if (quota.limit !== undefined) return formatCount(quota.limit)
  return '-'
}

function grokResetTime(quota: PublicGrokQuotaWindow): string {
  if (quota.reset_at) return formatTime(quota.reset_at)
  if (quota.reset_unix) return formatTime(new Date(quota.reset_unix * 1000).toISOString())
  return ''
}
</script>

<style scoped>
.usage-section + .usage-section {
  @apply mt-6 border-t border-gray-200 pt-5 dark:border-dark-700;
}

.usage-section-title {
  @apply mb-3 text-xs font-semibold uppercase text-gray-500 dark:text-dark-400;
}
</style>
