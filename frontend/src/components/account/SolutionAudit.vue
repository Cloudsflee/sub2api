<template>
  <details class="rounded-lg border p-3 text-sm dark:border-dark-700" @toggle="toggle">
    <summary class="cursor-pointer font-medium">{{ t('solutions.audit') }} · {{ version }}</summary>
    <p v-if="error" role="alert">{{ t('solutions.loadFailed') }} <button class="underline" @click="load">{{ t('solutions.retry') }}</button></p>
    <div v-if="audit" class="mt-3 space-y-3">
      <p>{{ audit.snapshot.source }} · {{ audit.snapshot.at }} · {{ audit.snapshot.count }} {{ t('solutions.offers') }}</p>
      <p>{{ t('solutions.valid') }} {{ audit.valid }} · {{ t('solutions.incomplete') }} {{ audit.incomplete }} · {{ t('solutions.verified') }} {{ audit.verified }}</p>
      <div class="overflow-x-auto"><table class="w-full text-left text-xs"><thead><tr><th>{{ t('solutions.type') }}</th><th>{{ t('solutions.total') }}</th><th v-for="v in dispositions" :key="v">{{ t('solutions.'+v) }}</th><th>{{ t('solutions.detail_read') }}</th><th>{{ t('solutions.unpriced') }}</th></tr></thead>
        <tbody><tr v-for="c in audit.coverage" :key="c.type" class="border-t"><th class="py-1">{{ c.type }}</th><td>{{ c.total }}</td><td v-for="v in dispositions" :key="v">{{ c.uses[v] || 0 }}</td><td>{{ c.details.read || 0 }}</td><td>{{ c.unpriced }}</td></tr></tbody>
      </table></div>
      <details><summary>{{ t('solutions.merges') }}</summary><p v-for="m in audit.merges" :key="m.solution_id + m.variants" class="mt-2">{{ m.variants }} · {{ m.reason }} ({{ m.solution_id }})</p></details>
      <details @toggle="toggleOffers"><summary>{{ t('solutions.dispositions') }}</summary>
        <div class="my-2 flex flex-wrap gap-2"><select v-model="type" class="input sm:w-auto" :aria-label="t('solutions.type')"><option value="">{{ t('solutions.allTypes') }}</option><option v-for="c in audit.coverage" :key="c.type">{{ c.type }}</option></select>
          <select v-model="disposition" class="input sm:w-auto" :aria-label="t('solutions.dispositions')"><option value="">{{ t('solutions.allStates') }}</option><option v-for="v in dispositions" :key="v" :value="v">{{ t('solutions.'+v) }}</option></select></div>
        <p v-if="offerError" role="alert">{{ t('solutions.loadFailed') }} <button class="underline" @click="loadOffers">{{ t('solutions.retry') }}</button></p>
        <details v-for="o in offers" :key="o.id" class="my-1"><summary>{{ o.name }} · {{ t('solutions.'+o.disposition) }}</summary><p>{{ o.reason }}</p><p>{{ o.shop }} · {{ o.id }} · {{ t(`solutions.detail_${o.detail_state}`) }}</p><p class="whitespace-pre-wrap break-words">{{ o.original || o.detail_reason }}</p></details>
        <div class="flex items-center gap-3"><button class="underline" :disabled="page===1" @click="page--">{{ t('productSelection.previous') }}</button>{{ page }} / {{ Math.max(1,Math.ceil(total/50)) }}<button class="underline" :disabled="page*50 >= total" @click="page++">{{ t('productSelection.next') }}</button></div>
      </details>
    </div>
  </details>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getSolutionAudit, getSolutionOffers, dispositions, type Audit, type Offer } from '@/api/solutionLibrary'
const props = defineProps<{ version: string }>()
const { t } = useI18n()
const audit = ref<Audit>()
const error = ref(false)
const type = ref('')
const disposition = ref('')
const offers = ref<Offer[]>([])
const total = ref(0)
const page = ref(1)
const offerError = ref(false)
const offersOpen = ref(false)
let seq = 0
let busy = false
async function load() {
  if (busy) return
  busy = true; error.value = false
  try { audit.value = await getSolutionAudit(props.version) } catch { error.value = true } finally { busy = false }
}
function toggle(e: Event) { if ((e.target as HTMLDetailsElement).open && !audit.value) void load() }
function toggleOffers(e: Event) { offersOpen.value = (e.target as HTMLDetailsElement).open }
async function loadOffers() {
  const n = ++seq
  offerError.value = false
  try { const r = await getSolutionOffers(props.version,type.value,disposition.value,page.value); if (n === seq) { offers.value = r.offers; total.value = r.total } }
  catch { if (n === seq) offerError.value = true }
}
watch([type,disposition], () => { page.value = 1 })
watch([offersOpen,type,disposition,page], () => { if (offersOpen.value) void loadOffers() })
</script>
