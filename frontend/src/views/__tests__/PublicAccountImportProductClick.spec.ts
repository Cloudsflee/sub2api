import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'
import type { PublicAccountImportProductsResponse } from '@/api/publicAccountImport'

const { getGroups, getProducts, getShops, fetchPublicSettings, getCatalogResult } = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getCatalogResult: vi.fn(),
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
    getPublicAccountImportProductsWithETag: async (...args: unknown[]) => getCatalogResult(...args) || ({
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

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAdmin: false }),
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
  const link = wrapper.find('a[href="https://wzyp.cn/item/goods"]')
  expect(link.exists()).toBe(true)
  await link.trigger('click')
  await vi.advanceTimersByTimeAsync(3000)
  await flushPromises()
}

describe('PublicAccountImportView product click verification', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-13T00:00:00Z'))
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    getCatalogResult.mockReset()
    getGroups.mockReset().mockResolvedValue([])
    getProducts.mockReset().mockResolvedValue(catalog)
    getShops.mockReset().mockResolvedValue([])
    fetchPublicSettings.mockReset()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
		vi.unstubAllGlobals()
  })

  it('opens the product URL when a live quote is temporarily unknown', async () => {
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

    expect(replace).toHaveBeenCalledWith('https://wzyp.cn/item/goods')
    expect(close).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('publicAccountImport.productVerificationFailed')
    expect(wrapper.text()).toContain('¥4')
    expect(wrapper.text()).not.toContain('¥1000')
    wrapper.unmount()
  })

  it('writes the live payable price back before opening the product URL', async () => {
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

    expect(replace).toHaveBeenCalledWith('https://wzyp.cn/item/goods')
    expect(close).not.toHaveBeenCalled()
		expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toMatchObject({ quantity: 1 })
    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      'https://wzyp.cn/shopApi/Shop/goodsInfo',
      'https://wzyp.cn/shopApi/Shop/getUserChannel',
      'https://wzyp.cn/shopApi/Shop/getGoodsPrice',
    ])
    expect(wrapper.text()).not.toContain('¥4')
    expect(wrapper.text()).toContain('¥500')
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

    expect(replace).toHaveBeenCalledWith('https://wzyp.cn/item/goods')
    expect(close).not.toHaveBeenCalled()
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toMatchObject({ quantity: 1 })
    wrapper.unmount()
  })

	it('opens the product URL after a 403 even when the catalog quote is old or incomplete', async () => {
		getProducts.mockResolvedValue({
			...catalog,
			products: [{
				...catalog.products[0],
				quote_verified_at: '2020-01-01T00:00:00Z',
				payable_price: undefined,
				unit_price: undefined,
			}],
		})
		const replace = vi.fn()
		const close = vi.fn()
		vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>403 Forbidden</html>', {
			status: 403,
			headers: { 'Content-Type': 'text/html' },
		})))
		const wrapper = mountView()
		await flushPromises()

		await openProduct(wrapper)

		expect(replace).toHaveBeenCalledWith('https://wzyp.cn/item/goods')
		expect(close).not.toHaveBeenCalled()
		expect(wrapper.text()).toContain('publicAccountImport.productVerificationFailed')
		wrapper.unmount()
	})

	it('blocks navigation when the catalog product URL is invalid', async () => {
		getProducts.mockResolvedValue({
			...catalog,
			products: [{ ...catalog.products[0], url: 'https://pay.ldxp.cn/shop/not-a-product' }],
		})
		const replace = vi.fn()
		const close = vi.fn()
		vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
		const fetchMock = vi.fn()
		vi.stubGlobal('fetch', fetchMock)
		const wrapper = mountView()
		await flushPromises()

		const tab = wrapper.findAll('main > div.grid button')[2]
		expect(tab).toBeDefined()
		await tab.trigger('click')
		const link = wrapper.find('a[href="#"]')
		expect(link.exists()).toBe(true)
		await link.trigger('click')
		await flushPromises()

		expect(fetchMock).not.toHaveBeenCalled()
		expect(close).not.toHaveBeenCalled()
		expect(window.open).not.toHaveBeenCalled()
		expect(replace).not.toHaveBeenCalled()
		expect(wrapper.text()).toContain('publicAccountImport.productLinkInvalid')
		wrapper.unmount()
	})

	it('does not fall back when live verification explicitly says the product is unavailable', async () => {
		getProducts.mockResolvedValue({
			...catalog,
			products: [{ ...catalog.products[0], quote_verified_at: '2020-01-01T00:00:00Z' }],
		})
		const replace = vi.fn()
		const close = vi.fn()
		vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
			code: 0,
			msg: '商品未上架，如有疑问请联系商家',
			data: null,
		}), { status: 200, headers: { 'Content-Type': 'application/json' } })))
		const wrapper = mountView()
		await flushPromises()

		await openProduct(wrapper)

		expect(close).toHaveBeenCalledOnce()
		expect(replace).not.toHaveBeenCalled()
		expect(wrapper.text()).toContain('publicAccountImport.productUnavailable')
		wrapper.unmount()
	})
})


function liveFetch() {
  return vi.fn(async (url: string, _options?: RequestInit) => new Response(JSON.stringify(
    url.endsWith('goodsInfo') ? {
      code: 1, data: { status: 1, user: { token: 'shop' }, goods_type: 'card', extend: { stock_count: 20, limit_count: 3 } },
    } : url.endsWith('getUserChannel') ? { code: 1, data: [{ id: 7, is_default: true }] }
      : { code: 1, data: { total_amount: 500 } }
  ), { headers: { 'Content-Type': 'application/json' } }))
}

function itemCatalog(count: number) {
  return { ...catalog, products: Array.from({ length: count }, (_, i) => ({
    ...catalog.products[0], id: `product-${i}`, name: `Product ${i}`, url: `https://wzyp.cn/item/goods-${i}`,
    payable_price: i + 1, unit_price: (i + 1) / 2,
  })) }
}

async function productTab(wrapper: VueWrapper) {
  await wrapper.findAll('main > div.grid button')[2].trigger('click')
  await vi.advanceTimersByTimeAsync(1)
}

function infoKeys(fetchMock: ReturnType<typeof liveFetch>): string[] {
  return fetchMock.mock.calls.filter(([url]) => url.endsWith('goodsInfo')).map(([, options]) => JSON.parse(String(options?.body)).goods_key)
}

describe('PublicAccountImportView automatic browser quotes', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-13T00:00:00Z'))
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    getGroups.mockReset().mockResolvedValue([])
    getProducts.mockReset().mockResolvedValue(catalog)
    getCatalogResult.mockReset()
    getShops.mockReset().mockResolvedValue([])
    fetchPublicSettings.mockReset()
  })
  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('captures only ten visible products, re-sorts updated prices, and does not extend batches or poll live prices', async () => {
    getProducts.mockResolvedValue(itemCatalog(12))
    const fetchMock = liveFetch()
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchMock).not.toHaveBeenCalled()
    await wrapper.findAll('main > div.grid button')[1].trigger('click')
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchMock).not.toHaveBeenCalled()
    await productTab(wrapper)
    expect(wrapper.find('[data-product-id="product-0"]').text()).toContain('¥1')
    await vi.advanceTimersByTimeAsync(23_000)
    expect(infoKeys(fetchMock)).toEqual(Array.from({ length: 10 }, (_, i) => `goods-${i}`))
    expect(wrapper.findAll('[data-product-id]')[0].attributes('data-product-id')).toBe('product-10')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMock).toHaveBeenCalledTimes(30)
    expect(wrapper.text()).toContain('¥500')
    expect(wrapper.text()).not.toContain('publicAccountImport.priceVerified ')
    wrapper.unmount()
  })

  it('cancels the old page, debounces search and its page reset into one batch, and honors explicit sorting', async () => {
    getProducts.mockResolvedValue(itemCatalog(12))
    const fetchMock = liveFetch()
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView()
    await flushPromises()
    await productTab(wrapper)
    const next = wrapper.findAll('button').find(button => button.text() === 'publicAccountImport.nextPage')!
    await next.trigger('click')
    await vi.advanceTimersByTimeAsync(700)
    expect(infoKeys(fetchMock)).toContain('goods-10')
    const search = wrapper.find('#public-product-search')
    await search.setValue('Product 1')
    await vi.advanceTimersByTimeAsync(150)
    await search.setValue('Product 11')
    const count = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(299)
    expect(fetchMock).toHaveBeenCalledTimes(count)
    await vi.advanceTimersByTimeAsync(5000)
    expect(infoKeys(fetchMock).filter(key => key === 'goods-11')).toHaveLength(1)
    expect(wrapper.findAll('[data-product-id]')).toHaveLength(1)
    await search.setValue('')
    await vi.advanceTimersByTimeAsync(301)
    const sort = wrapper.findAll('button').find(button => button.text() === 'publicAccountImport.priceDescending')!
    await sort.trigger('click')
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.findAll('[data-product-id]')[0].text()).toContain('¥500')
    wrapper.unmount()
  })

  it('coalesces focus and visibility returns and pauses automatic work while hidden or on another tab', async () => {
    let hidden = false
    vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
    const fetchMock = liveFetch()
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountView()
    await flushPromises()
    await productTab(wrapper)
    await vi.advanceTimersByTimeAsync(3000)
    hidden = true
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(65_000)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(6)
    await wrapper.findAll('main > div.grid button')[0].trigger('click')
    await vi.advanceTimersByTimeAsync(65_000)
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(6)
    wrapper.unmount()
  })

  it('keeps price and successful time together across a 304 and accepts a newer server quote', async () => {
    vi.stubGlobal('fetch', liveFetch())
    const wrapper = mountView()
    await flushPromises()
    await productTab(wrapper)
    await vi.advanceTimersByTimeAsync(3000)
    getCatalogResult.mockReturnValue({ notModified: true, etag: '"catalog"', data: null })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(wrapper.text()).toContain('¥500')
    getCatalogResult.mockReset()
    getProducts.mockResolvedValue({ ...catalog, products: [{ ...catalog.products[0], payable_price: 6, unit_price: 3, quote_verified_at: new Date(Date.now()).toISOString() }] })
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(500)
    expect(wrapper.text()).toContain('¥6')
    expect(wrapper.text()).not.toContain('¥500')
    expect(wrapper.text()).toContain('publicAccountImport.priceVerified')
    wrapper.unmount()
  })

  it('provides a separate keyboard-accessible refresh button, shares clicks and closes pending windows on unmount', async () => {
    const fetchMock = liveFetch()
    vi.stubGlobal('fetch', fetchMock)
    const replace = vi.fn()
    const close = vi.fn()
    vi.spyOn(window, 'open').mockReturnValue({ opener: null, location: { replace }, close } as any)
    const wrapper = mountView()
    await flushPromises()
    await productTab(wrapper)
    expect(wrapper.find('a button').exists()).toBe(false)
    const button = wrapper.find('[data-product-refresh]')
    expect(button.element.tagName).toBe('BUTTON')
    expect(button.attributes('type')).toBe('button')
    expect(button.attributes('aria-label')).toContain('Verified product')
    await wrapper.find('a[href="https://wzyp.cn/item/goods"]').trigger('click')
    await wrapper.find('a[href="https://wzyp.cn/item/goods"]').trigger('click')
    expect(window.open).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(replace).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(13_000)
    await button.trigger('click')
    expect(window.open).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(6)
    await vi.advanceTimersByTimeAsync(61_000)
    fetchMock.mockImplementation(() => new Promise<Response>(() => {}))
    await wrapper.find('a[href="https://wzyp.cn/item/goods"]').trigger('click')
    wrapper.unmount()
    expect(close).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(9000)
    expect(replace).toHaveBeenCalledTimes(1)
  })
})
