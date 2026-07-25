import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountStatusView from '../AccountStatusView.vue'
import type {
  PublicAccountStatusGroup,
  PublicAccountStatusPage
} from '@/api/publicAccountStatus'

const {
  listGroups,
  listAccounts,
  fetchPublicSettings
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  listAccounts: vi.fn(),
  fetchPublicSettings: vi.fn()
}))

vi.mock('@/api/publicAccountStatus', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountStatus')>(
    '@/api/publicAccountStatus'
  )
  return {
    ...actual,
    listPublicAccountStatusGroups: listGroups,
    listPublicAccountStatusAccounts: listAccounts
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    siteName: 'Sub2API',
    siteLogo: '',
    publicSettingsLoaded: true,
    fetchPublicSettings
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (!params) return key
        return `${key} ${Object.values(params).join(' ')}`
      },
      locale: { value: 'en' }
    })
  }
})

const groups: PublicAccountStatusGroup[] = [
  {
    id: 1,
    name: 'Primary Pool',
    description: 'Main capacity',
    platform: 'openai',
    status: 'active',
    status_summary: {
      total: 21,
      statuses: {
        available: 20,
        error: 1,
        inactive: 0,
        expired: 0,
        overloaded: 0,
        rate_limited: 0,
        temporarily_unavailable: 0,
        quota_exhausted: 0,
        paused: 0,
        model_limited: 0
      }
    }
  },
  {
    id: 2,
    name: 'Maintenance Pool',
    platform: 'anthropic',
    status: 'inactive',
    status_summary: {
      total: 1,
      statuses: {
        available: 0,
        error: 0,
        inactive: 1,
        expired: 0,
        overloaded: 0,
        rate_limited: 0,
        temporarily_unavailable: 0,
        quota_exhausted: 0,
        paused: 0,
        model_limited: 0
      }
    }
  }
]

function accountPage(name = 'p***t@example.com'): PublicAccountStatusPage {
  return {
    items: [
      {
        name,
        platform: 'openai',
        type: 'apikey',
        status: 'available',
        current_concurrency: 1,
        max_concurrency: 4,
        last_used_at: '2026-07-25T04:00:00Z',
        updated_at: '2026-07-25T04:01:00Z',
        expires_at: null,
        usage: {
          source: 'passive',
          updated_at: '2026-07-25T04:01:00Z',
          five_hour: {
            utilization: 25,
            resets_at: '2026-07-25T09:00:00Z',
            remaining_seconds: 18000,
            window_stats: {
              requests: 12,
              tokens: 3400,
              cost: 0.12,
              standard_cost: 0.1,
              user_cost: 0.15
            }
          }
        }
      }
    ],
    total: 21,
    page: 1,
    page_size: 20,
    pages: 2
  }
}

const PaginationStub = defineComponent({
  emits: ['update:page', 'update:pageSize'],
  template: `
    <div>
      <button data-testid="next-page" @click="$emit('update:page', 2)">next</button>
      <button data-testid="page-size" @click="$emit('update:pageSize', 50)">50</button>
    </div>
  `
})

function mountView() {
  return mount(AccountStatusView, {
    global: {
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        LocaleSwitcher: true,
        Icon: {
          props: ['name'],
          template: '<span :data-icon="name"></span>'
        },
        PlatformIcon: true,
        Skeleton: true,
        Pagination: PaginationStub
      }
    }
  })
}

describe('AccountStatusView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
    listGroups.mockReset()
    listAccounts.mockReset()
    fetchPublicSettings.mockReset()
    listGroups.mockResolvedValue(groups)
    listAccounts.mockImplementation(async (groupId: number, page: number, pageSize: number) => {
      const result = accountPage(groupId === 1 ? 'p***t@example.com' : 'm***e@example.com')
      return { ...result, page, page_size: pageSize, total: groupId === 1 ? 21 : 1, pages: groupId === 1 ? 2 : 1 }
    })
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false })
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders group tabs, inactive state, masked accounts, and no edit controls', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('Primary Pool')
    expect(wrapper.text()).toContain('Maintenance Pool')
    expect(wrapper.find('[title="publicAccountStatus.groupInactive"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('p***t@example.com')
    expect(wrapper.text()).toContain('publicAccountStatus.statuses.available')
    expect(wrapper.find('input, textarea, select').exists()).toBe(false)
    expect(wrapper.text().toLowerCase()).not.toContain('edit')

    const tabs = wrapper.findAll('[role="tab"]')
    await tabs[1].trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(2, 1, 20, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('publicAccountStatus.groupInactive')
    expect(wrapper.text()).toContain('m***e@example.com')
    wrapper.unmount()
  })

  it('expands passive usage and changes page and page size', async () => {
    const wrapper = mountView()
    await flushPromises()

    const expandButton = wrapper.findAll('button[aria-expanded="false"]')[0]
    await expandButton.trigger('click')
    expect(wrapper.text()).toContain('publicAccountStatus.usage.snapshot')
    expect(wrapper.text()).toContain('publicAccountStatus.usage.fiveHour')
    expect(wrapper.text()).toContain('3,400')

    await wrapper.get('[data-testid="next-page"]').trigger('click')
    await flushPromises()
    expect(listAccounts).toHaveBeenLastCalledWith(1, 2, 20, expect.any(AbortSignal))

    await wrapper.get('[data-testid="page-size"]').trigger('click')
    await flushPromises()
    expect(listAccounts).toHaveBeenLastCalledWith(1, 1, 50, expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('refreshes manually and every 30 seconds', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(listGroups).toHaveBeenCalledTimes(1)

    await wrapper.get('[data-testid="status-refresh"]').trigger('click')
    await flushPromises()
    expect(listGroups).toHaveBeenCalledTimes(2)

    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(listGroups).toHaveBeenCalledTimes(3)
    expect(listAccounts).toHaveBeenCalledTimes(3)
    wrapper.unmount()
  })

  it('reloads immediately when the current page no longer exists', async () => {
    listAccounts.mockImplementation(async (_groupId: number, requestedPage: number, pageSize: number) => {
      if (requestedPage === 2) {
        return { ...accountPage(), items: [], total: 1, page: 2, page_size: pageSize, pages: 1 }
      }
      return { ...accountPage('r***d@example.com'), total: 1, page: 1, page_size: pageSize, pages: 1 }
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="next-page"]').trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenNthCalledWith(2, 1, 2, 20, expect.any(AbortSignal))
    expect(listAccounts).toHaveBeenNthCalledWith(3, 1, 1, 20, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('r***d@example.com')
    wrapper.unmount()
  })

  it('shows empty and failed group states with retry', async () => {
    listGroups.mockResolvedValueOnce([])
    const emptyWrapper = mountView()
    await flushPromises()
    expect(emptyWrapper.text()).toContain('publicAccountStatus.noGroupsTitle')
    emptyWrapper.unmount()

    listGroups.mockRejectedValueOnce(new Error('network unavailable'))
    const errorWrapper = mountView()
    await flushPromises()
    expect(errorWrapper.text()).toContain('network unavailable')
    expect(errorWrapper.text()).toContain('publicAccountStatus.retry')
    errorWrapper.unmount()
  })
})
