import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import * as productCatalog from '@/utils/publicProductCatalog'
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
    adaptive_rate_per_second: 1.5,
    global_pressure_state: 'clear',
    global_pressure_until: '',
    global_silence_until: '',
    last_pressure_kind: '',
    egress_circuits: [],
    waf_response_fingerprint: null,
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

describe('PublicAccountImportView expired snapshots', () => {
  const now = Date.parse('2026-11-01T12:00:00Z')
  let wrapper: VueWrapper | undefined

  function snapshot(shopId: string, ageMinutes: number): PublicAccountImportProductSyncStatus {
    const updatedAt = new Date(now - ageMinutes * 60_000).toISOString()
    return {
      shop_id: shopId, state: 'idle', updated_at: updatedAt,
      snapshot_updated_at: updatedAt,
      snapshot_expires_at: new Date(now - (ageMinutes - 30) * 60_000).toISOString(),
      snapshot_state: ageMinutes > 30 ? 'expired' : 'stale', retry_after_seconds: 0,
    }
  }

  function product(id: string, shopId: string, price = 1, name = 'Key product'): PublicAccountImportProduct {
    return {
      id, shop_id: shopId, shop_name: shopId, shop_url: 'https://wzyp.cn/shop/' + shopId,
      name, url: 'https://wzyp.cn/item/' + id, goods_type: 'card', price, unit_price: price,
      payable_price: price, stock: 5, minimum_quantity: 1,
      updated_at: new Date(now).toISOString(), quote_verified_at: new Date(now).toISOString(),
    }
  }

  async function openCatalog(products: PublicAccountImportProduct[], statuses: PublicAccountImportProductSyncStatus[]) {
    getProducts.mockResolvedValue({ ...catalog(statuses), products })
    wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('publicAccountImport.productModule'))!.trigger('click')
    return wrapper
  }

  function ids() {
    return wrapper!.findAll('[data-product-id]').map(card => card.attributes('data-product-id'))
  }

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(now)
    getGroups.mockReset().mockResolvedValue([])
    getShops.mockReset().mockResolvedValue([])
    getProducts.mockReset()
    fetchPublicSettings.mockReset()
    authState.isAdmin = false
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('shows long-expired snapshots by default, hides all expired products, and resets the switch on remount', async () => {
    const view = await openCatalog([
      product('expired', 'expired', 1), product('fresh', 'fresh', 2), product('old', 'old', 3),
      product('year-old', 'year-old', 4), product('legacy', 'legacy', 5),
    ], [snapshot('expired', 60), snapshot('fresh', 5), snapshot('old', 211), snapshot('year-old', 365 * 24 * 60),
      { ...snapshot('legacy', 500), snapshot_state: 'legacy' }])
    expect(ids()).toEqual(['expired', 'fresh', 'old', 'year-old', 'legacy'])
    const card = view.get('[data-product-id="expired"]')
    expect(card.get('[data-product-snapshot-expired]').text()).toBe('publicAccountImport.shopProductsExpired')
    expect(card.find('[title="2026-11-01T11:00:00.000Z"]').exists()).toBe(true)
    expect(view.findAll('[data-product-snapshot-expired]')).toHaveLength(3)
    const toggle = view.get('[data-testid="hide-expired-products"]')
    expect((toggle.element as HTMLInputElement).checked).toBe(false)
    await toggle.setValue(true)
    expect(ids()).toEqual(['fresh', 'legacy'])
    await toggle.setValue(false)
    expect(ids()).toEqual(['expired', 'fresh', 'old', 'year-old', 'legacy'])
    await toggle.setValue(true)
    view.unmount()
    wrapper = mountView()
    await flushPromises()
    expect((wrapper.get('[data-testid="hide-expired-products"]').element as HTMLInputElement).checked).toBe(false)
    expect(ids()).toEqual(['expired', 'fresh', 'old', 'year-old', 'legacy'])
  })

  it('combines expiry, inclusion/exclusion search, price sorting, and pagination resets', async () => {
    const products = Array.from({ length: 24 }, (_, i) => product(
      String(i + 1), i < 12 ? 'fresh' : 'expired', i + 1, i === 11 ? 'Key skip' : 'Key product'
    ))
    const view = await openCatalog(products, [snapshot('fresh', 5), snapshot('expired', 365 * 24 * 60)])
    // Use the pagination control rather than changing component internals.
    const nextPage = view.findAll('button').find(button => button.text() === 'publicAccountImport.nextPage')!
    await nextPage.trigger('click')
    expect(ids()[0]).toBe('11')
    await view.get('[data-testid="hide-expired-products"]').setValue(true)
    expect(ids()[0]).toBe('1')
    expect(view.text()).toContain('publicAccountImport.pageStatus 1 2')
    await view.get('#public-product-search').setValue('key -skip')
    await vi.advanceTimersByTimeAsync(300)
    await view.findAll('button').find(button => button.text() === 'publicAccountImport.priceDescending')!.trigger('click')
    expect(ids()).toEqual(['11', '10', '9', '8', '7', '6', '5', '4', '3', '2'])
    await view.get('[data-testid="hide-expired-products"]').setValue(false)
    expect(ids()[0]).toBe('24')
    expect(view.text()).toContain('publicAccountImport.pageStatus 1 3')
  })

  it('expires while open and stays visible past the former cutoff without re-sorting on clock ticks', async () => {
    const sort = vi.spyOn(productCatalog, 'filterAndSortPublicProducts')
    const view = await openCatalog([product('boundary', 'boundary')], [snapshot('boundary', 30)])
    const initialSorts = sort.mock.calls.length
    getProducts.mockRejectedValue(new Error('refresh failed'))
    expect(view.find('[data-product-snapshot-expired]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(1000)
    expect(view.find('[data-product-snapshot-expired]').exists()).toBe(true)
    await view.get('[data-testid="hide-expired-products"]').setValue(true)
    expect(ids()).toEqual([])
    await view.get('[data-testid="hide-expired-products"]').setValue(false)
    expect(ids()).toEqual(['boundary'])
    vi.setSystemTime(now + 3 * 60 * 60_000 - 1000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(ids()).toEqual(['boundary'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(ids()).toEqual(['boundary'])
    vi.setSystemTime(now + 365 * 86400_000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(ids()).toEqual(['boundary'])
    expect(view.find('[data-product-snapshot-expired]').exists()).toBe(true)
    await view.get('[data-testid="hide-expired-products"]').setValue(true)
    expect(ids()).toEqual([])
    await view.get('[data-testid="hide-expired-products"]').setValue(false)
    expect(ids()).toEqual(['boundary'])
    expect(sort).toHaveBeenCalledTimes(initialSorts)
  })

  it('keeps expired results on failed refresh and clears the badge after a successful snapshot refresh', async () => {
    const item = product('one', 'one')
    const oldSnapshot = snapshot('one', 365 * 24 * 60)
    const view = await openCatalog([item], [oldSnapshot])
    getProducts.mockRejectedValue(new Error('refresh failed'))
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(ids()).toEqual(['one'])
    expect(view.find('[data-product-snapshot-expired]').exists()).toBe(true)
    expect(view.find(`[title="${oldSnapshot.snapshot_updated_at}"]`).exists()).toBe(true)
    expect(view.find(`[title="${item.quote_verified_at}"]`).exists()).toBe(true)
    await view.get('[data-testid="hide-expired-products"]').setValue(true)
    expect(ids()).toEqual([])
    getProducts.mockResolvedValue({ ...catalog([snapshot('one', 0)]), products: [item] })
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(ids()).toEqual(['one'])
    expect(view.find('[data-product-snapshot-expired]').exists()).toBe(false)
    // A successful shop sync follows the latest catalog, including removals.
    getProducts.mockResolvedValue({ ...catalog([snapshot('one', 0)]), products: [] })
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(ids()).toEqual([])
  })

  it('hides newly expired products automatically when the switch is enabled, including on a 304 response', async () => {
    const view = await openCatalog([product('one', 'one')], [snapshot('one', 30)])
    await view.get('[data-testid="hide-expired-products"]').setValue(true)
    // The API adapter's no-data branch is also used for HTTP 304.
    getProducts.mockResolvedValue(undefined)
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(ids()).toEqual(['one'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(ids()).toEqual([])
  })
})

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

  it('shows confirmed tiers and pending classification without inferring from visible products', async () => {
    const one = { ...status('one'), schedule: {
      tier: '11-50' as const, state: 'active' as const, effective_count: 51,
      catalog_seconds: 6480, quote_seconds: 25920, tier_pending: true,
    } }
    const two = { ...status('two'), schedule: {
      tier: 'pending' as const, state: 'pending' as const, effective_count: null,
      catalog_seconds: 0, quote_seconds: 0, tier_pending: false,
    } }
    getProducts.mockResolvedValue(catalog([one, two]))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)
    const row = wrapper.get('[data-shop-id="one"]')
    expect(row.get('[data-shop-tier]').attributes('data-shop-tier')).toBe('11-50')
    expect(row.text()).toContain('publicAccountImport.shopEffectiveProducts 51')
    expect(row.text()).toContain('publicAccountImport.shopTierConfirming')
    expect(row.text()).toContain('publicAccountImport.shopIntervalHours 7.2')
    expect(wrapper.get('[data-shop-id="two"]').text()).toContain('publicAccountImport.shopTier_pending')
    expect(wrapper.get('[data-shop-id="two"]').text()).not.toContain('publicAccountImport.shopEffectiveProducts')
    expect(wrapper.get('[data-shop-id="other"]').find('[data-shop-schedule]').exists()).toBe(false)
    requestRefresh.mockResolvedValue({ ...status('one', 'queued'), accepted: true })
    await row.get('button[data-shop-product-refresh]').trigger('click')
    await flushPromises()
    expect(row.get('[data-shop-tier]').attributes('data-shop-tier')).toBe('11-50')
    wrapper.unmount()
  })

  it('shows zero sellable items without quote cycles and retains the tier during pressure pauses', async () => {
    getProducts.mockResolvedValue(catalog([
      { ...status('one'), schedule: { tier: 'unavailable', state: 'dormant', effective_count: 0, catalog_seconds: 28800, quote_seconds: 0, tier_pending: false } },
      { ...status('two'), schedule: { tier: '201-1000', state: 'pressure_paused', effective_count: 800, catalog_seconds: 0, quote_seconds: 0, tier_pending: false } },
    ]))
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)
    expect(wrapper.get('[data-shop-id="one"]').text()).toContain('publicAccountImport.shopEffectiveProducts 0')
    expect(wrapper.get('[data-shop-id="one"]').text()).toContain('publicAccountImport.shopNoAutomaticQuotes')
    const paused = wrapper.get('[data-shop-id="two"]')
    expect(paused.get('[data-shop-tier]').attributes('data-shop-tier')).toBe('201-1000')
    expect(paused.text()).toContain('publicAccountImport.shopSchedule_pressure_paused')
    expect(paused.text()).not.toContain('publicAccountImport.shopQuotePeriod')
    wrapper.unmount()
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

  it('shows the next retry time for an unavailable blocked lane', async () => {
    const statusValue = workerStatus([2], 'shop API returned non-JSON HTTP 403')
    statusValue.lanes[1].state = 'blocked'
    statusValue.lanes[1].retry_at = '2026-08-17T12:05:00Z'
    statusValue.lanes[1].challenge_state = 'clear'
    getProducts.mockResolvedValue({
      ...catalog([status('one')]),
      worker_status: statusValue,
    })
    const wrapper = mountView()
    await flushPromises()
    await openShopsTab(wrapper)

    const lane = wrapper.find('[data-product-sync-lane="2"]')
    expect(lane.find('[data-product-sync-lane-retry-at]').exists()).toBe(true)
    expect(lane.find('[data-product-sync-lane-retry-at]').text()).toContain('publicAccountImport.productSyncLaneRetryAt')
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
