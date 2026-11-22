<template>
  <BaseDialog :show="true" :title="t('solutions.edit')" width="wide" @close="!busy && emit('cancel')">
    <form class="space-y-4 text-sm" @submit.prevent="save">
      <p v-if="conflict || source.incoming" role="alert" class="rounded bg-amber-50 p-3 text-amber-800">{{ t('solutions.conflict') }}
        <button v-if="conflict" type="button" class="underline" @click="reload">{{ t('solutions.reload') }}</button>
        <button v-if="source.incoming" type="button" class="underline" @click="useReport">{{ t('solutions.useReport') }}</button>
      </p>
      <p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
      <label v-for="key in ['name','purpose','customer','difference','prerequisite','gap'] as const" :key="key">{{ t('solutions.'+key) }}<textarea v-model.trim="draft[key]" required class="input" rows="2" /></label>
      <div class="grid gap-3 sm:grid-cols-3">
        <label>{{ t('solutions.stage') }}<select v-model="draft.stage" class="input"><option v-for="v in stages" :key="v" :value="v">{{ t('solutions.'+v) }}</option></select></label>
        <label>{{ t('solutions.delivery') }}<select v-model="draft.delivery" class="input"><option v-for="v in ['untested','verified','failed']" :key="v" :value="v">{{ t('solutions.'+v) }}</option></select></label>
        <label>{{ t('solutions.rank') }}<input v-model.number="draft.priority" type="number" min="1" step="1" required class="input" /></label>
      </div>
      <fieldset class="flex flex-wrap gap-3"><legend>{{ t('productSelection.channels') }}</legend><label v-for="c in selectionChannels" :key="c"><input v-model="draft.channels" type="checkbox" :value="c" /> {{ t('productSelection.channelNames.'+c) }}</label></fieldset>
      <div class="grid gap-3 sm:grid-cols-2"><label v-for="key in ['target','observed'] as const" :key="key">{{ t('solutions.'+key) }}
        <input :value="draft[key] ?? ''" type="number" min="0" step="0.01" class="input" :placeholder="t('solutions.unknown')" @input="setPrice(key,$event)" /></label></div>
      <details><summary>{{ t('solutions.journey') }}</summary><label v-for="key in journeyFields" :key="key">{{ t('solutions.'+key) }}<textarea v-model.trim="draft.journey[key]" class="input" rows="2" /></label></details>
      <details><summary>{{ t('solutions.components') }}</summary>
        <fieldset v-for="(item,i) in draft.components" :key="item.id" class="mt-3 space-y-2 border-l pl-3">
          <legend>{{ item.id }}</legend>
          <label>{{ t('solutions.name') }}<input v-model.trim="item.name" class="input" /></label>
          <div class="grid gap-2 sm:grid-cols-3"><label v-for="k in ['role','kind','status'] as const" :key="k">{{ t('solutions.'+k) }}
            <select v-model="item[k]" class="input"><option v-for="v in choices[k]" :key="v" :value="v">{{ t('solutions.'+v) }}</option></select>
          </label></div>
          <label>{{ t('solutions.condition') }}<textarea v-model.trim="item.condition" class="input" rows="2" /></label>
          <label>{{ t('solutions.depends') }}<select v-model="item.depends_on" class="input" multiple><option v-for="other in draft.components.filter(c => c.id !== item.id)" :key="other.id" :value="other.id">{{ other.name }}</option></select></label>
          <label>{{ t('solutions.sourceIds') }}<textarea :value="item.offers?.join('\n')" class="input font-mono" rows="3" @change="item.offers = ids($event)" /></label>
          <button type="button" class="text-red-600" @click="draft.components.splice(i,1)">{{ t('solutions.remove') }}</button>
        </fieldset>
        <button type="button" class="btn btn-secondary mt-2" @click="addItem">{{ t('solutions.addItem') }}</button>
      </details>
      <details><summary>{{ t('solutions.problems') }}</summary>
        <fieldset v-for="(p,i) in draft.problems" :key="i" class="mt-3 space-y-2 border-l pl-3">
          <label v-for="key in ['trigger','check','action','fallback'] as const" :key="key">{{ t('solutions.'+key) }}<textarea v-model.trim="p[key]" class="input" rows="2" /></label>
          <label>{{ t('solutions.component') }}<select v-model="p.component" class="input"><option v-for="item in draft.components" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
          <label>{{ t('solutions.status') }}<select v-model="p.status" class="input"><option v-for="v in choices.status" :key="v" :value="v">{{ t('solutions.'+v) }}</option></select></label>
          <label>{{ t('solutions.termsSource') }}<input v-model.trim="p.offer" class="input" /></label>
          <button type="button" class="text-red-600" @click="draft.problems.splice(i,1)">{{ t('solutions.remove') }}</button>
        </fieldset>
        <button type="button" class="btn btn-secondary mt-2" @click="draft.problems.push({trigger:'',check:'',action:'',fallback:'',component:draft.components[0]?.id || '',status:'untested'})">{{ t('solutions.addProblem') }}</button>
      </details>
      <details><summary>{{ t('solutions.commercial') }}</summary><label v-for="key in commercialFields" :key="key">{{ t('solutions.'+key) }}<textarea v-model.trim="draft.commercial[key]" class="input" rows="3" /></label></details>
      <label>{{ t('solutions.notes') }}<textarea v-model="notes" class="input" rows="3" maxlength="8000" /></label>
      <div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="busy" @click="emit('cancel')">{{ t('productSelection.cancel') }}</button><button type="submit" class="btn btn-primary" :disabled="busy || conflict">{{ t('productSelection.save') }}</button></div>
    </form>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { selectionChannels } from '@/api/productSelections'
import { commercialFields, journeyFields, stages, editSolution, getSolution, type Solution, type SolutionDetail } from '@/api/solutionLibrary'
import { selectionErrorStatus } from '@/utils/productSelection'
const props = defineProps<{ detail: SolutionDetail }>()
const emit = defineEmits<{ (e: 'saved'): void; (e: 'cancel'): void }>()
const { t } = useI18n()
const clone = (s: Solution): Solution => JSON.parse(JSON.stringify(s))
const source = ref(props.detail)
const draft = ref(clone(source.value.solution))
const notes = ref(source.value.notes)
const incoming = ref(false)
const busy = ref(false)
const error = ref('')
const conflict = ref(false)
const choices = { role: ['required','optional','alternative','backup'], kind: ['product','guide','service'], status: ['verified','merchant','untested','missing'] }
function setPrice(key: 'target' | 'observed', event: Event) {
  const value = (event.target as HTMLInputElement).value
  draft.value[key] = value === '' ? null : Number(value)
}
function ids(event: Event) { return (event.target as HTMLTextAreaElement).value.trim().split(/[\s,，]+/).filter(Boolean) }
function addItem() {
  let n = draft.value.components.length + 1
  while (draft.value.components.some(c => c.id === 'item-'+n)) n++
  draft.value.components.push({ id:'item-'+n, name:'', role:'optional', kind:'service', status:'untested', condition:'', depends_on:[], offers:[] })
}
function useReport() { if (source.value.incoming) { draft.value = clone(source.value.incoming); incoming.value = true } }
async function reload() {
  try { source.value = (await getSolution(source.value.solution.id)).detail; draft.value = clone(source.value.solution); notes.value = source.value.notes; conflict.value = false; incoming.value = false; error.value = '' }
  catch { error.value = t('solutions.loadFailed') }
}
async function save() {
  if (busy.value || conflict.value) return
  busy.value = true; error.value = ''
  try { await editSolution(draft.value,source.value.revision,notes.value,incoming.value); emit('saved') }
  catch (e) { conflict.value = selectionErrorStatus(e) === 409; if (!conflict.value) error.value = t('solutions.saveFailed') }
  finally { busy.value = false }
}
</script>

<style scoped>
label { display: block; }
summary { cursor: pointer; font-weight: 600; }
details label { margin-top: .5rem; }
</style>
