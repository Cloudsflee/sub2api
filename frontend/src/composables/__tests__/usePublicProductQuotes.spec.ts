import { effectScope, ref, type EffectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'
import { usePublicProductQuotes } from '../usePublicProductQuotes'

const base: PublicAccountImportProduct = {
  id: 'one', shop_id: 'shop', shop_name: 'Shop', shop_url: 'https://wzyp.cn/shop/shop',
  name: 'Product', url: 'https://pay.ldxp.cn/item/one', goods_type: 'card',
  price: 99, payable_price: 4, unit_price: 2, minimum_quantity: 2, stock: 8,
  market_price: 1000, updated_at: '2026-01-01T00:00:00Z', quote_verified_at: '2026-01-01T00:00:00Z',
}
function product(id: string): PublicAccountImportProduct { return { ...base, id, url: `https://wzyp.cn/item/${id}` } }
const json = (data: unknown, status = 200, headers = {}) => new Response(JSON.stringify(data), {
  status, headers: { 'Content-Type': 'application/json', ...headers },
})
function details(extra = {}) {
  return { code: 1, data: { status: 1, user: { token: 'shop' }, goods_type: 'card', extend: { stock_count: 12, limit_count: 3 }, ...extra } }
}
function successfulFetch() {
  return vi.fn(async (url: string, _options?: RequestInit) => {
    if (url.endsWith('goodsInfo')) return json(details())
    if (url.endsWith('getUserChannel')) return json({ code: 1, data: [{ id: 7, is_default: true }] })
    return json({ code: 1, data: { total_amount: 500 } })
  })
}
let scope: EffectScope
function setup(items = [base]) {
  const catalog = ref(items)
  scope = effectScope()
  const quotes = scope.run(() => usePublicProductQuotes(catalog))!
  return { catalog, ...quotes }
}

describe('browser product quotes', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-13T00:00:00Z'))
  })
  afterEach(() => {
    scope?.stop()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('atomically writes total, unrounded unit price, latest quantity, stock and successful time', async () => {
    const fetchMock = successfulFetch()
    vi.stubGlobal('fetch', fetchMock)
    const q = setup()
    const task = q.request(base, 'click')
    expect(q.products.value[0].payable_price).toBe(4)
    expect(q.status(q.products.value[0])).toBe('checking')
    await vi.advanceTimersByTimeAsync(1400)
    expect(await task).toEqual({ kind: 'success', url: 'https://wzyp.cn/item/one' })
    expect(q.products.value[0]).toMatchObject({
      price: 99, payable_price: 500, unit_price: 500 / 3, minimum_quantity: 3, stock: 12,
      goods_type: 'card', market_price: undefined, updated_at: base.updated_at,
      quote_verified_at: '2026-09-13T00:00:01.334Z',
    })
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body))).toMatchObject({ quantity: 3, coupon_code: '', channel_id: 7 })
    expect(fetchMock.mock.calls.every(([, options]) => options?.credentials === 'include')).toBe(true)
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toMatchObject({ trade_no: '' })
    expect(q.status(q.products.value[0])).toBe('verified')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(q.status(q.products.value[0])).toBe('historical')
    expect(q.products.value[0].payable_price).toBe(500)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('shares a promoted task, reuses success for 60 seconds, and enforces a 15 second forced-refresh cooldown', async () => {
    const fetchMock = successfulFetch()
    vi.stubGlobal('fetch', fetchMock)
    const q = setup()
    const automatic = q.request(base)
    expect(q.request(base, 'click')).toBe(automatic)
    expect(q.request(base, 'refresh')).toBe(automatic)
    q.cancelAutomatic()
    await vi.advanceTimersByTimeAsync(1400)
    expect((await automatic).kind).toBe('success')
    await q.request(base, 'refresh')
    expect(fetchMock).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(13_600)
    await q.request(base, 'click')
    expect(fetchMock).toHaveBeenCalledTimes(3)
    const refresh = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1400)
    expect((await refresh).kind).toBe('success')
    expect(fetchMock).toHaveBeenCalledTimes(6)
  })

  it('limits a captured batch to 10 products and spaces every HTTP start by at least 667 ms', async () => {
    const starts: number[] = []
    const handler = successfulFetch()
    const fetchMock = vi.fn((url: string) => { starts.push(Date.now()); return handler(url) })
    vi.stubGlobal('fetch', fetchMock)
    const items = Array.from({ length: 12 }, (_, i) => product(String(i)))
    const q = setup(items)
    q.startBatch(items)
    await vi.advanceTimersByTimeAsync(25_000)
    expect(fetchMock).toHaveBeenCalledTimes(30)
    expect(starts.slice(1).every((at, i) => at - starts[i] >= 667)).toBe(true)
    expect(q.products.value.filter(item => item.payable_price === 500)).toHaveLength(10)
  })

  it('runs at most two goods queries concurrently and starts manual queued work first', async () => {
    const responses: Array<(response: Response) => void> = []
    const fetchMock = vi.fn((_url: string, _options: RequestInit) => new Promise<Response>(resolve => responses.push(resolve)))
    vi.stubGlobal('fetch', fetchMock)
    const items = ['a', 'b', 'c', 'd'].map(product)
    const q = setup(items)
    q.startBatch(items.slice(0, 3))
    await vi.advanceTimersByTimeAsync(2000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const manual = q.request(items[3], 'click')
    responses[0](json({ code: 0, msg: 'temporary failure' }))
    await vi.advanceTimersByTimeAsync(1)
    expect(JSON.parse(String(fetchMock.mock.calls[2][1]?.body)).goods_key).toBe('d')
    q.dispose()
    expect((await manual).kind).toBe('cancelled')
  })

  it('prioritizes promoted HTTP work without a burst', async () => {
    const fetchMock = successfulFetch()
    vi.stubGlobal('fetch', fetchMock)
    const items = ['a', 'b'].map(product)
    const q = setup(items)
    q.startBatch(items)
    await vi.advanceTimersByTimeAsync(1)
    q.request(items[0], 'click')
    await vi.advanceTimersByTimeAsync(666)
    expect(String(fetchMock.mock.calls[1][0])).toContain('getUserChannel')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('counts manual queue time in the deadline and repeated clicks never extend it', async () => {
    const fetchMock = vi.fn(() => new Promise<Response>(() => {}))
    vi.stubGlobal('fetch', fetchMock)
    const items = ['a', 'b', 'c'].map(product)
    const q = setup(items)
    q.startBatch(items.slice(0, 2))
    const click = q.request(items[2], 'click')
    await vi.advanceTimersByTimeAsync(7000)
    expect(q.request(items[2], 'click')).toBe(click)
    await vi.advanceTimersByTimeAsync(1000)
    expect(await click).toMatchObject({ kind: 'failed', reason: 'timeout' })
    expect(q.status(q.products.value[2])).toBe('failed')
  })

  it('aborts hanging requests at 8 seconds and ignores responses that arrive after timeout', async () => {
    let respond!: (value: Response) => void
    const fetchMock = vi.fn((_url: string, _options: RequestInit) => new Promise<Response>(resolve => { respond = resolve }))
    vi.stubGlobal('fetch', fetchMock)
    const q = setup()
    const task = q.request(base, 'click')
    await vi.advanceTimersByTimeAsync(8000)
    expect(await task).toMatchObject({ kind: 'failed', reason: 'timeout' })
    expect(fetchMock.mock.calls[0][1].signal?.aborted).toBe(true)
    respond(json(details()))
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(q.products.value[0].payable_price).toBe(4)
    expect(q.products.value[0].quote_verified_at).toBe(base.quote_verified_at)
  })

  it.each([403, 429, 'connection'] as const)('pauses remaining automatic work for pressure %s while allowing clicks', async failure => {
    const fetchMock = vi.fn().mockImplementation(() => failure === 'connection'
      ? Promise.reject(new TypeError('Failed to fetch'))
      : Promise.resolve(json({}, failure, { 'Retry-After': '120' })))
    vi.stubGlobal('fetch', fetchMock)
    const items = ['a', 'b', 'c'].map(product)
    const q = setup(items)
    q.startBatch(items)
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(q.status(q.products.value[0])).toBe('failed')
    expect(q.status(q.products.value[1])).toBe('historical')
    expect(q.pausedUntil.value).toBe(new Date('2026-09-13T00:00:00Z').getTime() + (failure === 429 ? 120_000 : 60_000))
    q.startBatch(items.slice(1))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const click = q.request(items[1], 'click')
    await vi.advanceTimersByTimeAsync(1000)
    expect((await click).kind).toBe('failed')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('preserves local quotes across old, equal and timestampless snapshots; accepts only a newer valid quote', async () => {
    vi.stubGlobal('fetch', successfulFetch())
    const q = setup()
    const task = q.request(base)
    await vi.advanceTimersByTimeAsync(1400)
    await task
    const local = { ...q.products.value[0] }
    for (const timestamp of [base.quote_verified_at, undefined, local.quote_verified_at]) {
      q.catalog.value = [{ ...base, quote_verified_at: timestamp, updated_at: '2099-01-01T00:00:00Z' }]
      expect(q.products.value[0].payable_price).toBe(500)
      expect(q.products.value[0].quote_verified_at).toBe(local.quote_verified_at)
    }
    q.catalog.value = [{ ...base, payable_price: 6, unit_price: 3, quote_verified_at: '2026-09-13T00:00:02Z' }]
    expect(q.products.value[0].payable_price).toBe(6)
    await vi.advanceTimersByTimeAsync(1000)
    expect(q.status(q.products.value[0])).toBe('verified')
  })

  it('does not start another automatic HTTP request when a slow response reports pressure', async () => {
    const responders: Array<(response: Response) => void> = []
    const fetchMock = vi.fn(() => new Promise<Response>(resolve => responders.push(resolve)))
    vi.stubGlobal('fetch', fetchMock)
    const q = setup(['a', 'b', 'c'].map(product))
    q.startBatch(q.products.value)
    await vi.advanceTimersByTimeAsync(1600)
    responders[0](json({}, 429, { 'Retry-After': new Date(Date.now() + 90_000).toUTCString() }))
    await vi.advanceTimersByTimeAsync(1)
    responders[1](json(details()))
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(q.pausedUntil.value).toBeGreaterThan(Date.now() + 85_000)
    expect(q.products.value[1].quote_verified_at).toBe(base.quote_verified_at)
  })

  it.each(['removed', 'shop', 'key'])('cleans overlays and in-flight tasks when identity is %s', async change => {
    let respond!: (value: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { respond = resolve })))
    const q = setup()
    const task = q.request(base, 'click')
    q.catalog.value = change === 'removed' ? [] : [{ ...base, ...(change === 'shop' ? { shop_id: 'different' } : { url: 'https://wzyp.cn/item/different' }) }]
    expect((await task).kind).toBe('cancelled')
    respond(json(details()))
    await vi.advanceTimersByTimeAsync(3000)
    expect(q.products.value.every(item => item.payable_price === 4)).toBe(true)
  })

  it('cancels only automatic tasks and clears every timer on disposal', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
    const q = setup([product('a'), product('b')])
    const automatic = q.request(product('a'))
    const manual = q.request(product('b'), 'click')
    q.cancelAutomatic()
    expect((await automatic).kind).toBe('cancelled')
    await vi.advanceTimersByTimeAsync(8000)
    expect((await manual).kind).toBe('failed')
    scope.stop()
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([null, '', ' ', true, false, -1, 'Infinity', [], {}])('rejects malformed totals (%j) without stamping a success', async total => {
    const fetchMock = successfulFetch().mockImplementation(async url => url.endsWith('getGoodsPrice')
      ? json({ code: 1, data: { total_amount: total } })
      : url.endsWith('goodsInfo') ? json(details()) : json({ code: 1, data: [{ id: 7 }] }))
    vi.stubGlobal('fetch', fetchMock)
    const q = setup()
    const task = q.request(base)
    await vi.advanceTimersByTimeAsync(1400)
    expect((await task).kind).toBe('failed')
    expect(q.products.value[0]).toEqual(base)
    expect(q.status(q.products.value[0])).toBe('failed')
  })

  it.each(['article', 'resource', 'equity'])('accepts zero-price inventoryless %s goods', async goodsType => {
    const handler = successfulFetch()
    vi.stubGlobal('fetch', vi.fn((url: string) => url.endsWith('goodsInfo') ? Promise.resolve(json(details({ goods_type: goodsType, extend: {} })))
      : url.endsWith('getGoodsPrice') ? Promise.resolve(json({ code: 1, data: { total_amount: 0 } })) : handler(url)))
    const q = setup()
    const task = q.request(base)
    await vi.advanceTimersByTimeAsync(1400)
    expect((await task).kind).toBe('success')
    expect(q.products.value[0]).toMatchObject({ payable_price: 0, unit_price: 0, stock: 1, minimum_quantity: 1, goods_type: goodsType })
  })

  it.each([new Response('<html>challenge</html>', { status: 200 }), new Response('{', { headers: { 'Content-Type': 'application/json' } })])('retains a snapshot after a non-JSON or malformed JSON response', async response => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
    const q = setup()
    const task = q.request(base, 'click')
    await vi.advanceTimersByTimeAsync(1)
    expect((await task).kind).toBe('failed')
    expect(q.products.value[0]).toEqual(base)
  })

  it('removes explicitly unavailable products and prevents old snapshots from resurrecting them', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(details({ extend: { stock_count: 2, limit_count: 3 } }))))
    const q = setup()
    const task = q.request(base)
    await vi.advanceTimersByTimeAsync(1)
    expect((await task).kind).toBe('unavailable')
    q.catalog.value = [{ ...base }]
    expect(q.products.value).toEqual([])
  })
})
