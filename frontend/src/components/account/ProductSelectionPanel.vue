<template>
  <section data-testid="product-selection-panel" class="space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-lg font-semibold">{{ t('productSelection.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('productSelection.hint') }}</p>
      </div>
      <button v-if="authenticated" type="button" class="btn btn-primary" data-testid="selection-new" @click="openNew()">{{ t('productSelection.newRecord') }}</button>
    </div>

    <div v-if="!authenticated" class="rounded-xl border border-gray-200 bg-white p-6 dark:border-dark-700 dark:bg-dark-900">
      <p class="mb-4 text-sm text-gray-500 dark:text-dark-400">{{ t('productSelection.loginHint') }}</p>
      <RouterLink :to="{ path: '/login', query: { redirect: '/account-import#selection' } }" class="btn btn-primary">{{ t('productSelection.login') }}</RouterLink>
    </div>
    <template v-else>
      <div class="grid grid-cols-3 gap-3">
        <div v-for="stat in stats" :key="stat.label" class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-900">
          <div class="text-2xl font-semibold">{{ loading ? '—' : stat.value }}</div>
          <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t(`productSelection.${stat.label}`) }}</div>
        </div>
      </div>
      <div v-if="notice" role="status" class="rounded-lg bg-emerald-50 p-3 text-sm text-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300">{{ notice }}</div>
      <div class="flex flex-wrap items-center gap-3 border-b border-gray-200 pb-3 dark:border-dark-700">
        <button type="button" class="rounded-lg px-3 py-2 text-sm font-medium" :class="view === 'records' ? 'bg-primary-50 text-primary-700 dark:bg-primary-950 dark:text-primary-300' : ''" :aria-pressed="view === 'records'" data-testid="selection-records-tab" @click="view = 'records'">{{ t('productSelection.mine') }}</button>
        <button v-if="admin" type="button" class="rounded-lg px-3 py-2 text-sm font-medium" :class="view === 'research' ? 'bg-primary-50 text-primary-700 dark:bg-primary-950 dark:text-primary-300' : ''" :aria-pressed="view === 'research'" data-testid="selection-research-tab" @click="view = 'research'">{{ t('productSelection.research') }}</button>
        <button type="button" class="ml-auto text-sm text-primary-600 dark:text-primary-400" :disabled="loading || saving" @click="load()">{{ t('productSelection.refresh') }}</button>
      </div>
      <p v-if="admin" class="text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.researchPrivate') }}</p>

      <div class="flex flex-col gap-3 sm:flex-row">
        <input v-model="search" type="search" class="input min-w-0 flex-1" :placeholder="t('productSelection.search')" :aria-label="t('productSelection.search')" />
        <select v-model="stageFilter" class="input sm:w-48" :aria-label="t('productSelection.stage')">
          <option value="">{{ t('productSelection.allStages') }}</option>
          <option v-for="stage in selectionStages" :key="stage" :value="stage">{{ t(`productSelection.stages.${stage}`) }}</option>
        </select>
        <select v-if="view === 'records'" v-model="channelFilter" class="input sm:w-40" :aria-label="t('productSelection.channels')">
          <option value="">{{ t('productSelection.allChannels') }}</option>
          <option v-for="channel in selectionChannels" :key="channel" :value="channel">{{ t(`productSelection.channelNames.${channel}`) }}</option>
        </select>
      </div>

      <template v-if="view === 'records'">
        <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300"><input v-model="includeArchived" type="checkbox" class="rounded" />{{ t('productSelection.showArchive') }}</label>
        <p v-if="loadError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ loadError }}</p>
        <p v-if="loading" class="py-8 text-center text-sm text-gray-500">{{ t('productSelection.loading') }}</p>
        <div v-else-if="!loadError && !records.length" class="rounded-xl border border-dashed border-gray-300 p-8 text-center dark:border-dark-600">
          <p class="font-medium">{{ t('productSelection.empty') }}</p><p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('productSelection.emptyHint') }}</p>
        </div>
        <p v-else-if="!loadError && !filteredRecords.length" class="py-8 text-center text-sm text-gray-500">{{ t('productSelection.noResults') }}</p>
        <div class="grid gap-4 sm:grid-cols-2">
          <article v-for="record in pagedRecords" :key="record.id" :data-selection-id="record.id" class="flex flex-col rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <div class="mb-2 flex flex-wrap items-center gap-2"><span class="rounded px-2 py-1 text-xs font-medium" :class="stageClass(record.stage)">{{ t(`productSelection.stages.${record.stage}`) }}</span><span class="text-xs text-gray-500 dark:text-dark-400">{{ record.category }}</span></div>
            <button type="button" class="text-left text-sm font-semibold hover:text-primary-600" @click="openExisting(record)">{{ record.title }}</button>
            <p v-if="record.shop_name" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ record.shop_name }}</p>
            <div class="mt-3 text-sm"><span class="text-gray-500 dark:text-dark-400">{{ t('productSelection.purchasePrice') }}：</span><strong>{{ price(record.purchase_price) }}</strong></div>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t(`productSelection.${record.price_basis}`) }} · {{ t('productSelection.minimum', { count: record.minimum_quantity }) }}</p>
            <p v-if="record.quote_verified_at" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.quoteTime', { time: date(record.quote_verified_at) }) }}</p>
            <p v-if="record.target_sale_price !== null" class="mt-2 text-xs">{{ t('productSelection.targetSalePrice') }}：{{ price(record.target_sale_price) }}</p>
            <p v-if="record.observed_sale_price !== null" class="mt-1 text-xs">{{ t('productSelection.observedSalePrice') }}：{{ price(record.observed_sale_price) }}</p>
            <p v-if="record.decision" class="mt-3 line-clamp-3 whitespace-pre-line text-sm text-gray-600 dark:text-dark-300">{{ record.decision }}</p>
            <p v-if="record.next_action" class="mt-2 line-clamp-2 text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.next_action') }}：{{ record.next_action }}</p>
            <div class="mt-3 flex flex-wrap gap-1.5"><span v-for="channel in record.channels" :key="channel" class="rounded bg-gray-100 px-2 py-1 text-xs dark:bg-dark-800">{{ t(`productSelection.channelNames.${channel}`) }}</span><span v-for="tag in record.tags" :key="tag" class="rounded border border-gray-200 px-2 py-1 text-xs dark:border-dark-700">{{ tag }}</span></div>
            <div class="mt-auto flex flex-wrap items-center gap-3 pt-4 text-xs">
              <button type="button" class="font-medium text-primary-600 dark:text-primary-400" @click="openExisting(record)">{{ t('productSelection.edit') }}</button>
              <a v-if="safeURL(record.source_url)" :href="safeURL(record.source_url)" target="_blank" rel="noopener noreferrer nofollow" class="text-gray-500 hover:text-primary-600">{{ t('productSelection.source') }}</a>
              <button type="button" class="ml-auto text-gray-500 hover:text-primary-600" :disabled="saving" @click="setArchived(record)">{{ t(record.stage === 'archived' ? 'productSelection.restore' : 'productSelection.archive') }}</button>
            </div>
            <p class="mt-3 text-xs text-gray-400">{{ t('productSelection.updated', { time: date(record.updated_at) }) }}</p>
          </article>
        </div>
      </template>

      <template v-else-if="admin">
        <p v-if="researchError" role="alert" class="text-sm text-red-600">{{ researchError }}</p>
        <p v-if="loading && !research" class="py-8 text-center text-sm text-gray-500">{{ t('productSelection.loading') }}</p>
        <template v-if="research">
          <div class="rounded-lg border border-blue-100 bg-blue-50 p-4 text-sm dark:border-blue-900 dark:bg-blue-950/20">
            <p class="font-medium">{{ t('productSelection.researchSummary', { date: research.version, shops: research.shops, offers: research.offers, types: research.profiles.length, samples: research.sample_count }) }}</p>
            <p class="mt-2 text-xs text-gray-600 dark:text-dark-300">{{ t('productSelection.readOnlySnapshot') }}</p>
            <p class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('productSelection.researchLimit', { pending: research.pending, unresolved: research.unresolved }) }}</p>
            <p v-if="locale !== 'zh'" class="mt-1 text-xs">{{ t('productSelection.researchChinese') }}</p>
          </div>
          <p v-if="!filteredProfiles.length" class="py-8 text-center text-sm text-gray-500">{{ t('productSelection.noResults') }}</p>
          <article v-for="profile in pagedProfiles" :key="profile.id" :data-research-id="profile.id" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div><h3 class="font-semibold">{{ profile.name }}</h3><div class="mt-2 flex items-center gap-2"><span class="rounded px-2 py-1 text-xs font-medium" :class="stageClass(profile.stage)">{{ t(`productSelection.stages.${profile.stage}`) }}</span><span class="text-xs text-gray-500">{{ t('productSelection.offers', { count: profile.offers }) }}</span></div></div>
              <button type="button" class="btn btn-secondary text-xs" @click="openResearch(profile)">{{ t('productSelection.addDirection') }}</button>
            </div>
            <p class="mt-3 text-sm text-gray-600 dark:text-dark-300">{{ profile.position }}</p>
            <details class="mt-4 border-t border-gray-100 pt-3 dark:border-dark-800">
              <summary class="cursor-pointer text-sm font-medium text-primary-600 dark:text-primary-400">{{ t('productSelection.details') }}</summary>
              <dl class="mt-4 space-y-4">
                <div v-for="key in analysisFields" :key="key"><dt class="text-xs font-semibold text-gray-500 dark:text-dark-400">{{ t(`productSelection.analysisFields.${key}`) }}</dt><dd class="mt-1 whitespace-pre-line text-sm">{{ profile[key] }}</dd></div>
              </dl>
              <div class="mt-5 grid gap-3 sm:grid-cols-2"><div v-for="(channel, index) in selectionChannels" :key="channel" class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800"><h4 class="mb-1 text-xs font-semibold">{{ t(`productSelection.channelNames.${channel}`) }}</h4>{{ profile.channels[index] }}</div></div>
              <div v-if="profile.references.length" class="mt-4"><h4 class="text-sm font-semibold">{{ t('productSelection.references') }}</h4><ul class="mt-2 space-y-3"><li v-for="source in profile.references" :key="source.url" class="text-xs"><a :href="safeURL(source.url)" target="_blank" rel="noopener noreferrer" class="text-primary-600 underline dark:text-primary-400">{{ source.title }}</a><span class="ml-2 text-gray-500">{{ source.status }}</span><p class="mt-1 text-gray-600 dark:text-dark-300">{{ source.fact }}</p></li></ul></div>
            </details>
            <div v-if="profile.examples.length" class="mt-4 space-y-3 border-t border-gray-100 pt-4 dark:border-dark-800">
              <h4 class="text-xs font-semibold text-gray-500">{{ t('productSelection.samples') }}</h4>
              <div v-for="sample in profile.examples" :key="sample.id" class="flex flex-col gap-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-800 sm:flex-row sm:items-center">
                <div class="min-w-0 flex-1"><a :href="safeURL(sample.source_url)" target="_blank" rel="noopener noreferrer nofollow" class="text-sm font-medium hover:text-primary-600">{{ sample.title }}</a><p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ sample.shop_name }} · {{ price(sample.purchase_price) }} · {{ t('productSelection.minimum', { count: sample.minimum_quantity }) }}</p><p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.quoteTime', { time: date(sample.quote_verified_at) }) }}</p></div>
                <button type="button" class="shrink-0 self-start text-sm font-medium text-primary-600 dark:text-primary-400" :data-add-sample="sample.id" @click="openResearch(profile, sample)">{{ t('productSelection.add') }}</button>
              </div>
            </div>
          </article>
        </template>
      </template>
      <div v-if="pageCount > 1" class="flex items-center justify-between text-sm"><button type="button" class="btn btn-secondary" :disabled="page === 1" @click="page--">{{ t('productSelection.previous') }}</button><span>{{ t('productSelection.page', { page, total: pageCount }) }}</span><button type="button" class="btn btn-secondary" :disabled="page >= pageCount" @click="page++">{{ t('productSelection.next') }}</button></div>
    </template>

    <BaseDialog :show="editorOpen && authenticated" :title="t(editing ? 'productSelection.edit' : 'productSelection.newRecord')" width="extra-wide" :close-on-click-outside="false" :close-on-escape="!saving" :show-close-button="!saving" @close="closeEditor">
      <form id="product-selection-form" class="space-y-5" @submit.prevent="save">
        <p v-if="saveError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ saveError }}</p>
        <button v-if="conflict" type="button" class="btn btn-secondary" :disabled="loading" @click="reloadLatest">{{ t('productSelection.reloadLatest') }}</button>
        <fieldset :disabled="saving" class="space-y-4">
          <legend class="mb-3 text-sm font-semibold">{{ t('productSelection.basic') }}</legend>
          <div><label for="selection-title" class="input-label">{{ t('productSelection.recordTitle') }}</label><input id="selection-title" v-model="form.title" required maxlength="300" class="input" /></div>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="selection-category" class="input-label">{{ t('productSelection.category') }}</label><input id="selection-category" v-model="form.category" maxlength="150" class="input" /></div><div><label for="selection-stage" class="input-label">{{ t('productSelection.stage') }}</label><select id="selection-stage" v-model="form.stage" class="input"><option v-for="stage in selectionStages" :key="stage" :value="stage">{{ t(`productSelection.stages.${stage}`) }}</option></select></div></div>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="selection-shop" class="input-label">{{ t('productSelection.shopName') }}</label><input id="selection-shop" v-model="form.shop_name" maxlength="300" class="input" /></div><div><label for="selection-url" class="input-label">{{ t('productSelection.sourceUrl') }}</label><input id="selection-url" v-model="form.source_url" type="url" maxlength="2048" class="input" /></div></div>
          <div><span class="input-label">{{ t('productSelection.channels') }}</span><div class="flex flex-wrap gap-4"><label v-for="channel in selectionChannels" :key="channel" class="flex items-center gap-2 text-sm"><input v-model="form.channels" type="checkbox" :value="channel" />{{ t(`productSelection.channelNames.${channel}`) }}</label></div></div>
          <div><label for="selection-tags" class="input-label">{{ t('productSelection.tags') }}</label><input id="selection-tags" v-model="tagsInput" class="input" maxlength="500" :placeholder="t('productSelection.tagsHint')" /></div>
        </fieldset>
        <fieldset :disabled="saving" class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700">
          <legend class="pt-4 text-sm font-semibold">{{ t('productSelection.pricing') }}</legend>
          <div class="grid gap-4 sm:grid-cols-2"><div><label for="selection-cost" class="input-label">{{ t('productSelection.purchasePrice') }}</label><input id="selection-cost" v-model="prices.cost" type="number" min="0" max="100000000" step="any" class="input" /></div><div><label for="selection-minimum" class="input-label">{{ t('productSelection.minimumQuantity') }}</label><input id="selection-minimum" v-model.number="form.minimum_quantity" required type="number" min="1" max="1000000" step="1" class="input" /></div><div><label for="selection-target" class="input-label">{{ t('productSelection.targetSalePrice') }}</label><input id="selection-target" v-model="prices.target" type="number" min="0" max="100000000" step="any" class="input" /></div><div><label for="selection-observed" class="input-label">{{ t('productSelection.observedSalePrice') }}</label><input id="selection-observed" v-model="prices.observed" type="number" min="0" max="100000000" step="any" class="input" /></div></div>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.priceHint') }}</p>
          <p v-if="form.quote_verified_at" class="text-xs text-gray-500">{{ t('productSelection.quoteTime', { time: date(form.quote_verified_at) }) }}</p>
        </fieldset>
        <fieldset :disabled="saving" class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-700">
          <legend class="pt-4 text-sm font-semibold">{{ t('productSelection.reasoning') }}</legend>
          <div v-for="key in reasoningFields" :key="key"><label :for="`selection-${key}`" class="input-label">{{ t(`productSelection.${key}`) }}</label><textarea :id="`selection-${key}`" v-model="form[key]" class="input min-h-20 resize-y" :maxlength="key === 'official_comparison' ? 4000 : 2500" rows="3"></textarea></div>
          <div><label for="selection-notes" class="input-label">{{ t('productSelection.notes') }}</label><textarea id="selection-notes" v-model="form.notes" class="input min-h-24 resize-y" maxlength="6000" rows="4" :placeholder="t('productSelection.notesPlaceholder')"></textarea></div>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('productSelection.historyHint') }}</p>
        </fieldset>
      </form>
      <template #footer><button type="button" class="btn btn-secondary" :disabled="saving" @click="closeEditor">{{ t('productSelection.cancel') }}</button><button type="submit" form="product-selection-form" class="btn btn-primary" :disabled="saving || !form.title.trim()">{{ t(saving ? 'productSelection.saving' : 'productSelection.save') }}</button></template>
    </BaseDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAuthStore } from '@/stores/auth'
import {
  createProductSelection, getProductSelectionResearch, listProductSelections, updateProductSelection,
  selectionChannels, selectionStages, type SelectionChannel, type SelectionData, type SelectionRecord,
  type SelectionResearch, type SelectionResearchProfile, type SelectionResearchSample, type SelectionStage,
} from '@/api/productSelections'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import { emptySelection, selectionErrorStatus, selectionFromProduct, selectionFromResearch } from '@/utils/productSelection'
import { sanitizeUrl } from '@/utils/url'

const props = defineProps<{ draftProduct?: PublicAccountImportProduct | null }>()
const emit = defineEmits<{ (e: 'draft-consumed'): void }>()
const { t, locale } = useI18n()
const auth = useAuthStore()
const authenticated = computed(() => Boolean(auth.isAuthenticated))
const admin = computed(() => Boolean(auth.isAdmin))
const owner = computed(() => authenticated.value ? String(auth.user?.id ?? 'current') : '')
const records = ref<SelectionRecord[]>([])
const research = ref<SelectionResearch | null>(null)
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const researchError = ref('')
const saveError = ref('')
const conflict = ref(false)
const notice = ref('')
const view = ref<'records' | 'research'>('records')
const search = ref('')
const stageFilter = ref<SelectionStage | ''>('')
const channelFilter = ref<SelectionChannel | ''>('')
const includeArchived = ref(false)
const page = ref(1)
const pageSize = 8
const editorOpen = ref(false)
const editing = ref<SelectionRecord | null>(null)
const form = ref<SelectionData>(emptySelection())
const tagsInput = ref('')
const prices = ref({ cost: '', target: '', observed: '' })
let requestID = ''
let generation = 0
let loadSequence = 0
let mutationSequence = 0

const reasoningFields = ['specification', 'customer', 'official_comparison', 'alternatives', 'information_gap', 'delivery_risk', 'acquisition_plan', 'economics', 'repeat_assessment', 'decision', 'next_action'] as const
const analysisFields = ['subtypes', 'customer', 'competition', 'information', 'operations', 'economics', 'repeat', 'test'] as const
const stats = computed(() => [
  { label: 'activeCount', value: records.value.filter(r => r.stage !== 'archived').length },
  { label: 'priorityCount', value: records.value.filter(r => r.stage === 'priority').length },
  { label: 'trialCount', value: records.value.filter(r => r.stage === 'trial').length },
])
const filteredRecords = computed(() => records.value.filter(r =>
  (includeArchived.value || stageFilter.value === 'archived' || r.stage !== 'archived')
  && (!stageFilter.value || r.stage === stageFilter.value)
  && (!channelFilter.value || r.channels.includes(channelFilter.value))
  && `${r.title} ${r.category} ${r.shop_name} ${r.tags.join(' ')} ${r.notes} ${r.decision}`.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase()),
).sort((a, b) => b.updated_at.localeCompare(a.updated_at)))
const filteredProfiles = computed(() => (research.value?.profiles || []).filter(p =>
  (!stageFilter.value || p.stage === stageFilter.value)
  && `${p.name} ${p.position} ${p.examples.map(s => s.title).join(' ')}`.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase()),
))
const pageCount = computed(() => Math.max(1, Math.ceil((view.value === 'records' ? filteredRecords.value.length : filteredProfiles.value.length) / pageSize)))
const pagedRecords = computed(() => filteredRecords.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const pagedProfiles = computed(() => filteredProfiles.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch([view, search, stageFilter, channelFilter, includeArchived], () => { page.value = 1 })
watch(pageCount, n => { page.value = Math.min(page.value, n) })

watch([owner, admin], () => {
  generation++
  loadSequence++
  records.value = []
  research.value = null
  editorOpen.value = false
  editing.value = null
  form.value = emptySelection()
  loadError.value = ''; researchError.value = ''; saveError.value = ''; notice.value = ''
  saving.value = false; loading.value = false; view.value = 'records'
  if (owner.value) void load(true)
}, { immediate: true })
watch([() => props.draftProduct, owner], () => {
  if (props.draftProduct && owner.value) {
    openNew(selectionFromProduct(props.draftProduct))
    emit('draft-consumed')
  }
}, { immediate: true })
onBeforeUnmount(() => { generation++; loadSequence++ })

async function load(initial = false): Promise<void> {
  if (!owner.value) return
  const epoch = generation, sequence = ++loadSequence, mutation = mutationSequence
  loading.value = true; loadError.value = ''; researchError.value = ''
  const results = await Promise.allSettled([listProductSelections(), admin.value ? getProductSelectionResearch() : Promise.resolve(null)])
  if (epoch !== generation || sequence !== loadSequence) return
  if (results[0].status === 'fulfilled') {
    if (mutation === mutationSequence) records.value = results[0].value
  }
  else loadError.value = t('productSelection.loadFailed')
  if (results[1].status === 'fulfilled') research.value = results[1].value
  else researchError.value = t('productSelection.researchFailed')
  loading.value = false
  if (initial && !records.value.length && research.value && !editorOpen.value) view.value = 'research'
}

function dataOnly(record: SelectionData): SelectionData {
  return JSON.parse(JSON.stringify(Object.fromEntries(Object.keys(emptySelection()).map(key => [key, record[key as keyof SelectionData]])))) as SelectionData
}

function prepare(data: SelectionData): void {
  form.value = dataOnly(data)
  prices.value = { cost: data.purchase_price === null ? '' : String(data.purchase_price), target: data.target_sale_price === null ? '' : String(data.target_sale_price), observed: data.observed_sale_price === null ? '' : String(data.observed_sale_price) }
  tagsInput.value = data.tags.join(', ')
  saveError.value = ''; conflict.value = false
  editorOpen.value = true
}

function openNew(data: SelectionData = emptySelection()): void {
  if (!owner.value || saving.value) return
  editing.value = null
  requestID = globalThis.crypto?.randomUUID?.() || `selection-${Date.now()}-${Math.random().toString(36).slice(2)}`
  view.value = 'records'
  prepare(data)
}

function openExisting(record: SelectionRecord): void {
  if (saving.value) return
  editing.value = record
  prepare(record)
}

function openResearch(profile: SelectionResearchProfile, sample?: SelectionResearchSample): void {
  openNew(selectionFromResearch(profile, sample))
}

function closeEditor(): void { if (!saving.value) editorOpen.value = false }
function replaceRecord(record: SelectionRecord): void {
  mutationSequence++
  records.value = [record, ...records.value.filter(r => r.id !== record.id)]
}

async function save(): Promise<void> {
  if (saving.value || !owner.value) return
  const next = dataOnly(form.value)
  const numeric = [prices.value.cost, prices.value.target, prices.value.observed].map(raw => String(raw).trim() === '' ? null : Number(raw))
  if (numeric.some(n => n !== null && (!Number.isFinite(n) || n < 0 || n > 100000000))) { saveError.value = t('productSelection.invalidPrice'); return }
  next.purchase_price = numeric[0] ?? null
  next.target_sale_price = numeric[1] ?? null
  next.observed_sale_price = numeric[2] ?? null
  if (next.purchase_price === null) { next.price_basis = 'pending'; next.quote_verified_at = '' }
  else if (next.price_basis !== 'snapshot' || next.purchase_price !== form.value.purchase_price) { next.price_basis = 'estimate'; next.quote_verified_at = '' }
  next.tags = [...new Set(tagsInput.value.split(/[,，]/).map(s => s.trim()).filter(Boolean))]
  if (next.tags.length > 12 || next.tags.some(tag => Array.from(tag).length > 40)) { saveError.value = t('productSelection.tooManyTags'); return }
  saving.value = true; saveError.value = ''; notice.value = ''; conflict.value = false
  const epoch = generation
  try {
    const result = editing.value
      ? { record: await updateProductSelection(editing.value.id, editing.value.revision, next), created: true }
      : await createProductSelection(next, requestID)
    if (epoch !== generation) return
    replaceRecord(result.record)
    notice.value = t(result.created ? 'productSelection.saved' : 'productSelection.duplicate')
    if (result.created) editorOpen.value = false
    else { editing.value = result.record; prepare(result.record) }
    view.value = 'records'
  } catch (error) {
    if (epoch !== generation) return
    conflict.value = selectionErrorStatus(error) === 409
    saveError.value = t(conflict.value ? 'productSelection.conflict' : 'productSelection.saveFailed')
  } finally { if (epoch === generation) saving.value = false }
}

async function setArchived(record: SelectionRecord): Promise<void> {
  if (saving.value) return
  const epoch = generation
  saving.value = true; notice.value = ''; loadError.value = ''
  try {
    const result = await updateProductSelection(record.id, record.revision, { ...dataOnly(record), stage: record.stage === 'archived' ? 'review' : 'archived' })
    if (epoch !== generation) return
    replaceRecord(result)
    notice.value = t(result.stage === 'archived' ? 'productSelection.archived' : 'productSelection.saved')
  } catch (error) {
    if (epoch === generation) loadError.value = t(selectionErrorStatus(error) === 409 ? 'productSelection.conflict' : 'productSelection.saveFailed')
  } finally { if (epoch === generation) saving.value = false }
}

async function reloadLatest(): Promise<void> {
  const id = editing.value?.id, epoch = generation
  await load()
  if (epoch !== generation || loadError.value) return
  const latest = records.value.find(r => r.id === id)
  if (latest) openExisting(latest)
}

function price(value: number | null): string { return value === null ? t('productSelection.pending') : `¥${value.toFixed(2)}` }
function date(value: string): string { const parsed = new Date(value); return Number.isFinite(parsed.getTime()) ? parsed.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US') : '—' }
function safeURL(value: string): string { return sanitizeUrl(value) }
function stageClass(stage: SelectionStage): string {
  if (stage === 'priority') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300'
  if (stage === 'trial') return 'bg-blue-50 text-blue-700 dark:bg-blue-950/40 dark:text-blue-300'
  if (stage === 'hold') return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
}
</script>
