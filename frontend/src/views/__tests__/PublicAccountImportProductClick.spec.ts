import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'
import type { PublicAccountImportProductsResponse } from '@/api/publicAccountImport'

const { getGroups, getProducts, getShops, fetchPublicSettings } = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getShops: vi.fn(),
  fetchPublicSettings: vi.fn(),
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
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ siteName: 'Sub2API', siteLogo: '', fetchPublicSettings }),
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

const catalog: PublicAccountImportProductsResponse = {
  products: [{
    id: 'product',
    shop_id: 'shop',
    shop_name: 'Shop',
    shop_url: 'https://pay.ldxp.cn/shop/token',
    name: 'Verified product',
    url: 'https://pay.ldxp.cn/item/goods',
    goods_type: 'card',
    price: 99,
    payable_price: 4,
    unit_price: 2,
    stock: 8,
    minimum_quantity: 2,
    quote_verified_at: '2026-07-28T02:00:00Z',
    updated_at: '2026-07-28T02:00:00Z',
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
    global: {
      stubs: { RouterLink: RouterLinkStub, Icon: true, HelpTooltip: HelpTooltipStub },
    },
  })
}

async function openProduct(wrapper: VueWrapper) {
	const tab = wrapper.findAll('main > div.grid button')[2]
	expect(tab).toBeDefined()
	await tab.trigger('click')
  const link = wrapper.find('a[href="https://pay.ldxp.cn/item/goods"]')
  expect(link.exists()).toBe(true)
  await link.trigger('click')
  await flushPromises()
}

describe('PublicAccountImportView product click verification', () => {
  beforeEach(() => {
    getGroups.mockReset().mockResolvedValue([])
    getProducts.mockReset().mockResolvedValue(catalog)
    getShops.mockReset().mockResolvedValue([])
    fetchPublicSettings.mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
		vi.unstubAllGlobals()
  })

  it('blocks navigation and closes the pre-opened window when a quote is unknown', async () => {
    const replace = vi.fn()
    const close = vi.fn()
    vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: { status: 1, user: { token: 'shop-token' }, price: 1000, extend: { stock_count: 8, limit_count: 2 } },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: [{ id: 1, status: 1, is_default: 1 }],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 0, msg: '请求频繁，请稍后重试' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })))
    const wrapper = mountView()
    await flushPromises()

    await openProduct(wrapper)

    expect(close).toHaveBeenCalledOnce()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('publicAccountImport.productVerificationFailed')
    expect(wrapper.text()).toContain('¥4')
    expect(wrapper.text()).not.toContain('¥1000')
    wrapper.unmount()
  })

  it('opens the product URL after a valid quote without changing the catalog price', async () => {
    const replace = vi.fn()
    const close = vi.fn()
    vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: { status: 1, user: { token: 'shop-token' }, price: 1000, extend: { stock_count: 8, limit_count: 0 } },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: [{ id: 1, status: 1, is_default: 1 }],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 1, data: { total_amount: 500 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView()
    await flushPromises()

    await openProduct(wrapper)

    expect(replace).toHaveBeenCalledWith('https://pay.ldxp.cn/item/goods')
    expect(close).not.toHaveBeenCalled()
		expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toMatchObject({ quantity: 1 })
    expect(wrapper.text()).toContain('¥4')
    expect(wrapper.text()).not.toContain('¥500')
    wrapper.unmount()
  })

  it('quotes an inventoryless article with quantity one before opening it', async () => {
    const replace = vi.fn()
    const close = vi.fn()
    vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: {
          status: 1,
          goods_type: 'article',
          user: { token: 'shop-token' },
          extend: { has_buy: 0, paid_type: 1 },
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        code: 1,
        data: [{ id: 1, status: 1, is_default: 1 }],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 1, data: { total_amount: 5.16 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView()
    await flushPromises()

    await openProduct(wrapper)

    expect(replace).toHaveBeenCalledWith('https://pay.ldxp.cn/item/goods')
    expect(close).not.toHaveBeenCalled()
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toMatchObject({ quantity: 1 })
    wrapper.unmount()
  })
})
