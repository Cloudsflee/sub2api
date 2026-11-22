<template>
  <article class="min-w-0 space-y-2 rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900" :data-solution="row.id">
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="font-semibold">{{ row.name }}</h3>
      <span class="text-xs text-primary-700 dark:text-primary-300">{{ [row.retired && t('solutions.retired'), row.review && t('solutions.needsReview'), row.incomplete && t('solutions.incomplete'), t('solutions.' + row.stage), t('solutions.' + row.delivery)].filter(Boolean).join(' · ') }}</span>
    </div>
    <p class="text-sm">{{ row.purpose }}</p>
    <p class="text-sm text-gray-600 dark:text-dark-300">{{ row.parts.join(' · ') }}</p>
    <p class="text-sm">{{ t('solutions.baseCost') }} {{ money(row.base_cost) }} · {{ t('solutions.target') }} {{ money(row.target) }}</p>
    <p class="text-xs text-gray-600 dark:text-dark-300">{{ t('solutions.prerequisite') }} · {{ row.prerequisite }}</p>
    <p class="text-xs text-amber-700 dark:text-amber-300">{{ t('solutions.gap') }} · {{ row.gap }}</p>
    <button type="button" class="text-sm font-medium text-primary-700 dark:text-primary-300" :aria-expanded="open" @click="toggle">{{ t(open ? 'solutions.collapse' : 'solutions.expand') }}</button>
    <div v-if="open" class="space-y-3 border-t pt-3 text-sm dark:border-dark-700">
      <p v-if="error" role="alert">{{ t('solutions.loadFailed') }} <button class="underline" @click="load">{{ t('solutions.retry') }}</button></p>
      <p v-else-if="!d">{{ t('solutions.loading') }}</p>
      <template v-if="d && s">
        <p>{{ s.customer }} · {{ s.difference }}</p>
        <details open><summary>{{ t('solutions.journey') }}</summary><dl class="space-y-2 pl-3 pt-2"><div v-for="key in journeyFields" :key="key"><dt>{{ t('solutions.'+key) }}</dt><dd>{{ s.journey[key] }}</dd></div></dl></details>
        <details><summary>{{ t('solutions.problems') }} ({{ s.problems.length }})</summary>
          <div v-for="(p,i) in s.problems" :key="i" class="ml-3 space-y-1 border-l pl-3 pt-2">
            <h4 class="font-medium">{{ p.trigger }}</h4>
            <p v-for="key in ['check','action','fallback'] as const" :key="key">{{ t('solutions.'+key) }} · {{ p[key] }}</p>
            <p>{{ s.components.find(c => c.id === p.component)?.name }} · {{ t('solutions.'+p.status) }}</p>
            <p v-if="p.offer">{{ t('solutions.termsSource') }} · {{ byId.get(p.offer)?.name }} ({{ p.offer }})</p>
          </div>
        </details>
        <details><summary>{{ t('solutions.components') }} ({{ s.components.length }})</summary>
          <div v-for="item in s.components" :key="item.id" class="ml-3 space-y-1 py-2">
            <h4 class="font-semibold">{{ item.name }} · {{ t('solutions.'+item.role) }}</h4>
            <p>{{ item.condition }} · {{ t('solutions.'+item.status) }}</p>
            <p v-if="item.depends_on?.length">{{ t('solutions.depends') }} · {{ item.depends_on.map(id => s?.components.find(c => c.id === id)?.name).join('、') }}</p>
            <details v-if="item.offers?.length" @toggle="openSources($event, item.id)"><summary>{{ t('solutions.sources') }} ({{ item.offers.length }})</summary>
              <template v-if="sourceGroups.has(item.id)"><div v-for="id in item.offers.slice(0, shown[item.id] || 10)" :key="id" class="space-y-1 border-l pl-3 pt-3">
                <template v-if="byId.get(id)"><a :href="sanitizeUrl(byId.get(id)!.url)" target="_blank" rel="noopener noreferrer nofollow">{{ byId.get(id)!.shop }} · {{ byId.get(id)!.name }}</a>
                  <p>{{ t('solutions.purchaseLot') }} {{ byId.get(id)!.minimum }} · {{ money(byId.get(id)!.cost) }} · {{ byId.get(id)!.quote_at || t('solutions.unknown') }}</p>
                  <p>{{ t('solutions.catalogStatus') }} · {{ t('solutions.' + (payload?.availability[id]?.state || 'unknown')) }}<span v-if="payload?.availability[id]?.state === 'changed'"> · {{ money(payload?.availability[id]?.cost) }} · {{ payload?.availability[id]?.quote_at }}</span></p>
                  <details><summary>{{ t('solutions.original') }} · {{ t(`solutions.detail_${byId.get(id)!.detail_state}`) }}</summary><p class="break-words whitespace-pre-wrap">{{ byId.get(id)!.original || byId.get(id)!.detail_reason }}</p><small>{{ id }} · {{ byId.get(id)!.detail_at || '—' }}</small></details>
                </template>
              </div>
              <button v-if="item.offers.length > (shown[item.id] || 10)" class="underline" @click="shown[item.id] = (shown[item.id] || 10) + 25">{{ t('solutions.more') }}</button></template>
            </details>
          </div>
        </details>
        <details><summary>{{ t('solutions.commercial') }}</summary><dl class="space-y-2 pl-3 pt-2"><div v-for="key in commercialFields" :key="key"><dt>{{ t('solutions.'+key) }}</dt><dd>{{ s.commercial[key] }}</dd></div><div><dt>{{ t('solutions.observed') }}</dt><dd>{{ money(s.observed) }}</dd></div></dl></details>
        <details><summary>{{ t('solutions.evidence') }}</summary>
          <p class="mt-2">{{ t('solutions.snapshot') }} {{ d.snapshot_at }} · {{ t('solutions.analysis') }} {{ d.analyzed_at }} · {{ d.version }}</p>
          <div v-for="e in d.evidence" :key="e.id" class="mt-2"><p>{{ e.fact }}</p><a :href="sanitizeUrl(e.url)" target="_blank" rel="noopener noreferrer">{{ e.title }}</a> · {{ t('solutions.'+e.kind) }} · {{ e.at }}</div>
        </details>
        <p v-if="d.notes" class="whitespace-pre-wrap">{{ t('solutions.notes') }} · {{ d.notes }}</p>
        <button class="btn btn-secondary" type="button" @click="editing = true">{{ t('solutions.edit') }}</button>
        <SolutionEditor v-if="editing" :detail="d" @cancel="editing = false" @saved="afterSave" />
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { commercialFields, journeyFields, getSolution, type SolutionSummary, type LibraryDetail } from '@/api/solutionLibrary'
import { sanitizeUrl } from '@/utils/url'
import SolutionEditor from './SolutionEditor.vue'
const props = defineProps<{ row: SolutionSummary }>()
const emit = defineEmits<{ (e: 'saved'): void }>()
const { t } = useI18n()
const open = ref(false)
const editing = ref(false)
const payload = ref<LibraryDetail>()
const error = ref(false)
const busy = ref(false)
const d = computed(() => payload.value?.detail)
const s = computed(() => d.value?.solution)
const sourceGroups = reactive(new Set<string>())
function openSources(event: Event, id: string) { if ((event.target as HTMLDetailsElement).open) sourceGroups.add(id) }
const shown = reactive<Record<string, number>>({})
const byId = computed(() => new Map(d.value?.offers.map(o => [o.id,o])))
const money = (n?: number | null) => n == null ? t('solutions.unknown') : '¥' + n.toFixed(2)
async function load() {
  if (busy.value) return
  busy.value = true; error.value = false
  try { payload.value = await getSolution(props.row.id) } catch { error.value = true }
  finally { busy.value = false }
}
function toggle() { open.value = !open.value; if (open.value && !d.value) void load() }
function afterSave() { editing.value = false; void load(); emit('saved') }
</script>

<style scoped>
summary { cursor: pointer; font-weight: 600; }
dt { font-weight: 600; }
dd { white-space: pre-line; }
a { text-decoration: underline; overflow-wrap: anywhere; }
</style>
