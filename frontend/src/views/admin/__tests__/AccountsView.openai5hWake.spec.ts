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
  running_item_count: 1,
  woken_count: 1,
  skipped_active_count: 0,
  failed_count: 0,
  cancelled_count: 0,
  alignment_span_seconds: 0,
  created_at: '2026-07-30T00:00:00Z',
  updated_at: '2026-07-30T00:00:01Z'
}

const mountView = (stubOverrides: Record<string, any> = {}) => mount(AccountsView, {
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
        name: 'OpenAI5hWakeDialog',
        props: ['show', 'initialTask'],
        template: '<div v-if="show" data-testid="wake-dialog-stub" />'
      },
      ...stubOverrides
    }
  }
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(resolvePromise => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

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
  it('does not let an older latest-task response overwrite a task just reported by the dialog', async () => {
    const staleResponse = deferred<OpenAI5hWakeTask | null>()
    mocks.getLatestTask.mockReturnValue(staleResponse.promise)
    const wrapper = mountView()
    await flushPromises()

    const freshTask = { ...runningTask, id: 99 }
    const dialog = wrapper.findComponent({ name: 'OpenAI5hWakeDialog' })
    dialog.vm.$emit('task-updated', freshTask)
    await flushPromises()

    staleResponse.resolve(runningTask)
    await flushPromises()

    expect(dialog.props('initialTask')).toEqual(freshTask)
    wrapper.unmount()
  })

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

  it('refreshes visible usage cells even when the terminal account-list reload fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const account = {
      id: 7,
      name: 'OpenAI account',
      platform: 'openai',
      type: 'oauth',
      status: 'active',
      schedulable: true,
      credentials: {},
      extra: {},
      created_at: '2026-07-30T00:00:00Z',
      updated_at: '2026-07-30T00:00:00Z'
    }
    mocks.getLatestTask.mockResolvedValue(null)
    mocks.list
      .mockResolvedValueOnce({ items: [account], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockRejectedValueOnce(new Error('temporary list failure'))
    const wrapper = mountView({
      DataTable: {
        props: ['data'],
        template: '<div><slot v-if="data.length" name="cell-usage" :row="data[0]" /></div>'
      },
      AccountUsageCell: {
        props: ['manualRefreshToken'],
        template: '<span data-testid="usage-refresh-token">{{ manualRefreshToken }}</span>'
      }
    })
    await flushPromises()
    expect(wrapper.get('[data-testid="usage-refresh-token"]').text()).toBe('0')

    const completedTask: OpenAI5hWakeTask = {
      ...runningTask,
      status: 'succeeded',
      processed_items: runningTask.total_items,
      finished_at: '2026-07-30T00:00:10Z'
    }
    wrapper.findComponent({ name: 'OpenAI5hWakeDialog' }).vm.$emit('completed', completedTask)
    await flushPromises()

    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="usage-refresh-token"]').text()).toBe('1')
    wrapper.unmount()
    consoleError.mockRestore()
  })
})
