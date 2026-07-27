import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'
import type {
  PublicAccountImportProductRefreshResponse,
  PublicAccountImportProductSyncStatus,
  PublicAccountImportProductsResponse,
  PublicAccountImportShop,
} from '@/api/publicAccountImport'

const {
  getGroups,
  getProducts,
  getShops,
  requestRefresh,
  submitImport,
  submitShop,
  fetchPublicSettings,
} = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getShops: vi.fn(),
  requestRefresh: vi.fn(),
  submitImport: vi.fn(),
  submitShop: vi.fn(),
  fetchPublicSettings: vi.fn(),
}))

vi.mock('@/api/publicAccountImport', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountImport')>(
    '@/api/publicAccountImport'
  )
  return {
    ...actual,
    getPublicAccountImportGroups: getGroups,
    getPublicAccountImportProducts: getProducts,
    getPublicAccountImportShops: getShops,
    requestPublicAccountImportProductRefresh: requestRefresh,
    submitPublicAccountImport: submitImport,
    submitPublicAccountImportShop: submitShop,
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    siteName: 'Sub2API',
    siteLogo: '',
    fetchPublicSettings,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params
        ? `${key} ${Object.values(params).join(' ')}`
        : key,
    }),
  }
})

const shops: PublicAccountImportShop[] = [
  { id: 'one', name: 'One', url: 'https://pay.ldxp.cn/shop/one', created_at: '2026-07-27T00:00:00Z' },
  { id: 'two', name: 'Two', url: 'https://pay.ldxp.cn/shop/two', created_at: '2026-07-27T00:00:00Z' },
  { id: 'other', name: 'Other', url: 'https://example.com/shop', created_at: '2026-07-27T00:00:00Z' },
]

function status(
  shopId: string,
  state: PublicAccountImportProductSyncStatus['state'] = 'idle',
  retryAfterSeconds = 0
): PublicAccountImportProductSyncStatus {
  return {
    shop_id: shopId,
    state,
    updated_at: shopId === 'one' ? '2026-07-27T01:00:00Z' : '',
    retry_after_seconds: retryAfterSeconds,
  }
}

function catalog(statuses: PublicAccountImportProductSyncStatus[]): PublicAccountImportProductsResponse {
  return {
    products: [],
    shop_count: statuses.length,
    pending_shops: 0,
    queued_shops: statuses.filter((item) => item.state === 'queued').length,
    refreshing_shops: statuses.filter((item) => item.state === 'refreshing').length,
    failed_shops: statuses.filter((item) => item.state === 'failed').length,
    refresh_seconds: 900,
    shop_sync_statuses: statuses,
  }
}

const RouterLinkStub = defineComponent({
  template: '<a><slot /></a>',
})

const HelpTooltipStub = defineComponent({
  template: '<span><slot name="content" /></span>',
})

function mountView(): VueWrapper {
  return mount(PublicAccountImportView, {
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        Icon: true,
        HelpTooltip: HelpTooltipStub,
      },
    },
  })
}

async function openShopsTab(wrapper: VueWrapper): Promise<void> {
  const tab = wrapper.findAll('button').find((button) => button.text() === 'publicAccountImport.shopModule')
  expect(tab).toBeDefined()
  await tab!.trigger('click')
}

function refreshButtons(wrapper: VueWrapper) {
  return wrapper.findAll('button[data-shop-product-refresh]')
}

describe('PublicAccountImportView per-shop product refresh', () => {
  beforeEach(() => {
    getGroups.mockReset().mockResolvedValue([])
    getProducts.mockReset().mockResolvedValue(catalog([status('one'), status('two')]))
    getShops.mockReset().mockResolvedValue(shops)
    requestRefresh.mockReset()
    submitImport.mockReset()
    submitShop.mockReset()
    fetchPublicSettings.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows separate refresh buttons only for supported shops and applies server states', async () => {
    getProducts.mockResolvedValue(catalog([status('one'), status('two', 'queued')]))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    const buttons = refreshButtons(wrapper)
    expect(buttons).toHaveLength(2)
    expect(buttons[0].attributes('data-shop-product-refresh')).toBe('one')
    expect(buttons[0].attributes('disabled')).toBeUndefined()
    expect(buttons[1].attributes('disabled')).toBeDefined()
    expect(buttons[0].element.closest('a')).toBeNull()
    expect(buttons[0].attributes('title')).toBe('publicAccountImport.shopProductsRefresh')
    expect(buttons[0].attributes('aria-label')).toBe('publicAccountImport.shopProductsRefresh')
    expect(wrapper.text()).toContain('publicAccountImport.shopProductsUpdatedAt')
    expect(wrapper.text()).toContain('publicAccountImport.shopProductsQueued')

    wrapper.unmount()
  })

  it('keeps requests for different shops independent', async () => {
    const pending = new Map<string, (value: PublicAccountImportProductRefreshResponse) => void>()
    requestRefresh.mockImplementation((shopId: string) => new Promise((resolve) => {
      pending.set(shopId, resolve)
    }))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    await refreshButtons(wrapper)[0].trigger('click')
    expect(refreshButtons(wrapper)[0].attributes('disabled')).toBeDefined()
    expect(refreshButtons(wrapper)[1].attributes('disabled')).toBeUndefined()

    await refreshButtons(wrapper)[1].trigger('click')
    expect(requestRefresh).toHaveBeenNthCalledWith(1, 'one')
    expect(requestRefresh).toHaveBeenNthCalledWith(2, 'two')
    expect(refreshButtons(wrapper)[0].attributes('disabled')).toBeDefined()
    expect(refreshButtons(wrapper)[1].attributes('disabled')).toBeDefined()

    pending.get('one')!({ ...status('one', 'queued'), accepted: true })
    pending.get('two')!({ ...status('two', 'queued'), accepted: true })
    await flushPromises()
    wrapper.unmount()
  })

  it('uses the existing shop message area for request failures', async () => {
    requestRefresh.mockRejectedValue(new Error('network down'))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    await refreshButtons(wrapper)[0].trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('network down')
    expect(refreshButtons(wrapper)[0].attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('re-enables a shop after the existing ten-second catalog poll reports completion', async () => {
    vi.useFakeTimers()
    getProducts
      .mockResolvedValueOnce(catalog([status('one', 'queued'), status('two')]))
      .mockResolvedValue(catalog([status('one'), status('two')]))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)
    expect(refreshButtons(wrapper)[0].attributes('disabled')).toBeDefined()

    await vi.advanceTimersByTimeAsync(10_000)
    await flushPromises()

    expect(getProducts.mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(refreshButtons(wrapper)[0].attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('refreshes shop sync statuses immediately after submitting a new shop', async () => {
    submitShop.mockResolvedValue({
      shop: { id: 'new', name: 'New', url: 'https://pay.ldxp.cn/shop/new', created_at: '2026-07-27T00:00:00Z' },
      created: true,
    })
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)
    expect(getProducts).toHaveBeenCalledTimes(1)

    await wrapper.find('#public-shop-name').setValue('New')
    await wrapper.find('#public-shop-url').setValue('https://pay.ldxp.cn/shop/new')
    await wrapper.find('#public-shop-name').element.closest('form')!.dispatchEvent(new Event('submit'))
    await flushPromises()

    expect(submitShop).toHaveBeenCalledWith({ name: 'New', url: 'https://pay.ldxp.cn/shop/new' })
    expect(getProducts.mock.calls.length).toBeGreaterThanOrEqual(2)
    wrapper.unmount()
  })
})
