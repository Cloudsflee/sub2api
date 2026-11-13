<template>
  <span class="mt-1.5 block text-xs leading-5" data-shop-schedule>
    <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span
        class="inline-flex rounded px-1.5 py-0.5 text-[11px] font-medium"
        :class="isSizeTier ? 'bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'"
        :title="t('publicAccountImport.shopTierHelp')"
        :data-shop-tier="schedule.tier"
      >{{ tierLabel }}</span>
      <span v-if="schedule.effective_count !== null" class="text-gray-600 dark:text-dark-300">
        {{ t('publicAccountImport.shopEffectiveProducts', { count: schedule.effective_count }) }}
      </span>
      <span v-if="schedule.tier_pending" class="text-amber-700 dark:text-amber-300">
        {{ t('publicAccountImport.shopTierConfirming') }}
      </span>
    </span>
    <span v-if="statusText" class="block text-amber-700 dark:text-amber-300" :data-shop-schedule-state="schedule.state">
      {{ statusText }}
    </span>
    <span v-else class="block text-gray-500 dark:text-dark-400">
      {{ t('publicAccountImport.shopCatalogPeriod', { interval: interval(schedule.catalog_seconds) }) }}
      · {{ schedule.quote_seconds > 0
        ? t('publicAccountImport.shopQuotePeriod', { interval: interval(schedule.quote_seconds) })
        : t('publicAccountImport.shopNoAutomaticQuotes') }}
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PublicAccountImportShopSchedule } from '@/api/publicAccountImport'

const props = defineProps<{ schedule: PublicAccountImportShopSchedule }>()
const { t } = useI18n()
const isSizeTier = computed(() => ['1-10', '11-50', '51-200', '201-1000'].includes(props.schedule.tier))
const tierLabel = computed(() => isSizeTier.value
  ? t('publicAccountImport.shopSizeTier', { range: props.schedule.tier.replace('-', '–') })
  : t(`publicAccountImport.shopTier_${props.schedule.tier}`))
const statusText = computed(() => ['active', 'dormant'].includes(props.schedule.state)
  ? '' : t(`publicAccountImport.shopSchedule_${props.schedule.state}`))

function interval(seconds: number): string {
  if (seconds >= 86400) return t('publicAccountImport.shopIntervalDays', { count: Number((seconds / 86400).toFixed(1)) })
  if (seconds >= 3600) return t('publicAccountImport.shopIntervalHours', { count: Number((seconds / 3600).toFixed(1)) })
  return t('publicAccountImport.shopIntervalMinutes', { count: Math.max(1, Math.ceil(seconds / 60)) })
}
</script>
