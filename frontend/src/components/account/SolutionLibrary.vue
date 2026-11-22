<template>
  <div class="space-y-4">
    <h2 class="text-lg font-semibold">{{ t('solutions.library') }}</h2>
    <p v-if="error" role="alert" class="text-red-600">{{ t('solutions.loadFailed') }} <button class="underline" @click="load">{{ t('solutions.retry') }}</button></p>
    <p v-else-if="loading" role="status">{{ t('solutions.loading') }}</p>
    <template v-else>
      <SolutionAudit v-if="version" :key="version" :version="version" />
      <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <input v-model.trim="search" type="search" class="input" :aria-label="t('solutions.search')" :placeholder="t('solutions.search')" />
        <select v-model="type" :aria-label="t('solutions.allTypes')" class="input"><option value="">{{ t('solutions.allTypes') }}</option><option v-for="v in types" :key="v">{{ v }}</option></select>
        <select v-model="channel" :aria-label="t('productSelection.channels')" class="input"><option value="">{{ t('productSelection.allChannels') }}</option><option v-for="v in selectionChannels" :key="v" :value="v">{{ t('productSelection.channelNames.' + v) }}</option></select>
        <select v-model="status" :aria-label="t('solutions.allStates')" class="input"><option value="">{{ t('solutions.allStates') }}</option><option v-for="v in states" :key="v" :value="v">{{ t('solutions.' + v) }}</option></select>
      </div>
      <p v-if="!filtered.length" class="py-6 text-center text-gray-500">{{ t(rows.length ? 'solutions.noMatch' : 'solutions.empty') }}</p>
      <SolutionCard v-for="row in filtered" :key="row.id" :row="row" @saved="load(false)" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { listSolutions, stages, type SolutionSummary } from '@/api/solutionLibrary'
import { selectionChannels, type SelectionChannel } from '@/api/productSelections'
import SolutionCard from './SolutionCard.vue'
import SolutionAudit from './SolutionAudit.vue'
const { t } = useI18n()
const rows = ref<SolutionSummary[]>([])
const version = ref('')
const search = ref('')
const type = ref('')
const channel = ref<SelectionChannel | ''>('')
const status = ref('')
const states = [...stages, 'incomplete', 'needsReview', 'retired', 'untested', 'verified', 'failed']
const types = computed(() => [...new Set(rows.value.flatMap(r => r.types))].sort())
const filtered = computed(() => rows.value.filter(r =>
  (!search.value || [r.name,r.purpose,r.prerequisite,...r.parts].join(' ').toLocaleLowerCase().includes(search.value.toLocaleLowerCase()))
  && (!type.value || r.types.includes(type.value)) && (!channel.value || r.channels.includes(channel.value))
  && (!status.value || [r.stage, r.delivery, r.incomplete && 'incomplete', r.review && 'needsReview', r.retired && 'retired'].includes(status.value))))
const loading = ref(true)
const error = ref(false)
let generation = 0
async function load(spinner: boolean | Event = true) {
  const sequence = ++generation
  if (spinner !== false) loading.value = true
  error.value = false
  try {
    const result = await listSolutions()
    if (sequence === generation) { rows.value = result.solutions; version.value = result.version }
  } catch { if (sequence === generation) error.value = true }
  finally { if (sequence === generation) loading.value = false }
}
onMounted(load)
</script>
