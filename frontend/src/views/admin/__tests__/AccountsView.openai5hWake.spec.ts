import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getLatestTask: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: mocks.list,
      listWithEtag: mocks.listWithEtag,
      getBatchTodayStats: mocks.getBatchTodayStats,
      getLatestOpenAI5hWakeTask: mocks.getLatestTask,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 })
    },
    proxies: { getAll: mocks.getAllProxies },
    groups: { getAll: mocks.getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: mocks.showSuccess,
    showWarning: mocks.showWarning,
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import AccountsView from '../AccountsView.vue'
import type { OpenAI5hWakeTask } from '@/api/admin/accounts'

const runningTask: OpenAI5hWakeTask = {
  id: 88,
  status: 'running',
  eligible_account_count: 4,
  active_window_count: 1,
  estimated_request_count: 3,
  total_items: 3,
  processed_items: 1,
  woken_count: 1,
  skipped_active_count: 0,
  failed_count: 0,
  cancelled_count: 0,
  alignment_span_seconds: 0,
  created_at: '2026-07-30T00:00:00Z',
  updated_at: '2026-07-30T00:00:01Z'
}

const mountView = () => mount(AccountsView, {
  attachTo: document.body,
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      AccountTableActions: { template: '<div><slot name="after" /></div>' },
      AccountTableFilters: true,
      DataTable: true,
      Pagination: true,
      ConfirmDialog: true,
      AccountBulkActionsBar: true,
      AccountActionMenu: true,
      ImportDataModal: true,
      ReAuthAccountModal: true,
      AccountTestModal: true,
      AccountStatsModal: true,
      ScheduledTestsPanel: true,
      SyncFromCrsModal: true,
      TempUnschedStatusModal: true,
      ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true,
      CreateAccountModal: true,
      EditAccountModal: true,
      BulkEditAccountModal: true,
      TotpStepUpDialog: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      AccountTodayStatsCell: true,
      AccountGroupsCell: true,
      AccountUsageCell: true,
      HelpTooltip: true,
      Icon: true,
      OpenAI5hWakeDialog: {
        props: ['show', 'initialTask'],
        template: '<div v-if="show" data-testid="wake-dialog-stub" />'
      }
    }
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  mocks.list.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
  mocks.listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
  mocks.getBatchTodayStats.mockResolvedValue({ stats: {} })
  mocks.getAllProxies.mockResolvedValue([])
  mocks.getAllGroups.mockResolvedValue([])
  mocks.getLatestTask.mockResolvedValue(runningTask)
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

describe('AccountsView OpenAI 5h wake integration', () => {
  it('restores a running task and opens it from the status entry', async () => {
    const wrapper = mountView()
    await flushPromises()

    const entry = wrapper.get('[data-testid="openai-5h-wake-running-entry"]')
    await entry.trigger('click')

    expect(wrapper.find('[data-testid="wake-dialog-stub"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('exposes the database-wide action in the more-actions menu', async () => {
    mocks.getLatestTask.mockResolvedValue(null)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
    const menuItem = document.body.querySelector<HTMLElement>('[data-testid="openai-5h-wake-menu-item"]')
    expect(menuItem).not.toBeNull()
    menuItem!.click()
    await flushPromises()

    expect(wrapper.find('[data-testid="wake-dialog-stub"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('keeps tracking after the dialog is closed and refreshes rows at terminal status', async () => {
    vi.useFakeTimers()
    const completedTask: OpenAI5hWakeTask = {
      ...runningTask,
      status: 'succeeded',
      processed_items: 3,
      woken_count: 2,
      skipped_active_count: 1,
      finished_at: '2026-07-30T00:00:10Z'
    }
    mocks.getLatestTask.mockResolvedValueOnce(runningTask).mockResolvedValueOnce(completedTask)
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect(mocks.getLatestTask).toHaveBeenCalledTimes(2)
    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(mocks.showSuccess).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="openai-5h-wake-running-entry"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
