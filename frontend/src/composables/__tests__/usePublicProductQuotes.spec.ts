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
        return { data: { state: calls < 2 ? 'running' : 'succeeded' } }
      }
      return { data: { products: [{ ...base, payable_price: 7, unit_price: 3.5, stock: 6, quote_verified_at: '2026-09-13T00:00:02Z' }] } }
    })
    const q = setup([base, { ...base, id: 'two', name: 'Other' }])
    const task = q.request(base, 'click')
    expect(q.refreshDisabled(base)).toBe(true)
    await vi.advanceTimersByTimeAsync(2_000)
    await expect(task).resolves.toEqual({ kind: 'success', url: base.url })
    expect(post).toHaveBeenCalledWith('/public/account-import/products/refresh-one', { shop_id: 'shop', product_id: 'one' })
    expect(get.mock.calls.filter(([p]) => String(p).endsWith('/status'))).toHaveLength(2)
    expect(q.products.value.find(item => item.id === 'one')?.payable_price).toBe(7)
    expect(q.products.value.find(item => item.id === 'two')?.payable_price).toBe(4)
  })

  it('shares duplicate clicks and keeps the previous quote on failure', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockResolvedValue({ data: { state: 'failed' } })
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

  it('re-reads the catalog when a task is superseded', async () => {
    post.mockResolvedValue({ data: { accepted: true, state: 'queued' } })
    get.mockImplementation(async (path: string) => path.endsWith('/status')
      ? { data: { state: 'superseded' } }
      : { data: { products: [{ ...base, payable_price: 8, quote_verified_at: '2026-09-13T00:00:03Z' }] } })
    const q = setup()
    const task = q.request(base, 'refresh')
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(task).resolves.toMatchObject({ kind: 'failed', reason: 'superseded' })
    expect(get).toHaveBeenCalledWith('/public/account-import/products')
    expect(q.products.value[0].payable_price).toBe(8)
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
