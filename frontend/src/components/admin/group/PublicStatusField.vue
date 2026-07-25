<template>
  <div>
    <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <div class="text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.publicStatus.label') }}
        </div>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ t('admin.groups.publicStatus.hint') }}
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-3">
        <button
          type="button"
          data-testid="public-status-toggle"
          role="switch"
          :aria-checked="modelValue"
          :aria-label="t('admin.groups.publicStatus.label')"
          :class="[
            'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
            modelValue ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'
          ]"
          @click="$emit('update:modelValue', !modelValue)"
        >
          <span
            :class="[
              'inline-block h-4 w-4 rounded-full bg-white shadow transition-transform',
              modelValue ? 'translate-x-6' : 'translate-x-1'
            ]"
          ></span>
        </button>
        <span class="w-16 text-sm text-gray-500 dark:text-gray-400">
          {{ modelValue ? t('admin.groups.publicStatus.enabled') : t('admin.groups.publicStatus.disabled') }}
        </span>
      </div>
    </div>

    <div
      v-if="modelValue"
      class="mt-3 flex min-w-0 items-center gap-2 border-y border-gray-200 py-2 dark:border-dark-700"
    >
      <Icon name="globe" size="sm" class="shrink-0 text-primary-600 dark:text-primary-400" />
      <code class="min-w-0 flex-1 truncate text-xs text-gray-600 dark:text-gray-300" :title="url">{{ url }}</code>
      <button
        type="button"
        data-testid="public-status-copy"
        class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-primary-400"
        :title="t('admin.groups.publicStatus.copy')"
        :aria-label="t('admin.groups.publicStatus.copy')"
        @click="copyUrl"
      >
        <Icon name="copy" size="sm" />
      </button>
      <button
        type="button"
        data-testid="public-status-open"
        class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-primary-400"
        :title="t('admin.groups.publicStatus.open')"
        :aria-label="t('admin.groups.publicStatus.open')"
        @click="openUrl"
      >
        <Icon name="externalLink" size="sm" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

const props = defineProps<{
  modelValue: boolean
  url: string
}>()

defineEmits<{
  (event: 'update:modelValue', value: boolean): void
}>()

const { t } = useI18n()
const { copyToClipboard } = useClipboard()

function copyUrl(): void {
  void copyToClipboard(props.url, t('admin.groups.publicStatus.copied'))
}

function openUrl(): void {
  window.open(props.url, '_blank', 'noopener,noreferrer')
}
</script>
