<template>
  <div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-white">
    <header class="border-b border-gray-200 bg-white dark:border-dark-800 dark:bg-dark-900">
      <nav class="mx-auto flex h-16 max-w-7xl items-center justify-between gap-4 px-4 sm:px-6">
        <RouterLink to="/home" class="flex min-w-0 items-center gap-3">
          <img
            :src="siteLogo || '/logo.svg'"
            alt="Logo"
            class="h-9 w-9 shrink-0 rounded-lg object-contain"
          />
          <span class="truncate text-base font-semibold text-gray-900 dark:text-white">{{ siteName }}</span>
        </RouterLink>
        <div class="flex shrink-0 items-center gap-1">
          <RouterLink
            v-if="isAdmin"
            to="/admin/dashboard"
            class="inline-flex h-9 items-center gap-2 px-2 text-sm font-medium text-gray-600 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-400"
            :title="t('publicAccountStatus.backToAdmin')"
          >
            <Icon name="arrowLeft" size="sm" />
            <span class="hidden lg:inline">{{ t('publicAccountStatus.backToAdmin') }}</span>
          </RouterLink>
          <RouterLink
            v-else-if="isAuthenticated"
            to="/dashboard"
            class="inline-flex h-9 items-center gap-2 px-2 text-sm font-medium text-gray-600 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-400"
            :title="t('publicAccountStatus.backToDashboard')"
          >
            <Icon name="arrowLeft" size="sm" />
            <span class="hidden lg:inline">{{ t('publicAccountStatus.backToDashboard') }}</span>
          </RouterLink>
          <RouterLink
            v-else
            :to="{ path: '/login', query: { redirect: '/account-status' } }"
            class="inline-flex h-9 items-center gap-2 px-2 text-sm font-medium text-gray-600 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-400"
            :title="t('publicAccountStatus.login')"
          >
            <Icon name="login" size="sm" />
            <span class="hidden lg:inline">{{ t('publicAccountStatus.login') }}</span>
          </RouterLink>
          <RouterLink
            to="/account-import"
            class="inline-flex h-9 items-center gap-2 px-2 text-sm font-medium text-gray-600 hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-400"
            :title="t('publicAccountStatus.accountImportLink')"
          >
            <Icon name="upload" size="sm" />
            <span class="hidden sm:inline">{{ t('publicAccountStatus.accountImportLink') }}</span>
          </RouterLink>
          <LocaleSwitcher />
          <button
            type="button"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="toggleTheme"
          >
            <Icon :name="isDark ? 'sun' : 'moon'" size="md" />
          </button>
        </div>
      </nav>
    </header>

    <main class="mx-auto w-full max-w-7xl px-4 py-7 sm:px-6 sm:py-9">
      <div class="mb-6 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
            {{ t('publicAccountStatus.title') }}
          </h1>
          <p v-if="lastUpdated" class="mt-1 text-xs text-gray-500 dark:text-dark-400">
            {{ t('publicAccountStatus.lastUpdated', { time: formatTime(lastUpdated) }) }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary self-start sm:self-auto"
          :disabled="refreshing || groupsLoading"
          :title="t('publicAccountStatus.refresh')"
          data-testid="status-refresh"
          @click="manualRefresh"
        >
          <Icon name="refresh" size="sm" :class="refreshing ? 'animate-spin' : ''" />
          <span class="ml-2">{{ refreshing ? t('publicAccountStatus.refreshing') : t('publicAccountStatus.refresh') }}</span>
        </button>
      </div>

      <div v-if="groupsLoading" aria-live="polite">
        <div class="flex gap-2 overflow-hidden border-b border-gray-200 pb-3 dark:border-dark-700">
          <Skeleton v-for="index in 4" :key="index" width="9rem" height="2.5rem" />
        </div>
        <div class="mt-5 grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6">
          <Skeleton v-for="index in 6" :key="index" height="4.5rem" />
        </div>
        <div class="mt-6 overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
          <div v-for="index in 6" :key="index" class="grid grid-cols-5 gap-4 border-b border-gray-100 px-5 py-5 last:border-b-0 dark:border-dark-800">
            <Skeleton height="1rem" />
            <Skeleton height="1rem" />
            <Skeleton height="1rem" />
            <Skeleton height="1rem" />
            <Skeleton height="1rem" />
          </div>
        </div>
      </div>

      <div
        v-else-if="groupError && groups.length === 0"
        class="border-y border-red-200 bg-red-50 px-5 py-10 text-center dark:border-red-900 dark:bg-red-950/20"
        role="alert"
      >
        <Icon name="exclamationCircle" size="lg" class="mx-auto text-red-500" />
        <h2 class="mt-3 text-sm font-semibold text-red-800 dark:text-red-200">{{ t('publicAccountStatus.loadFailed') }}</h2>
        <p class="mt-1 break-words text-sm text-red-700 dark:text-red-300">{{ groupError }}</p>
        <button type="button" class="btn btn-secondary mt-4" @click="manualRefresh">
          {{ t('publicAccountStatus.retry') }}
        </button>
      </div>

      <div v-else-if="groups.length === 0" class="border-y border-gray-200 py-16 text-center dark:border-dark-700">
        <Icon name="globe" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
        <h2 class="mt-4 text-base font-semibold text-gray-900 dark:text-white">{{ t('publicAccountStatus.noGroupsTitle') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.noGroupsDescription') }}</p>
      </div>

      <template v-else>
        <div
          class="overflow-x-auto border-b border-gray-200 dark:border-dark-700"
          role="tablist"
          :aria-label="t('publicAccountStatus.title')"
          data-testid="status-tabs"
        >
          <div class="flex min-w-max gap-1">
            <button
              v-for="group in groups"
              :key="group.id"
              type="button"
              role="tab"
              class="relative flex h-12 min-w-36 max-w-64 items-center gap-2 px-4 text-left text-sm font-medium transition-colors"
              :class="group.id === activeGroupId
                ? 'text-primary-700 dark:text-primary-300'
                : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800'"
              :aria-selected="group.id === activeGroupId"
              :title="group.name"
              @click="selectGroup(group.id)"
            >
              <PlatformIcon :platform="group.platform" size="sm" />
              <span class="min-w-0 flex-1 truncate">{{ group.name }}</span>
              <span
                v-if="group.status !== 'active'"
                class="h-2 w-2 shrink-0 rounded-full bg-gray-400"
                :title="t('publicAccountStatus.groupInactive')"
              ></span>
              <span
                v-if="group.id === activeGroupId"
                class="absolute inset-x-2 bottom-0 h-0.5 bg-primary-500"
              ></span>
            </button>
          </div>
        </div>

        <div v-if="activeGroup" class="py-5">
          <div class="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="break-words text-lg font-semibold text-gray-900 dark:text-white">{{ activeGroup.name }}</h2>
                <span
                  v-if="activeGroup.status !== 'active'"
                  class="rounded-full bg-gray-200 px-2 py-0.5 text-xs font-medium text-gray-700 dark:bg-dark-700 dark:text-dark-200"
                >
                  {{ t('publicAccountStatus.groupInactive') }}
                </span>
              </div>
              <p v-if="activeGroup.description" class="mt-1 max-w-3xl break-words text-sm text-gray-500 dark:text-dark-400">
                {{ activeGroup.description }}
              </p>
            </div>
            <span class="shrink-0 text-xs font-medium uppercase text-gray-500 dark:text-dark-400">{{ activeGroup.platform }}</span>
          </div>
        </div>

        <section v-if="activeGroup" class="border-y border-gray-200 dark:border-dark-700" :aria-label="t('publicAccountStatus.statusSummary')">
          <div class="flex overflow-x-auto">
            <div
              v-for="item in summaryItems"
              :key="item.key"
              class="min-w-28 flex-1 border-r border-gray-100 px-4 py-3 last:border-r-0 dark:border-dark-800"
            >
              <div class="text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</div>
              <div class="mt-1 flex items-center gap-2">
                <span v-if="item.key !== 'total'" class="h-2 w-2 rounded-full" :class="statusDotClass(item.key)"></span>
                <span class="font-mono text-lg font-semibold text-gray-900 dark:text-white">{{ item.count }}</span>
              </div>
            </div>
          </div>
        </section>

        <div
          v-if="groupError && groups.length"
          class="mt-4 flex items-center justify-between gap-4 border-y border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/20 dark:text-amber-200"
          role="status"
        >
          <span class="break-words">{{ groupError }}</span>
          <button type="button" class="shrink-0 font-medium underline" @click="manualRefresh">{{ t('publicAccountStatus.retry') }}</button>
        </div>

        <div class="mt-6">
          <div v-if="accountsLoading && !accountPage" class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
            <div v-for="index in 6" :key="index" class="grid min-h-20 grid-cols-5 items-center gap-4 border-b border-gray-100 px-5 last:border-b-0 dark:border-dark-800">
              <Skeleton height="1rem" />
              <Skeleton height="1rem" />
              <Skeleton height="1rem" />
              <Skeleton height="1rem" />
              <Skeleton height="1rem" />
            </div>
          </div>

          <div
            v-else-if="accountsError"
            class="border-y border-red-200 bg-red-50 px-5 py-10 text-center dark:border-red-900 dark:bg-red-950/20"
            role="alert"
          >
            <Icon name="exclamationCircle" size="lg" class="mx-auto text-red-500" />
            <h3 class="mt-3 text-sm font-semibold text-red-800 dark:text-red-200">{{ t('publicAccountStatus.loadFailed') }}</h3>
            <p class="mt-1 break-words text-sm text-red-700 dark:text-red-300">{{ accountsError }}</p>
            <button type="button" class="btn btn-secondary mt-4" @click="reloadAccounts">{{ t('publicAccountStatus.retry') }}</button>
          </div>

          <div v-else-if="accountPage && accountPage.items.length === 0" class="border-y border-gray-200 py-14 text-center dark:border-dark-700">
            <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
            <h3 class="mt-4 text-base font-semibold text-gray-900 dark:text-white">{{ t('publicAccountStatus.noAccountsTitle') }}</h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.noAccountsDescription') }}</p>
          </div>

          <template v-else-if="accountPage">
            <div class="hidden overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900 md:block" :class="accountsLoading ? 'opacity-70' : ''">
              <table class="w-full table-fixed">
                <thead class="border-b border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800/70">
                  <tr class="text-left text-xs font-semibold text-gray-500 dark:text-dark-400">
                    <th class="w-[24%] px-5 py-3">{{ t('publicAccountStatus.columns.account') }}</th>
                    <th class="w-[17%] px-4 py-3">{{ t('publicAccountStatus.columns.status') }}</th>
                    <th class="w-[12%] px-4 py-3">{{ t('publicAccountStatus.columns.concurrency') }}</th>
                    <th class="w-[25%] px-4 py-3">{{ t('publicAccountStatus.columns.activity') }}</th>
                    <th class="w-[15%] px-4 py-3">{{ t('publicAccountStatus.columns.expires') }}</th>
                    <th class="w-[7%] px-4 py-3 text-center">{{ t('publicAccountStatus.columns.usage') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                  <template v-for="(account, index) in accountPage.items" :key="accountKey(account, index)">
                    <tr class="min-h-20 align-middle">
                      <td class="px-5 py-4">
                        <div class="flex min-w-0 items-center gap-3">
                          <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300">
                            <PlatformIcon :platform="account.platform" size="md" />
                          </div>
                          <div class="min-w-0">
                            <div class="truncate text-sm font-medium text-gray-900 dark:text-white" :title="account.name">{{ account.name }}</div>
                            <div class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ account.platform }} · {{ account.type }}</div>
                          </div>
                        </div>
                      </td>
                      <td class="px-4 py-4">
                        <span class="inline-flex max-w-full items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium" :class="statusBadgeClass(account.status)">
                          <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="statusDotClass(account.status)"></span>
                          <span class="truncate">{{ statusLabel(account.status) }}</span>
                        </span>
                        <div v-if="account.recovery_at" class="mt-1 break-words text-xs text-gray-500 dark:text-dark-400">
                          {{ t('publicAccountStatus.recoveryAt', { time: formatTime(account.recovery_at) }) }}
                        </div>
                      </td>
                      <td class="px-4 py-4 font-mono text-sm text-gray-700 dark:text-dark-200">
                        {{ t('publicAccountStatus.currentMax', { current: account.current_concurrency, max: account.max_concurrency }) }}
                      </td>
                      <td class="px-4 py-4 text-xs text-gray-500 dark:text-dark-400">
                        <div>{{ t('publicAccountStatus.lastUsed') }}: <span class="text-gray-700 dark:text-dark-200">{{ account.last_used_at ? formatTime(account.last_used_at) : t('publicAccountStatus.never') }}</span></div>
                        <div class="mt-1">{{ t('publicAccountStatus.updatedAt') }}: <span class="text-gray-700 dark:text-dark-200">{{ formatTime(account.updated_at) }}</span></div>
                      </td>
                      <td class="px-4 py-4 text-xs text-gray-600 dark:text-dark-300">
                        {{ account.expires_at ? formatTime(account.expires_at) : t('publicAccountStatus.noExpiry') }}
                      </td>
                      <td class="px-4 py-4 text-center">
                        <button
                          type="button"
                          class="inline-flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 disabled:cursor-default disabled:opacity-30 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-primary-400"
                          :disabled="!account.usage"
                          :title="account.usage ? t('publicAccountStatus.usageDetails') : t('publicAccountStatus.noUsage')"
                          :aria-expanded="expandedRows.has(index)"
                          @click="toggleExpanded(index)"
                        >
                          <Icon :name="expandedRows.has(index) ? 'chevronUp' : 'chevronDown'" size="sm" />
                        </button>
                      </td>
                    </tr>
                    <tr v-if="account.usage && expandedRows.has(index)">
                      <td colspan="6" class="p-0">
                        <PublicAccountUsageDetails :usage="account.usage" />
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
            </div>

            <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900 md:hidden" :class="accountsLoading ? 'opacity-70' : ''">
              <article
                v-for="(account, index) in accountPage.items"
                :key="accountKey(account, index)"
                class="border-b border-gray-100 last:border-b-0 dark:border-dark-800"
              >
                <div class="p-4">
                  <div class="flex min-w-0 items-start justify-between gap-3">
                    <div class="flex min-w-0 items-center gap-3">
                      <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300">
                        <PlatformIcon :platform="account.platform" size="md" />
                      </div>
                      <div class="min-w-0">
                        <div class="break-all text-sm font-semibold text-gray-900 dark:text-white">{{ account.name }}</div>
                        <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ account.platform }} · {{ account.type }}</div>
                      </div>
                    </div>
                    <span class="inline-flex max-w-[48%] shrink-0 items-center gap-1.5 rounded-full px-2 py-1 text-xs font-medium" :class="statusBadgeClass(account.status)">
                      <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="statusDotClass(account.status)"></span>
                      <span class="truncate">{{ statusLabel(account.status) }}</span>
                    </span>
                  </div>

                  <div class="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 text-xs">
                    <div>
                      <div class="text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.columns.concurrency') }}</div>
                      <div class="mt-0.5 font-mono font-medium text-gray-800 dark:text-dark-100">{{ t('publicAccountStatus.currentMax', { current: account.current_concurrency, max: account.max_concurrency }) }}</div>
                    </div>
                    <div>
                      <div class="text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.columns.expires') }}</div>
                      <div class="mt-0.5 break-words font-medium text-gray-800 dark:text-dark-100">{{ account.expires_at ? formatTime(account.expires_at) : t('publicAccountStatus.noExpiry') }}</div>
                    </div>
                    <div>
                      <div class="text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.lastUsed') }}</div>
                      <div class="mt-0.5 break-words font-medium text-gray-800 dark:text-dark-100">{{ account.last_used_at ? formatTime(account.last_used_at) : t('publicAccountStatus.never') }}</div>
                    </div>
                    <div>
                      <div class="text-gray-500 dark:text-dark-400">{{ t('publicAccountStatus.updatedAt') }}</div>
                      <div class="mt-0.5 break-words font-medium text-gray-800 dark:text-dark-100">{{ formatTime(account.updated_at) }}</div>
                    </div>
                  </div>

                  <div v-if="account.recovery_at" class="mt-3 border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-800 dark:text-dark-400">
                    {{ t('publicAccountStatus.recoveryAt', { time: formatTime(account.recovery_at) }) }}
                  </div>

                  <button
                    v-if="account.usage"
                    type="button"
                    class="mt-4 flex h-9 w-full items-center justify-between border-t border-gray-100 pt-3 text-sm font-medium text-gray-700 dark:border-dark-800 dark:text-dark-200"
                    :aria-expanded="expandedRows.has(index)"
                    @click="toggleExpanded(index)"
                  >
                    <span>{{ t('publicAccountStatus.usageDetails') }}</span>
                    <Icon :name="expandedRows.has(index) ? 'chevronUp' : 'chevronDown'" size="sm" />
                  </button>
                  <div v-else class="mt-4 border-t border-gray-100 pt-3 text-xs text-gray-400 dark:border-dark-800 dark:text-dark-500">
                    {{ t('publicAccountStatus.noUsage') }}
                  </div>
                </div>
                <PublicAccountUsageDetails v-if="account.usage && expandedRows.has(index)" :usage="account.usage" />
              </article>
            </div>

            <Pagination
              v-if="accountPage.total > 0"
              :total="accountPage.total"
              :page="page"
              :page-size="pageSize"
              :page-size-options="[20, 50, 100]"
              :show-page-size-selector="true"
              @update:page="changePage"
              @update:page-size="changePageSize"
            />
          </template>
        </div>
      </template>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Pagination from '@/components/common/Pagination.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Skeleton from '@/components/common/Skeleton.vue'
import PublicAccountUsageDetails from '@/components/public/PublicAccountUsageDetails.vue'
import {
  listPublicAccountStatusAccounts,
  listPublicAccountStatusGroups,
  type PublicAccountStatusAccount,
  type PublicAccountStatusCategory,
  type PublicAccountStatusGroup,
  type PublicAccountStatusPage
} from '@/api/publicAccountStatus'
import { formatDateTimeToMinute } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import { sanitizeUrl } from '@/utils/url'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const groups = ref<PublicAccountStatusGroup[]>([])
const activeGroupId = ref<number | null>(null)
const accountPage = ref<PublicAccountStatusPage | null>(null)
const page = ref(1)
const pageSize = ref(20)
const groupsLoading = ref(true)
const accountsLoading = ref(false)
const refreshing = ref(false)
const groupError = ref('')
const accountsError = ref('')
const lastUpdated = ref<Date | null>(null)
const expandedRows = ref(new Set<number>())

let groupsAbortController: AbortController | null = null
let accountsAbortController: AbortController | null = null
let refreshTimer: ReturnType<typeof setInterval> | null = null
let refreshPromise: Promise<void> | null = null

const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() =>
  sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', {
    allowRelative: true,
    allowDataUrl: true
  })
)
const isAdmin = computed(() => Boolean(authStore.isAdmin))
const isAuthenticated = computed(() => Boolean(authStore.isAuthenticated))
const isDark = ref(document.documentElement.classList.contains('dark'))
const activeGroup = computed(() => groups.value.find((group) => group.id === activeGroupId.value) ?? null)

const statusOrder: PublicAccountStatusCategory[] = [
  'available',
  'error',
  'inactive',
  'expired',
  'overloaded',
  'rate_limited',
  'temporarily_unavailable',
  'quota_exhausted',
  'paused',
  'model_limited'
]

const summaryItems = computed(() => {
  if (!activeGroup.value) return []
  const summary = activeGroup.value.status_summary
  return [
    { key: 'total', label: t('publicAccountStatus.total'), count: summary.total },
    ...statusOrder.map((status) => ({
      key: status,
      label: statusLabel(status),
      count: summary.statuses?.[status] ?? 0
    }))
  ]
})

function toggleTheme(): void {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

function initializeTheme(): void {
  const savedTheme = localStorage.getItem('theme')
  const prefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
  isDark.value = savedTheme ? savedTheme === 'dark' : prefersDark
  document.documentElement.classList.toggle('dark', isDark.value)
}

function isCanceled(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && 'code' in error && (error as { code?: string }).code === 'ERR_CANCELED')
}

async function refreshAll(initial = false): Promise<void> {
  if (refreshPromise) return refreshPromise
  refreshPromise = (async () => {
    if (initial) groupsLoading.value = true
    groupsAbortController?.abort()
    const controller = new AbortController()
    groupsAbortController = controller
    try {
      const nextGroups = await listPublicAccountStatusGroups(controller.signal)
      groups.value = nextGroups
      groupError.value = ''

      const activeStillExists = nextGroups.some((group) => group.id === activeGroupId.value)
      if (!activeStillExists) {
        activeGroupId.value = nextGroups[0]?.id ?? null
        page.value = 1
        accountPage.value = null
        expandedRows.value = new Set()
      }

      if (activeGroupId.value !== null) {
        await loadAccounts({ preserve: !initial })
      } else {
        accountPage.value = null
        accountsError.value = ''
      }
      lastUpdated.value = new Date()
    } catch (error) {
      if (!isCanceled(error)) {
        groupError.value = extractApiErrorMessage(error, t('publicAccountStatus.loadFailed'))
      }
    } finally {
      if (groupsAbortController === controller) groupsAbortController = null
      groupsLoading.value = false
    }
  })()
  try {
    await refreshPromise
  } finally {
    refreshPromise = null
  }
}

async function loadAccounts(options: { preserve?: boolean } = {}): Promise<void> {
  const groupId = activeGroupId.value
  if (groupId === null) return
  accountsAbortController?.abort()
  const controller = new AbortController()
  accountsAbortController = controller
  accountsLoading.value = true
  accountsError.value = ''
  if (!options.preserve) accountPage.value = null
  try {
    const result = await listPublicAccountStatusAccounts(
      groupId,
      page.value,
      pageSize.value,
      controller.signal
    )
    if (groupId !== activeGroupId.value) return
    if (page.value > result.pages) {
      page.value = Math.max(1, result.pages)
      await loadAccounts(options)
      return
    }
    accountPage.value = result
    lastUpdated.value = new Date()
  } catch (error) {
    if (!isCanceled(error)) {
      accountsError.value = extractApiErrorMessage(error, t('publicAccountStatus.loadFailed'))
    }
  } finally {
    if (accountsAbortController === controller) {
      accountsAbortController = null
      accountsLoading.value = false
    }
  }
}

async function manualRefresh(): Promise<void> {
  refreshing.value = true
  try {
    await refreshAll(false)
  } finally {
    refreshing.value = false
  }
}

async function reloadAccounts(): Promise<void> {
  await loadAccounts()
}

async function selectGroup(groupId: number): Promise<void> {
  if (groupId === activeGroupId.value) return
  activeGroupId.value = groupId
  page.value = 1
  expandedRows.value = new Set()
  await loadAccounts()
}

async function changePage(nextPage: number): Promise<void> {
  page.value = nextPage
  expandedRows.value = new Set()
  await loadAccounts()
}

async function changePageSize(nextSize: number): Promise<void> {
  if (![20, 50, 100].includes(nextSize)) return
  pageSize.value = nextSize
  page.value = 1
  expandedRows.value = new Set()
  await loadAccounts()
}

function toggleExpanded(index: number): void {
  const next = new Set(expandedRows.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  expandedRows.value = next
}

function accountKey(account: PublicAccountStatusAccount, index: number): string {
  return `${activeGroupId.value}-${page.value}-${index}-${account.name}-${account.platform}`
}

function formatTime(value: string | Date): string {
  return formatDateTimeToMinute(value) || '-'
}

function statusLabel(status: string): string {
  return t(`publicAccountStatus.statuses.${status}`)
}

function statusDotClass(status: string): string {
  const classes: Record<string, string> = {
    available: 'bg-emerald-500',
    error: 'bg-red-500',
    inactive: 'bg-gray-400',
    expired: 'bg-zinc-500',
    overloaded: 'bg-orange-500',
    rate_limited: 'bg-amber-500',
    temporarily_unavailable: 'bg-cyan-500',
    quota_exhausted: 'bg-rose-500',
    paused: 'bg-slate-500',
    model_limited: 'bg-violet-500'
  }
  return classes[status] ?? 'bg-gray-400'
}

function statusBadgeClass(status: PublicAccountStatusCategory): string {
  const classes: Record<PublicAccountStatusCategory, string> = {
    available: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300',
    error: 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-300',
    inactive: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200',
    expired: 'bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-200',
    overloaded: 'bg-orange-50 text-orange-700 dark:bg-orange-950/40 dark:text-orange-300',
    rate_limited: 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300',
    temporarily_unavailable: 'bg-cyan-50 text-cyan-700 dark:bg-cyan-950/40 dark:text-cyan-300',
    quota_exhausted: 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300',
    paused: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-200',
    model_limited: 'bg-violet-50 text-violet-700 dark:bg-violet-950/40 dark:text-violet-300'
  }
  return classes[status]
}

onMounted(() => {
  initializeTheme()
  if (!appStore.publicSettingsLoaded) void appStore.fetchPublicSettings()
  void refreshAll(true)
  refreshTimer = setInterval(() => {
    void refreshAll(false)
  }, 30_000)
})

onBeforeUnmount(() => {
  groupsAbortController?.abort()
  accountsAbortController?.abort()
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>
