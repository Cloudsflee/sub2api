import { effectScope, ref, type EffectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post } }))

import { usePublicProductQuotes } from '../usePublicProductQuotes'

const base: PublicAccountImportProduct = {
  id: 'one', shop_id: 'shop', shop_name: 'Shop', shop_url: 'https://shop.example/shop',
  name: 'Product', url: 'https://wzyp.cn/item/one', goods_type: 'card',
  price: 99, payable_price: 4, unit_price: 2, minimum_quantity: 2, stock: 8,
  updated_at: '2026-01-01T00:00:00Z', quote_verified_at: '2026-01-01T00:00:00Z',
}
const refreshed: PublicAccountImportProduct = {
  ...base, price: 10, market_price: 12, payable_price: 21, unit_price: 7,
  minimum_quantity: 3, stock: 6, goods_type: 'resource', quote_verified_at: '2026-09-13T00:00:00Z',
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
    get.mockReset(); post.mockReset()
  })
  afterEach(() => { scope?.stop(); vi.useRealTimers() })

  it('queues a task, polls queued/running, and merges only the target product', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockImplementation(async (path: string) => {
      if (path.endsWith('/status')) {
        const calls = get.mock.calls.filter(([p]) => String(p).endsWith('/status')).length
        return { data: { state: calls < 2 ? 'running' : 'succeeded', product: calls < 2 ? null : refreshed } }
      }
      throw new Error('Unexpected catalog request')
    })
    const q = setup([base, { ...base, id: 'two', name: 'Other' }])
    const task = q.request(base, 'click')
    expect(q.refreshDisabled(base)).toBe(true)
    await vi.advanceTimersByTimeAsync(2_000)
    await expect(task).resolves.toEqual({ kind: 'success', url: base.url })
    expect(post).toHaveBeenCalledWith('/public/account-import/products/refresh-one', { shop_id: 'shop', product_id: 'one' })
    expect(get.mock.calls.filter(([p]) => String(p).endsWith('/status'))).toHaveLength(2)
    expect(get).toHaveBeenCalledTimes(2)
    expect(q.products.value.find(item => item.id === 'one')).toEqual(refreshed)
    expect(q.products.value.find(item => item.id === 'two')?.payable_price).toBe(4)
    // A catalog request started before refresh can finish after the single quote.
    q.catalog.value = [{ ...base }, { ...base, id: 'two', name: 'Other' }]
    expect(q.products.value[0]).toEqual(refreshed)
    expect(q.products.value[1].payable_price).toBe(4)
    // Preserve a subsequent, newer catalog quote through another delayed response.
    const newer = { ...refreshed, price: 50, payable_price: 30, unit_price: 10, quote_verified_at: '2026-09-13T00:00:02Z' }
    q.catalog.value = [newer]
    q.catalog.value = [{ ...base }]
    expect(q.products.value[0]).toEqual(newer)
  })

  it('shares duplicate clicks and keeps the previous quote on failure', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state: 'failed', product: null } })
    const q = setup()
    const first = q.request(base, 'click')
    expect(q.request(base, 'click')).toBe(first)
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(first).resolves.toMatchObject({ kind: 'failed' })
    expect(q.products.value[0]).toMatchObject({ payable_price: 4, unit_price: 2, stock: 8 })
    expect(q.status(q.products.value[0])).toBe('failed')
    expect(q.refreshCooldown(base)).toBe(14)
  })

  it('removes unavailable products from the current overlay', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state: 'unavailable' } })
    const q = setup()
    const task = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(task).resolves.toEqual({ kind: 'unavailable' })
    expect(q.products.value).toEqual([])
  })

  it.each(['succeeded', 'superseded'])('fetches one catalog only for a legacy %s response', async (state) => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockImplementation(async (path: string) => path.endsWith('/status')
      ? { data: { state } }
      : { data: { products: [refreshed] } })
    const q = setup()
    const task = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(task).resolves.toMatchObject(state === 'succeeded' ? { kind: 'success' } : { kind: 'failed', reason: 'superseded' })
    expect(get).toHaveBeenCalledWith('/public/account-import/products')
    expect(get).toHaveBeenCalledTimes(2)
    expect(q.products.value[0]).toEqual(refreshed)
  })

  it.each(['succeeded', 'superseded'])('reads status once when POST immediately returns %s', async (state) => {
    post.mockResolvedValue({ data: { accepted: false, state } })
    get.mockResolvedValue({ data: { state, product: refreshed } })
    const q = setup()
    await expect(q.request(base, 'refresh')).resolves.toMatchObject(state === 'succeeded'
      ? { kind: 'success', url: base.url }
      : { kind: 'failed', reason: 'superseded' })
    expect(get).toHaveBeenCalledTimes(1)
    expect(get).toHaveBeenCalledWith('/public/account-import/products/refresh-one/status', {
      params: { shop_id: 'shop', product_id: 'one' },
    })
    expect(q.products.value[0]).toEqual(refreshed)
  })

  it.each(['succeeded', 'superseded'])('removes only the target when %s explicitly returns null', async (state) => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state, product: null } })
    const other = { ...base, id: 'two', url: 'https://wzyp.cn/item/two' }
    const q = setup([base, other])
    const task = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(task).resolves.toEqual({ kind: 'unavailable' })
    expect(get).toHaveBeenCalledTimes(1)
    expect(q.products.value).toEqual([other])
    q.catalog.value = [{ ...base }, other]
    expect(q.products.value).toEqual([other])
  })

  it.each(['older', 'equal'])('keeps the displayed quote when status has an %s quote', async (age) => {
    post.mockResolvedValue({ data: { state: 'succeeded' } })
    const incoming = { ...base, quote_verified_at: age === 'equal' ? refreshed.quote_verified_at : base.quote_verified_at }
    get.mockResolvedValue({ data: { state: 'succeeded', product: incoming } })
    const q = setup([refreshed])
    await expect(q.request(refreshed, 'refresh')).resolves.toMatchObject({ kind: 'success' })
    expect(q.products.value[0]).toEqual(refreshed)
    q.catalog.value = [{ ...base }]
    expect(q.products.value[0]).toEqual(refreshed)
    // The same comparison also applies when the newer quote lives in the overlay.
    await vi.advanceTimersByTimeAsync(15_000)
    await expect(q.request(base, 'refresh')).resolves.toMatchObject({ kind: 'success' })
    expect(q.products.value[0]).toEqual(refreshed)
  })

  it.each([
    { id: 'different' },
    { shop_id: 'different' },
    { url: 'https://wzyp.cn/item/different' },
    { url: 'https://example.com/item/one' },
  ])('rejects a mismatched product identity: %j', async (change) => {
    post.mockResolvedValue({ data: { state: 'succeeded' } })
    get.mockResolvedValue({ data: { state: 'succeeded', product: { ...refreshed, ...change } } })
    const q = setup()
    await expect(q.request(base, 'refresh')).resolves.toMatchObject({ kind: 'failed', reason: 'identity-mismatch' })
    expect(q.products.value).toEqual([base])
    expect(get).toHaveBeenCalledTimes(1)
  })

  it.each([
    { quote_verified_at: 'invalid' },
    { quote_verified_at: '2026-09-13T00:02:00Z' },
    { unit_price: 100 },
  ])('keeps the old price when a returned quote is invalid: %j', async (change) => {
    post.mockResolvedValue({ data: { state: 'succeeded' } })
    get.mockResolvedValue({ data: { state: 'succeeded', product: { ...refreshed, ...change } } })
    const q = setup()
    await expect(q.request(base, 'refresh')).resolves.toMatchObject({ kind: 'failed', reason: 'invalid-quote' })
    expect(q.products.value).toEqual([base])
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('ignores a status response that arrives after the task is disposed', async () => {
    post.mockResolvedValue({ data: { state: 'queued' } })
    let resolve!: (response: unknown) => void
    get.mockReturnValue(new Promise(done => { resolve = done }))
    const q = setup()
    const task = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1_000)
    q.dispose()
    resolve({ data: { state: 'succeeded', product: refreshed } })
    await vi.advanceTimersByTimeAsync(0)
    await expect(task).resolves.toEqual({ kind: 'cancelled' })
    expect(q.catalog.value).toEqual([base])
  })

  it('stops polling after the 120 second deadline and cleans timers on dispose', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'running' } })
    get.mockResolvedValue({ data: { state: 'running' } })
    const q = setup()
    const task = q.request(base, 'click')
    await vi.advanceTimersByTimeAsync(120_000)
    await expect(task).resolves.toMatchObject({ kind: 'failed', reason: 'timeout' })
    q.dispose()
    expect(vi.getTimerCount()).toBe(0)
  })
})
