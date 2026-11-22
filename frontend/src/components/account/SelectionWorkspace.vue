<template>
  <section>
    <nav v-if="admin" class="mb-5 flex gap-2" :aria-label="t('solutions.nav')">
      <button v-for="tab in ['library','personal'] as const" :key="tab" type="button" class="btn" :class="active === tab ? 'btn-primary' : 'btn-secondary'" :aria-pressed="active === tab" @click="active = tab">{{ t('solutions.' + tab) }}</button>
    </nav>
    <SolutionLibrary v-if="admin && active === 'library'" :key="auth.user?.id" />
    <ProductSelectionPanel v-else :draft-product="draftProduct" @draft-consumed="emit('draft-consumed')" />
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import ProductSelectionPanel from './ProductSelectionPanel.vue'
import SolutionLibrary from './SolutionLibrary.vue'

const props = defineProps<{ draftProduct?: PublicAccountImportProduct | null }>()
const emit = defineEmits<{ (e: 'draft-consumed'): void }>()
const { t } = useI18n()
const auth = useAuthStore()
const admin = computed(() => Boolean(auth.isAdmin && auth.isAuthenticated))
const active = ref<'library' | 'personal'>('library')
watch([admin, () => auth.user?.id], () => {
  active.value = props.draftProduct || !admin.value ? 'personal' : 'library'
}, { immediate: true })
watch(() => props.draftProduct, product => { if (product) active.value = 'personal' })
</script>
