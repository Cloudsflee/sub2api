<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.openAI5hWake.title')"
    width="extra-wide"
    :close-on-escape="true"
    @close="emit('close')"
  >
    <div class="min-h-[420px]" data-testid="openai-5h-wake-dialog">
      <div v-if="initializing" class="flex min-h-[360px] items-center justify-center">
        <div class="flex items-center gap-3 text-sm text-gray-500 dark:text-dark-300">
          <Icon name="refresh" size="md" class="animate-spin" />
          <span>{{ t('admin.accounts.openAI5hWake.loading') }}</span>
        </div>
      </div>

      <div
        v-else-if="loadError && !task && !preview"
        class="flex min-h-[360px] flex-col items-center justify-center gap-4 text-center"
      >
        <Icon name="exclamationCircle" size="xl" class="text-red-500" />
        <p class="max-w-lg text-sm text-red-600 dark:text-red-400">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary" @click="initialize">
          <Icon name="refresh" size="sm" class="mr-1.5" />
          {{ t('common.retry') }}
        </button>
      </div>

      <div v-else-if="task" class="space-y-5" data-testid="openai-5h-wake-task">
        <div class="flex flex-wrap items-start justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-700">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ t('admin.accounts.openAI5hWake.taskLabel', { id: task.id }) }}
              </span>
              <span :class="taskStatusClass" class="inline-flex rounded-full px-2.5 py-1 text-xs font-medium">
                {{ taskStatusLabel }}
              </span>
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.openAI5hWake.backgroundHint') }}
            </p>
          </div>
          <div class="text-right text-xs text-gray-500 dark:text-dark-400">
            <div>{{ formatTimestamp(task.created_at) }}</div>
            <div v-if="task.cancel_requested_at" class="mt-1 font-medium text-amber-600 dark:text-amber-400">
              {{ t('admin.accounts.openAI5hWake.cancelRequested') }}
            </div>
          </div>
        </div>

        <div>
          <div class="mb-2 flex items-center justify-between gap-3 text-sm">
            <span class="font-medium text-gray-700 dark:text-gray-200">
              {{ t('admin.accounts.openAI5hWake.progress') }}
            </span>
            <span class="tabular-nums text-gray-500 dark:text-dark-300">
              {{ task.processed_items }} / {{ task.total_items }} ({{ progressPercent }}%)
            </span>
          </div>
          <div
            class="h-2 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700"
            role="progressbar"
            :aria-valuenow="progressPercent"
            aria-valuemin="0"
            aria-valuemax="100"
          >
            <div
              class="h-full rounded-full bg-primary-600 transition-[width] duration-300"
              :style="{ width: `${progressPercent}%` }"
            ></div>
          </div>
        </div>

        <div class="grid grid-cols-2 divide-x divide-y divide-gray-200 overflow-hidden rounded-md border border-gray-200 dark:divide-dark-700 dark:border-dark-700 sm:grid-cols-4 sm:divide-y-0">
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.woken') }}</div>
            <div class="mt-1 text-xl font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{{ task.woken_count }}</div>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.skipped') }}</div>
            <div class="mt-1 text-xl font-semibold tabular-nums text-blue-600 dark:text-blue-400">{{ task.skipped_active_count }}</div>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.failed') }}</div>
            <div class="mt-1 text-xl font-semibold tabular-nums text-red-600 dark:text-red-400">{{ task.failed_count }}</div>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.cancelled') }}</div>
            <div class="mt-1 text-xl font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ task.cancelled_count }}</div>
          </div>
        </div>

        <div class="grid gap-x-8 gap-y-3 border-y border-gray-200 py-4 text-sm dark:border-dark-700 sm:grid-cols-3">
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.earliestReset') }}</div>
            <div class="mt-1 font-medium text-gray-800 dark:text-gray-100">{{ formatTimestamp(task.earliest_reset_at) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.latestReset') }}</div>
            <div class="mt-1 font-medium text-gray-800 dark:text-gray-100">{{ formatTimestamp(task.latest_reset_at) }}</div>
          </div>
          <div>
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.openAI5hWake.alignmentSpan') }}</div>
            <div class="mt-1 font-medium text-gray-800 dark:text-gray-100">{{ alignmentSpan }}</div>
          </div>
        </div>

        <div>
          <div class="mb-3 flex items-center justify-between gap-3">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.accounts.openAI5hWake.results') }}
            </h4>
            <span v-if="pollError" class="text-xs text-amber-600 dark:text-amber-400">{{ pollError }}</span>
          </div>

          <div class="min-h-[240px] overflow-x-auto rounded-md border border-gray-200 dark:border-dark-700">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800">
                <tr>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.pool') }}</th>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.members') }}</th>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.itemStatus') }}</th>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.attempts') }}</th>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.resetAt') }}</th>
                  <th class="px-3 py-2.5 text-left text-xs font-medium text-gray-500 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.errorCode') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
                <tr v-if="itemsLoading && items.length === 0">
                  <td colspan="6" class="h-[190px] px-3 py-8 text-center text-gray-500 dark:text-dark-400">
                    {{ t('admin.accounts.openAI5hWake.loadingResults') }}
                  </td>
                </tr>
                <tr v-else-if="items.length === 0">
                  <td colspan="6" class="h-[190px] px-3 py-8 text-center text-gray-500 dark:text-dark-400">
                    {{ t('admin.accounts.openAI5hWake.noResults') }}
                  </td>
                </tr>
                <tr v-for="item in items" :key="item.id">
                  <td class="whitespace-nowrap px-3 py-2.5 font-mono text-xs text-gray-600 dark:text-dark-300" :title="item.identity_hash">
                    {{ item.identity_hash.slice(0, 12) }}
                  </td>
                  <td class="max-w-[220px] px-3 py-2.5 text-xs text-gray-600 dark:text-dark-300">
                    <span class="break-words">{{ item.member_account_ids.join(', ') }}</span>
                  </td>
                  <td class="whitespace-nowrap px-3 py-2.5">
                    <span :class="itemStatusClass(item.status)" class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium">
                      {{ itemStatusLabel(item.status) }}
                    </span>
                  </td>
                  <td class="whitespace-nowrap px-3 py-2.5 tabular-nums text-gray-600 dark:text-dark-300">
                    {{ item.attempted_account_ids.length }}
                  </td>
                  <td class="whitespace-nowrap px-3 py-2.5 text-xs text-gray-600 dark:text-dark-300">
                    {{ formatTimestamp(item.reset_at) }}
                  </td>
                  <td class="whitespace-nowrap px-3 py-2.5 font-mono text-xs text-red-600 dark:text-red-400">
                    {{ item.error_code || '-' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <Pagination
            v-if="itemsTotal > itemsPageSize"
            :page="itemsPage"
            :total="itemsTotal"
            :page-size="itemsPageSize"
            :show-page-size-selector="false"
            @update:page="changeItemsPage"
          />
        </div>
      </div>

      <div v-else-if="preview" class="space-y-6" data-testid="openai-5h-wake-preview">
        <div class="border-b border-gray-200 pb-4 dark:border-dark-700">
          <p class="text-sm leading-6 text-gray-600 dark:text-dark-300">
            {{ t('admin.accounts.openAI5hWake.scopeNotice') }}
          </p>
          <p class="mt-2 text-sm leading-6 text-amber-700 dark:text-amber-300">
            {{ t('admin.accounts.openAI5hWake.usageNotice') }}
          </p>
        </div>

        <div
          v-if="loadError"
          role="alert"
          data-testid="openai-5h-wake-start-error"
          class="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800/60 dark:bg-red-900/20 dark:text-red-300"
        >
          {{ loadError }}
        </div>

        <div class="grid grid-cols-2 divide-x divide-y divide-gray-200 overflow-hidden rounded-md border border-gray-200 dark:divide-dark-700 dark:border-dark-700 sm:grid-cols-4 sm:divide-y-0">
          <div v-for="stat in previewStats" :key="stat.label" class="bg-white px-4 py-4 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">{{ stat.label }}</div>
            <div class="mt-1 text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ stat.value }}</div>
          </div>
        </div>

        <div class="grid gap-x-8 gap-y-4 border-y border-gray-200 py-5 dark:border-dark-700 sm:grid-cols-2">
          <div class="flex items-center justify-between gap-4">
            <span class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.totalOpenAIAccounts') }}</span>
            <strong class="tabular-nums text-gray-900 dark:text-white">{{ preview.total_openai_accounts }}</strong>
          </div>
          <div class="flex items-center justify-between gap-4">
            <span class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.accounts.openAI5hWake.excludedAccounts') }}</span>
            <strong class="tabular-nums text-gray-900 dark:text-white">{{ excludedTotal }}</strong>
          </div>
        </div>

        <details v-if="excludedTotal > 0" class="group">
          <summary class="cursor-pointer text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('admin.accounts.openAI5hWake.exclusionDetails') }}
          </summary>
          <div class="mt-3 grid gap-x-8 gap-y-2 sm:grid-cols-2 lg:grid-cols-3">
            <div
              v-for="entry in exclusionEntries"
              :key="entry.key"
              class="flex items-center justify-between gap-3 py-1 text-sm"
            >
              <span class="text-gray-500 dark:text-dark-400">{{ entry.label }}</span>
              <span class="font-medium tabular-nums text-gray-800 dark:text-gray-100">{{ entry.value }}</span>
            </div>
          </div>
        </details>

        <div
          v-if="preview.unique_quota_pools === 0"
          class="rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800/60 dark:bg-amber-900/20 dark:text-amber-300"
        >
          {{ t('admin.accounts.openAI5hWake.noEligiblePools') }}
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-between gap-3">
        <div>
          <button
            v-if="task && isTaskActive && !task.cancel_requested_at"
            type="button"
            class="btn btn-danger"
            :disabled="cancelling"
            data-testid="openai-5h-wake-cancel"
            @click="showCancelConfirm = true"
          >
            {{ cancelling ? t('admin.accounts.openAI5hWake.cancelling') : t('admin.accounts.openAI5hWake.cancelTask') }}
          </button>
        </div>
        <div class="flex items-center gap-3">
          <button type="button" class="btn btn-secondary" @click="emit('close')">
            {{ t('common.close') }}
          </button>
          <button
            v-if="preview && !task"
            type="button"
            class="btn btn-primary"
            :disabled="starting || preview.unique_quota_pools === 0"
            data-testid="openai-5h-wake-start"
            @click="startTask"
          >
            <Icon v-if="starting" name="refresh" size="sm" class="mr-1.5 animate-spin" />
            <Icon v-else name="clock" size="sm" class="mr-1.5" />
            {{ starting ? t('admin.accounts.openAI5hWake.starting') : t('admin.accounts.openAI5hWake.confirmStart') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="showCancelConfirm"
    :title="t('admin.accounts.openAI5hWake.cancelConfirmTitle')"
    :message="t('admin.accounts.openAI5hWake.cancelConfirmMessage')"
    :confirm-text="t('admin.accounts.openAI5hWake.cancelTask')"
    :cancel-text="t('common.cancel')"
    :danger="true"
    @confirm="cancelTask"
    @cancel="showCancelConfirm = false"
  />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { accountsAPI } from '@/api/admin/accounts'
import type {
  OpenAI5hWakeItemStatus,
  OpenAI5hWakePreview,
  OpenAI5hWakeTask,
  OpenAI5hWakeTaskItem
} from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{
  show: boolean
  initialTask?: OpenAI5hWakeTask | null
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'task-updated', task: OpenAI5hWakeTask): void
  (event: 'completed', task: OpenAI5hWakeTask): void
}>()

const { t } = useI18n()
const preview = ref<OpenAI5hWakePreview | null>(null)
const task = ref<OpenAI5hWakeTask | null>(null)
const items = ref<OpenAI5hWakeTaskItem[]>([])
const itemsPage = ref(1)
const itemsPageSize = 10
const itemsTotal = ref(0)
const initializing = ref(false)
const starting = ref(false)
const cancelling = ref(false)
const itemsLoading = ref(false)
const loadError = ref('')
const pollError = ref('')
const showCancelConfirm = ref(false)
const completedTaskIDs = new Set<number>()
let initializeSequence = 0
let pollInFlight = false
let pollTimer: ReturnType<typeof setInterval> | null = null

const terminalStatuses = new Set<OpenAI5hWakeTask['status']>([
  'succeeded',
  'partial_succeeded',
  'failed',
  'cancelled'
])

const isActiveTask = (value: OpenAI5hWakeTask | null | undefined) =>
  Boolean(value && (value.status === 'pending' || value.status === 'running'))

const isTaskActive = computed(() => isActiveTask(task.value))
const progressPercent = computed(() => {
  if (!task.value) return 0
  if (task.value.total_items <= 0) return terminalStatuses.has(task.value.status) ? 100 : 0
  return Math.min(100, Math.round((task.value.processed_items / task.value.total_items) * 100))
})

const taskStatusLabel = computed(() => {
  if (!task.value) return ''
  return t(`admin.accounts.openAI5hWake.status.${task.value.status}`)
})

const taskStatusClass = computed(() => {
  switch (task.value?.status) {
    case 'succeeded':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'partial_succeeded':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'cancelled':
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
    default:
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  }
})

const previewStats = computed(() => {
  if (!preview.value) return []
  return [
    { label: t('admin.accounts.openAI5hWake.eligibleAccounts'), value: preview.value.eligible_accounts },
    { label: t('admin.accounts.openAI5hWake.uniqueQuotaPools'), value: preview.value.unique_quota_pools },
    { label: t('admin.accounts.openAI5hWake.activeWindows'), value: preview.value.active_windows },
    { label: t('admin.accounts.openAI5hWake.estimatedRequests'), value: preview.value.estimated_requests }
  ]
})

const excludedTotal = computed(() => {
  if (!preview.value) return 0
  return Object.values(preview.value.excluded).reduce((sum, value) => sum + value, 0)
})

const exclusionEntries = computed(() => {
  if (!preview.value) return []
  return Object.entries(preview.value.excluded)
    .filter(([, value]) => value > 0)
    .map(([key, value]) => ({
      key,
      value,
      label: t(`admin.accounts.openAI5hWake.exclusions.${key}`)
    }))
})

const alignmentSpan = computed(() => {
  if (!task.value?.earliest_reset_at || !task.value.latest_reset_at) return '-'
  const seconds = Math.max(0, task.value.alignment_span_seconds)
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const remainder = seconds % 60
  const parts: string[] = []
  if (hours > 0) parts.push(`${hours}h`)
  if (minutes > 0 || hours > 0) parts.push(`${minutes}m`)
  parts.push(`${remainder}s`)
  return parts.join(' ')
})

const formatTimestamp = (value?: string) => formatDateTime(value) || '-'

const itemStatusLabel = (status: OpenAI5hWakeItemStatus) =>
  t(`admin.accounts.openAI5hWake.itemStatuses.${status}`)

const itemStatusClass = (status: OpenAI5hWakeItemStatus) => {
  switch (status) {
    case 'woken':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'skipped_active':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'cancelled':
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
    default:
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  }
}

const stopPolling = () => {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

const startPolling = () => {
  stopPolling()
  if (!props.show || !isTaskActive.value) return
  pollTimer = setInterval(() => {
    void refreshTask()
  }, 1000)
}

const notifyTask = (nextTask: OpenAI5hWakeTask) => {
  task.value = nextTask
  emit('task-updated', nextTask)
  if (terminalStatuses.has(nextTask.status)) {
    stopPolling()
    if (!completedTaskIDs.has(nextTask.id)) {
      completedTaskIDs.add(nextTask.id)
      emit('completed', nextTask)
    }
  }
}

const loadItems = async () => {
  if (!task.value) return
  itemsLoading.value = true
  try {
    const result = await accountsAPI.listOpenAI5hWakeTaskItems(task.value.id, itemsPage.value, itemsPageSize)
    items.value = result.items
    itemsTotal.value = result.total
  } finally {
    itemsLoading.value = false
  }
}

const refreshTask = async () => {
  if (!task.value || pollInFlight) return
  pollInFlight = true
  try {
    const nextTask = await accountsAPI.getOpenAI5hWakeTask(task.value.id)
    notifyTask(nextTask)
    await loadItems()
    pollError.value = ''
  } catch (error) {
    pollError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.refreshFailed'))
  } finally {
    pollInFlight = false
  }
}

const showExistingTask = async (existingTask: OpenAI5hWakeTask) => {
  preview.value = null
  notifyTask(existingTask)
  if (isActiveTask(existingTask)) startPolling()
  try {
    await loadItems()
    pollError.value = ''
  } catch (error) {
    pollError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.resultsLoadFailed'))
  }
}

const initialize = async () => {
  const sequence = ++initializeSequence
  stopPolling()
  initializing.value = true
  loadError.value = ''
  pollError.value = ''
  task.value = null
  preview.value = null
  items.value = []
  itemsTotal.value = 0
  itemsPage.value = 1
  try {
    let latest = isActiveTask(props.initialTask) ? props.initialTask! : null
    if (!latest) {
      try {
        const fetched = await accountsAPI.getLatestOpenAI5hWakeTask()
        if (isActiveTask(fetched)) latest = fetched
      } catch {
        // Preview remains usable even if the optional latest-task lookup fails.
      }
    }
    if (sequence !== initializeSequence || !props.show) return
    if (latest) {
      await showExistingTask(latest)
      return
    }
    preview.value = await accountsAPI.previewOpenAI5hWake()
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.loadFailed'))
  } finally {
    if (sequence === initializeSequence) initializing.value = false
  }
}

const startTask = async () => {
  if (!preview.value || preview.value.unique_quota_pools === 0 || starting.value) return
  starting.value = true
  loadError.value = ''
  try {
    const result = await accountsAPI.createOpenAI5hWakeTask()
    itemsPage.value = 1
    await showExistingTask(result.task)
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.startFailed'))
  } finally {
    starting.value = false
  }
}

const cancelTask = async () => {
  showCancelConfirm.value = false
  if (!task.value || !isTaskActive.value || cancelling.value) return
  cancelling.value = true
  try {
    const nextTask = await accountsAPI.cancelOpenAI5hWakeTask(task.value.id)
    notifyTask(nextTask)
    startPolling()
  } catch (error) {
    pollError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.cancelFailed'))
  } finally {
    cancelling.value = false
  }
}

const changeItemsPage = (page: number) => {
  itemsPage.value = page
  void loadItems().catch((error) => {
    pollError.value = extractApiErrorMessage(error, t('admin.accounts.openAI5hWake.resultsLoadFailed'))
  })
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      void initialize()
    } else {
      ++initializeSequence
      stopPolling()
      showCancelConfirm.value = false
    }
  },
  { immediate: true }
)

watch(
  () => props.initialTask,
  (nextTask) => {
    if (!props.show || !isActiveTask(nextTask) || task.value?.id === nextTask?.id) return
    void showExistingTask(nextTask!)
  }
)

onUnmounted(stopPolling)
</script>
