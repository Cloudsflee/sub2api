<template>
  <section class="min-w-0 rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900" :data-choice-group="row.id">
    <div class="flex flex-wrap items-baseline justify-between gap-2 px-4 py-3">
      <h3 class="font-semibold">{{ row.name }} <span class="ml-1 text-xs font-normal text-gray-500">{{ t('solutions.delivery') }} · {{ t('solutions.' + row.delivery) }}</span></h3>
      <button type="button" class="text-sm font-medium text-primary-700 underline dark:text-primary-300" :aria-controls="'service-' + row.id" @click="emit('service')">{{ t('solutions.toService') }}</button>
    </div>
    <table class="block w-full text-left text-sm sm:table sm:table-fixed">
      <caption class="sr-only">{{ row.name }} · {{ t('solutions.productChoices') }}</caption>
      <thead class="sr-only bg-gray-50 text-gray-600 sm:not-sr-only sm:table-header-group dark:bg-dark-800 dark:text-dark-300">
        <tr><th scope="col" class="sm:w-[29%]">{{ t('solutions.choiceProduct') }}</th><th scope="col" class="sm:w-[31%]">{{ t('solutions.choiceProblem') }}</th><th scope="col">{{ t('solutions.choicePairing') }}</th></tr>
      </thead>
      <tbody class="block sm:table-row-group">
        <tr v-for="choice in row.choices" :key="choice.role + '/' + choice.offer_id + '/' + choice.name" class="block border-t border-gray-200 px-4 py-3 sm:table-row dark:border-dark-700">
          <th scope="row" class="block font-medium sm:table-cell">
            <span class="mr-2 text-xs font-normal text-primary-700 dark:text-primary-300">{{ t('solutions.choiceRoles.' + choice.role) }}</span>
            <span :title="choice.listing">{{ choice.name }}</span>
            <span v-if="choice.status === 'missing' || choice.status === 'merchant'" class="block text-xs font-normal text-gray-500">{{ t('solutions.' + choice.status) }}</span>
          </th>
          <td class="block sm:table-cell"><span class="mobile-label">{{ t('solutions.choiceProblem') }}</span>{{ choice.solves }}</td>
          <td class="block sm:table-cell"><span class="mobile-label">{{ t('solutions.choicePairing') }}</span>{{ choice.condition }}</td>
        </tr>
      </tbody>
    </table>
    <p v-if="!row.choices?.length" class="px-4 pb-3 text-sm">{{ t('solutions.noChoices') }}</p>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SolutionSummary } from '@/api/solutionLibrary'

defineProps<{ row: SolutionSummary }>()
const emit = defineEmits<{ service: [] }>()
const { t } = useI18n()
</script>

<style scoped>
th, td { vertical-align: top; overflow-wrap: anywhere; }
.mobile-label { display: block; margin: .375rem 0 .125rem; font-size: .75rem; opacity: .75; }
@media (min-width: 640px) {
  th, td { padding: .6rem 1rem; }
  .mobile-label { display: none; }
}
</style>
