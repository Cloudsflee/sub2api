import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import type {
  PublicAccountImportProduct,
  PublicAccountImportProductRefreshResponse,
  PublicAccountImportProductSyncStatus,
  PublicAccountImportProductsResponse,
  PublicAccountImportShop,
  PublicAccountImportProductSyncWorkerStatus,
} from '@/api/publicAccountImport'

const {
  getGroups,
  getProducts,
  getShops,
  updateTrust,
  deleteShop,
  requestRefresh,
  submitImport,
  submitShop,
  fetchPublicSettings,
  authState,
} = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getShops: vi.fn(),
  updateTrust: vi.fn(),
  deleteShop: vi.fn(),
  requestRefresh: vi.fn(),
  submitImport: vi.fn(),
  submitShop: vi.fn(),
  fetchPublicSettings: vi.fn(),
  authState: { isAdmin: false },
}))

vi.mock('@/api/publicAccountImport', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountImport')>(
    '@/api/publicAccountImport'
  )
  return {
    ...actual,
    getPublicAccountImportGroups: getGroups,
		getPublicAccountImportProductsWithETag: async (...args: unknown[]) => ({
			notModified: false,
			etag: '"catalog"',
			data: await getProducts(...args),
		}),
    getPublicAccountImportShops: getShops,
    updatePublicAccountImportShopTrustLevel: updateTrust,
    deletePublicAccountImportShop: deleteShop,
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

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
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
  { id: 'one', name: 'One', url: 'https://pay.ldxp.cn/shop/7HZ37ZCG/g47fr5', created_at: '2026-07-27T00:00:00Z', trust_level: 'trusted' },
  { id: 'two', name: 'Two', url: 'https://pay.ldxp.cn/shop/two', created_at: '2026-07-27T00:00:00Z', trust_level: 'neutral' },
  { id: 'other', name: 'Other', url: 'https://example.com/shop', created_at: '2026-07-27T00:00:00Z', trust_level: 'untrusted' },
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
		snapshot_state: shopId === 'one' ? 'fresh' : 'pending',
		snapshot_updated_at: shopId === 'one' ? '2026-07-27T01:00:00Z' : '',
		snapshot_expires_at: shopId === 'one' ? '2026-07-27T01:30:00Z' : '',
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
		expired_shops: statuses.filter((item) => item.snapshot_state === 'expired').length,
    refresh_seconds: 900,
    shop_sync_statuses: statuses,
  }
}

function workerStatus(
  unavailableLanes: number[] = [],
  reason = ''
): PublicAccountImportProductSyncWorkerStatus {
  const lanes = Array.from({ length: 6 }, (_, index) => {
    const unavailable = unavailableLanes.includes(index + 1)
    return {
      lane: index + 1,
      availability: unavailable ? 'unavailable' : 'available',
      reason: unavailable ? reason : '',
      state: unavailable ? 'error' : 'idle',
    }
  })
  return {
    availability: unavailableLanes.length ? 'unavailable' : 'available',
    reason,
    updated_at: '2026-08-04T04:00:00Z',
    configured_lane_count: 6,
    expected_lane_count: 6,
    available_lane_count: 6 - unavailableLanes.length,
    unavailable_lane_count: unavailableLanes.length,
    lanes,
  }
}

const RouterLinkStub = defineComponent({
  props: {
    to: {
      type: [String, Object],
      required: true,
    },
  },
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
    updateTrust.mockReset()
    deleteShop.mockReset()
    requestRefresh.mockReset()
    submitImport.mockReset()
    submitShop.mockReset()
    fetchPublicSettings.mockReset()
    authState.isAdmin = false
  })

  afterEach(() => {
    vi.useRealTimers()
		vi.restoreAllMocks()
  })

  it('returns anonymous administrator sign-in to the account import page', async () => {
    const wrapper = mountView()
    await flushPromises()

    const link = wrapper.findComponent(RouterLinkStub)
    expect(link.props('to')).toEqual({ path: '/login', query: { redirect: '/account-import' } })
    expect(link.text()).toBe('publicAccountImport.adminLogin')
    wrapper.unmount()
  })

  it('links authenticated administrators back to the admin dashboard', async () => {
    authState.isAdmin = true
    const wrapper = mountView()
    await flushPromises()

    const link = wrapper.findComponent(RouterLinkStub)
    expect(link.props('to')).toBe('/admin/dashboard')
    expect(link.text()).toBe('publicAccountImport.backToAdmin')
    wrapper.unmount()
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

  it('shows trust labels publicly and hides management controls from anonymous visitors', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    expect(wrapper.find('[data-shop-trust-level="trusted"]').text()).toBe('publicAccountImport.shopTrustTrusted')
    expect(wrapper.find('[data-shop-trust-level="neutral"]').text()).toBe('publicAccountImport.shopTrustNeutral')
    expect(wrapper.find('[data-shop-trust-level="untrusted"]').text()).toBe('publicAccountImport.shopTrustUntrusted')
    expect(wrapper.find('[data-shop-admin-controls]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows all six update lanes and failure reasons to anonymous visitors', async () => {
    getProducts.mockResolvedValue({
      ...catalog([status('one'), status('two')]),
      worker_status: workerStatus([2, 5], 'ESA verification failed (VerifyCode=F001)'),
    })
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    expect(wrapper.findAll('[data-product-sync-lane]')).toHaveLength(6)
    expect(wrapper.findAll('[data-product-sync-lane-availability="available"]')).toHaveLength(4)
    expect(wrapper.findAll('[data-product-sync-lane-availability="unavailable"]')).toHaveLength(2)
    expect(wrapper.find('[data-product-sync-lane="2"]').text()).toContain('ESA verification failed (VerifyCode=F001)')
    expect(wrapper.find('[data-product-sync-worker-availability]').text()).toBe('publicAccountImport.productSyncUnavailable')
    expect(wrapper.find('[data-shop-admin-controls]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('sorts trusted shops first and untrusted shops last while preserving group order', async () => {
    getShops.mockResolvedValue([
      { ...shops[2], id: 'untrusted-first', name: 'Untrusted first' },
      { ...shops[1], id: 'neutral-first', name: 'Neutral first' },
      // A legacy value must be treated as neutral by the view as a defensive
      // fallback even though the API normalizes it before returning.
      { ...shops[1], id: 'legacy-neutral', name: 'Legacy neutral', trust_level: 'legacy' as PublicAccountImportShop['trust_level'] },
      { ...shops[0], id: 'trusted-first', name: 'Trusted first' },
      { ...shops[0], id: 'trusted-second', name: 'Trusted second' },
      { ...shops[1], id: 'neutral-second', name: 'Neutral second' },
      { ...shops[2], id: 'untrusted-second', name: 'Untrusted second' },
    ])
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    expect(wrapper.findAll('[data-shop-id]').map((row) => row.attributes('data-shop-id'))).toEqual([
      'trusted-first',
      'trusted-second',
      'neutral-first',
      'legacy-neutral',
      'neutral-second',
      'untrusted-first',
      'untrusted-second',
    ])
    wrapper.unmount()
  })

  it('lets administrators update one trust level and rolls back a failed update', async () => {
    authState.isAdmin = true
    updateTrust
      .mockResolvedValueOnce({ ...shops[0], trust_level: 'untrusted' })
      .mockRejectedValueOnce(new Error('classification failed'))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    const first = wrapper.find('select[data-shop-trust-select="one"]')
    await first.setValue('untrusted')
    await flushPromises()
    expect(updateTrust).toHaveBeenNthCalledWith(1, 'one', 'untrusted')
    expect((wrapper.find('select[data-shop-trust-select="one"]').element as HTMLSelectElement).value).toBe('untrusted')

    await wrapper.find('select[data-shop-trust-select="one"]').setValue('neutral')
    await flushPromises()
    expect(updateTrust).toHaveBeenNthCalledWith(2, 'one', 'neutral')
    expect((wrapper.find('select[data-shop-trust-select="one"]').element as HTMLSelectElement).value).toBe('untrusted')
    expect(wrapper.text()).toContain('classification failed')
    wrapper.unmount()
  })

  it('confirms an administrator deletion and removes the shop, products, and status locally', async () => {
    authState.isAdmin = true
    const product: PublicAccountImportProduct = {
      id: 'product-one', shop_id: 'one', shop_name: 'One', shop_url: shops[0].url,
      name: 'Product one', url: 'https://pay.ldxp.cn/item/product-one', goods_type: 'card',
      price: 2, stock: 1, minimum_quantity: 1, updated_at: '2026-07-27T01:00:00Z',
    }
    getProducts
      .mockResolvedValueOnce({ ...catalog([status('one'), status('two')]), products: [product] })
      .mockResolvedValue(catalog([status('two')]))
    deleteShop.mockResolvedValue({ id: 'one' })
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    await wrapper.find('button[data-shop-delete="one"]').trigger('click')
    const dialog = wrapper.findComponent(ConfirmDialog)
    expect(dialog.props('show')).toBe(true)
    dialog.vm.$emit('confirm')
    await flushPromises()

    expect(deleteShop).toHaveBeenCalledWith('one')
    expect(wrapper.find('select[data-shop-trust-select="one"]').exists()).toBe(false)
    expect(wrapper.find('button[data-shop-product-refresh="one"]').exists()).toBe(false)
    expect(dialog.props('show')).toBe(false)
    const productsTab = wrapper.findAll('button').find((button) => button.text().includes('publicAccountImport.productModule'))
    await productsTab!.trigger('click')
    expect(wrapper.find('a[href="https://pay.ldxp.cn/item/product-one"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps the shop and confirmation open when an administrator deletion fails', async () => {
    authState.isAdmin = true
    deleteShop.mockRejectedValue(new Error('delete failed'))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    await wrapper.find('button[data-shop-delete="one"]').trigger('click')
    const dialog = wrapper.findComponent(ConfirmDialog)
    dialog.vm.$emit('confirm')
    await flushPromises()

    expect(wrapper.find('select[data-shop-trust-select="one"]').exists()).toBe(true)
    expect(dialog.props('show')).toBe(true)
    expect(wrapper.text()).toContain('delete failed')
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

	it('polls an idle catalog every sixty seconds', async () => {
		vi.useFakeTimers()
		const wrapper = mountView()
		await flushPromises()
		expect(getProducts).toHaveBeenCalledTimes(1)

		await vi.advanceTimersByTimeAsync(10_000)
		expect(getProducts).toHaveBeenCalledTimes(1)
		await vi.advanceTimersByTimeAsync(50_000)
		await flushPromises()
		expect(getProducts).toHaveBeenCalledTimes(2)
		wrapper.unmount()
	})

	it('pauses catalog polling while hidden and refreshes immediately when visible', async () => {
		vi.useFakeTimers()
		let hidden = false
		vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
		const wrapper = mountView()
		await flushPromises()
		expect(getProducts).toHaveBeenCalledTimes(1)

		hidden = true
		document.dispatchEvent(new Event('visibilitychange'))
		await vi.advanceTimersByTimeAsync(60_000)
		expect(getProducts).toHaveBeenCalledTimes(1)

		hidden = false
		document.dispatchEvent(new Event('visibilitychange'))
		await flushPromises()
		expect(getProducts).toHaveBeenCalledTimes(2)
		wrapper.unmount()
	})

	it('does not restart polling when an in-flight catalog request finishes after unmount', async () => {
		vi.useFakeTimers()
		let resolveCatalog!: (value: PublicAccountImportProductsResponse) => void
		getProducts.mockReturnValueOnce(new Promise((resolve) => {
			resolveCatalog = resolve
		}))
		const wrapper = mountView()
		expect(getProducts).toHaveBeenCalledTimes(1)

		wrapper.unmount()
		resolveCatalog(catalog([status('one'), status('two')]))
		await flushPromises()
		await vi.advanceTimersByTimeAsync(60_000)

		expect(getProducts).toHaveBeenCalledTimes(1)
	})

  it('refreshes shop sync statuses immediately after submitting a new shop', async () => {
    submitShop.mockResolvedValue({
      shop: { id: 'new', name: 'New', url: 'https://pay.ldxp.cn/shop/new', created_at: '2026-07-27T00:00:00Z', trust_level: 'neutral' },
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
