import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PublicAccountImportView from '../PublicAccountImportView.vue'
import type { PublicAccountImportProductsResponse } from '@/api/publicAccountImport'

const { getGroups, getProducts, getShops, fetchPublicSettings, get, post } = vi.hoisted(() => ({
  getGroups: vi.fn(), getProducts: vi.fn(), getShops: vi.fn(), fetchPublicSettings: vi.fn(), get: vi.fn(), post: vi.fn(),
}))
vi.mock('@/api/client', () => ({ apiClient: { get, post, patch: vi.fn(), delete: vi.fn() } }))
vi.mock('@/api/publicAccountImport', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountImport')>('@/api/publicAccountImport')
  return { ...actual, getPublicAccountImportGroups: getGroups, getPublicAccountImportShops: getShops,
    getPublicAccountImportProductsWithETag: async (...args: unknown[]) => ({ notModified: false, etag: '"catalog"', data: await getProducts(...args) }) }
})
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ siteName: 'Sub2API', siteLogo: '', fetchPublicSettings }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
vi.mock('vue-i18n', async () => { const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n'); return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${Object.values(params).join(' ')}` : key }) } })

const catalog: PublicAccountImportProductsResponse = { products: [{ id: 'product', shop_id: 'shop', shop_name: 'Shop', shop_url: 'https://wzyp.cn/shop/token', name: 'Verified product', url: 'https://wzyp.cn/item/goods', goods_type: 'card', price: 99, payable_price: 4, unit_price: 2, stock: 8, minimum_quantity: 2, quote_verified_at: '2020-01-01T00:00:00Z', updated_at: '2020-01-01T00:00:00Z' }], shop_count: 1, pending_shops: 0, queued_shops: 0, refreshing_shops: 0, failed_shops: 0, expired_shops: 0, refresh_seconds: 900, shop_sync_statuses: [] }
const RouterLinkStub = defineComponent({ template: '<a><slot /></a>' })
const HelpTooltipStub = defineComponent({ template: '<span><slot name="content" /></span>' })
function mountView(): VueWrapper { return mount(PublicAccountImportView, { global: { stubs: { RouterLink: RouterLinkStub, Icon: true, HelpTooltip: HelpTooltipStub } } }) }
async function openProduct(wrapper: VueWrapper) { await wrapper.findAll('main > div.grid button')[2].trigger('click'); await flushPromises(); const link = wrapper.find('a[href="https://wzyp.cn/item/goods"]'); await link.trigger('click'); await vi.advanceTimersByTimeAsync(1_000); await flushPromises() }

describe('PublicAccountImportView product task flow', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-09-13T00:00:00Z')); vi.spyOn(document, 'hidden', 'get').mockReturnValue(false); getGroups.mockResolvedValue([]); getShops.mockResolvedValue([]); getProducts.mockResolvedValue(catalog); fetchPublicSettings.mockReset(); get.mockReset(); post.mockReset() })
  afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

  it('posts a same-origin single-product task and opens the product after success', async () => {
    const replace = vi.fn(); vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close: vi.fn() } as any)
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } }); get.mockResolvedValue({ data: { state: 'succeeded' } })
    const wrapper = mountView(); await flushPromises(); await openProduct(wrapper)
    expect(post).toHaveBeenCalledWith('/public/account-import/products/refresh-one', { shop_id: 'shop', product_id: 'product' }); expect(replace).toHaveBeenCalledWith('https://wzyp.cn/item/goods'); wrapper.unmount()
  })

  it('keeps the old quote and shows the failed status when the task fails', async () => {
    const replace = vi.fn(); vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close: vi.fn() } as any)
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } }); get.mockResolvedValue({ data: { state: 'failed' } })
    const wrapper = mountView(); await flushPromises(); await openProduct(wrapper)
    expect(wrapper.text()).toContain('publicAccountImport.productVerificationFailed'); expect(wrapper.text()).toContain('¥4'); wrapper.unmount()
  })

  it('removes an unavailable product and closes the pending window', async () => {
    const close = vi.fn(); vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace: vi.fn() }, close } as any)
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } }); get.mockResolvedValue({ data: { state: 'unavailable' } })
    const wrapper = mountView(); await flushPromises(); await openProduct(wrapper)
    expect(close).toHaveBeenCalled(); expect(wrapper.text()).toContain('publicAccountImport.productUnavailable'); wrapper.unmount()
  })
})
