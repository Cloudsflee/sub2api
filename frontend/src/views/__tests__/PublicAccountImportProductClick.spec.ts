import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PublicAccountImportView from '../PublicAccountImportView.vue'
import type { PublicAccountImportProductsResponse } from '@/api/publicAccountImport'

const { getGroups, getProducts, getShops, fetchPublicSettings, get, post } = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getShops: vi.fn(),
  fetchPublicSettings: vi.fn(),
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({ apiClient: { get, post, patch: vi.fn(), delete: vi.fn() } }))
vi.mock('@/api/publicAccountImport', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountImport')>('@/api/publicAccountImport')
  return {
    ...actual,
    getPublicAccountImportGroups: getGroups,
    getPublicAccountImportShops: getShops,
    getPublicAccountImportProductsWithETag: async (...args: unknown[]) => ({
      notModified: false,
      etag: '"catalog"',
      data: await getProducts(...args),
    }),
  }
})
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ siteName: 'Sub2API', siteLogo: '', fetchPublicSettings }),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
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

const catalog: PublicAccountImportProductsResponse = {
  products: [{
    id: 'product',
    shop_id: 'shop',
    shop_name: 'Shop',
    shop_url: 'https://wzyp.cn/shop/token',
    name: 'Verified product',
    url: 'https://wzyp.cn/item/goods',
    goods_type: 'card',
    price: 99,
    payable_price: 4,
    unit_price: 2,
    stock: 8,
    minimum_quantity: 2,
    quote_verified_at: '2020-01-01T00:00:00Z',
    updated_at: '2020-01-01T00:00:00Z',
  }],
  shop_count: 1,
  pending_shops: 0,
  queued_shops: 0,
  refreshing_shops: 0,
  failed_shops: 0,
  expired_shops: 0,
  refresh_seconds: 900,
  shop_sync_statuses: [],
}

const RouterLinkStub = defineComponent({ template: '<a><slot /></a>' })
const HelpTooltipStub = defineComponent({ template: '<span><slot name="content" /></span>' })

function mountView(): VueWrapper {
  return mount(PublicAccountImportView, {
    global: { stubs: { RouterLink: RouterLinkStub, Icon: true, HelpTooltip: HelpTooltipStub } },
  })
}

async function productLink(wrapper: VueWrapper) {
  await wrapper.findAll('main > div.grid button')[2].trigger('click')
  await flushPromises()
  return wrapper.find('a[href="https://wzyp.cn/item/goods"]')
}

async function clickProduct(wrapper: VueWrapper): Promise<MouseEvent> {
  const link = await productLink(wrapper)
  const event = new MouseEvent('click', { bubbles: true, cancelable: true })
  link.element.dispatchEvent(event)
  await flushPromises()
  return event
}

describe('PublicAccountImportView product task flow', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-13T00:00:00Z'))
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    getGroups.mockResolvedValue([])
    getShops.mockResolvedValue([])
    getProducts.mockResolvedValue(catalog)
    fetchPublicSettings.mockReset()
    get.mockReset()
    post.mockReset()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it.each([
    '2026-09-12T23:59:30Z',
    '2026-09-12T23:58:59Z',
    '2020-01-01T00:00:00Z',
  ])('opens the real link without requesting a quote last verified at %s', async (verifiedAt) => {
    getProducts.mockResolvedValue({
      ...catalog,
      products: [{ ...catalog.products[0], quote_verified_at: verifiedAt }],
    })
    const wrapper = mountView()
    const event = await clickProduct(wrapper)
    await vi.advanceTimersByTimeAsync(5_000)

    expect(event.defaultPrevented).toBe(false)
    expect(post).not.toHaveBeenCalled()
    expect(get).not.toHaveBeenCalled()
    expect(wrapper.find('a[href="https://wzyp.cn/item/goods"]').attributes('target')).toBe('_blank')
    expect(wrapper.find('a[href="https://wzyp.cn/item/goods"]').attributes('href')).toBe('https://wzyp.cn/item/goods')
    expect(wrapper.find('[data-product-id="product"]').attributes('aria-busy')).toBe('false')
    expect(wrapper.find('[data-product-refresh="product"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('¥4')
    expect(wrapper.text()).toContain('publicAccountImport.priceVerifiedAt')
    expect(wrapper.text()).not.toContain('publicAccountImport.priceChecking')
    expect(wrapper.text()).not.toContain('about:blank')
    wrapper.unmount()
  })

  it('refreshes only on request and preserves navigation and the cached quote on failure', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state: 'failed' } })
    const wrapper = mountView()
    await productLink(wrapper)
    expect(post).not.toHaveBeenCalled()
    await wrapper.get('[data-product-refresh="product"]').trigger('click')
    expect(wrapper.text()).toContain('publicAccountImport.priceChecking')
    const event = await clickProduct(wrapper)
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/public/account-import/products/refresh-one', {
      shop_id: 'shop',
      product_id: 'product',
    })
    await vi.advanceTimersByTimeAsync(1_000)
    await flushPromises()

    expect(event.defaultPrevented).toBe(false)
    expect(wrapper.text()).toContain('publicAccountImport.priceCheckFailed')
    expect(wrapper.text()).toContain('¥4')
    expect(wrapper.find('a[href="https://wzyp.cn/item/goods"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('removes a product when manual refresh reports it unavailable', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state: 'unavailable' } })
    const wrapper = mountView()
    await productLink(wrapper)
    await wrapper.get('[data-product-refresh="product"]').trigger('click')
    await vi.advanceTimersByTimeAsync(1_000)
    await flushPromises()

    expect(wrapper.text()).toContain('publicAccountImport.productUnavailable')
    expect(wrapper.find('[data-product-id="product"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
