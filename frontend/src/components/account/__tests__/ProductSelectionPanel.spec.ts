import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineComponent, reactive, ref } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProductSelectionPanel from '../ProductSelectionPanel.vue'
import messages from '@/i18n/locales/zh/productSelection'
import { emptySelection, selectionFromProduct, selectionFromResearch } from '@/utils/productSelection'
import type { SelectionRecord, SelectionResearch } from '@/api/productSelections'
import type { PublicAccountImportProduct } from '@/api/publicAccountImport'

const mocks = vi.hoisted(() => ({
  auth: {} as { isAuthenticated: boolean; isAdmin: boolean; user: { id: number } | null },
  list: vi.fn(), research: vi.fn(), create: vi.fn(), update: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  locale: ref('zh'),
  t: (key: string, params: Record<string, unknown> = {}) => {
    const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown>)[part], messages)
    return String(value).replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? ''))
  },
}) }))
vi.mock('@/api/productSelections', async () => ({
  ...await vi.importActual<typeof import('@/api/productSelections')>('@/api/productSelections'),
  listProductSelections: mocks.list, getProductSelectionResearch: mocks.research,
  createProductSelection: mocks.create, updateProductSelection: mocks.update,
}))

const research: SelectionResearch = JSON.parse(readFileSync(resolve(process.cwd(), '../backend/internal/service/productselection/research-20261117.json'), 'utf8'))
const dialog = defineComponent({ props: ['show'], template: '<div v-if="show" role="dialog"><slot/><slot name="footer"/></div>' })
const sampleProduct: PublicAccountImportProduct = {
  id: 'p1', shop_id: 's1', shop_name: '来源店', shop_url: 'https://shop.example/',
  name: '待核价商品', url: 'https://shop.example/p1', goods_type: 'card', price: 999,
  payable_price: 999, unit_price: 999, stock: 10, minimum_quantity: 1,
  quote_pending: true, quote_verified_at: '2026-11-16T00:00:00Z', updated_at: '2026-11-16T00:00:00Z',
}
const record = (overrides: Partial<SelectionRecord> = {}): SelectionRecord => ({
  ...emptySelection(), title: '已保存选品', id: 'record1', revision: 1, request_id: 'request1',
  created_at: '2026-11-16T00:00:00Z', updated_at: '2026-11-16T00:00:00Z', ...overrides,
})
let wrapper: VueWrapper
function render(product?: PublicAccountImportProduct) {
  wrapper = mount(ProductSelectionPanel, {
    props: { draftProduct: product },
    global: { stubs: { BaseDialog: dialog, RouterLink: true } },
  })
  return wrapper
}
beforeEach(() => {
  mocks.auth = reactive({ isAuthenticated: true, isAdmin: true, user: { id: 17 } })
  mocks.list.mockReset().mockResolvedValue([])
  mocks.research.mockReset().mockResolvedValue(research)
  mocks.create.mockReset().mockImplementation(async data => ({ record: record(data), created: true }))
  mocks.update.mockReset().mockImplementation(async (id, revision, data) => record({ ...data, id, revision: revision + 1 }))
})
afterEach(() => wrapper?.unmount())

describe('Product selection records', () => {
  it('offers login without loading private records; regular users cannot fetch admin research', async () => {
    mocks.auth.isAuthenticated = false
    render()
    await flushPromises()
    expect(wrapper.text()).toContain('登录后可以保存选品')
    expect(mocks.list).not.toHaveBeenCalled()
    expect(mocks.research).not.toHaveBeenCalled()
    mocks.auth.isAdmin = false
    mocks.auth.isAuthenticated = true
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(mocks.research).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="selection-research-tab"]').exists()).toBe(false)
  })

  it('keeps all dated research groups and samples, and saves a sample with the evaluation framework', async () => {
    render()
    await flushPromises()
    expect(wrapper.text()).toContain('39 个分析组')
    expect(wrapper.text()).toContain('77 条样本')
    await wrapper.get('[data-add-sample]').trigger('click')
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    const saved = mocks.create.mock.calls[0][0]
    expect(saved.price_basis).toBe('snapshot')
    expect(saved.quote_verified_at).toBeTruthy()
    expect(saved.information_gap).toBeTruthy()
    expect(saved.official_comparison).toBeTruthy()
    expect(saved.target_sale_price).toBeNull()
    expect(saved.observed_sale_price).toBeNull()
    expect(saved.id).toBeUndefined()
    expect(wrapper.find('[data-selection-id="record1"]').exists()).toBe(true)
    for (const p of research.profiles) {
      for (const sample of p.examples) {
        const data = selectionFromResearch(p, sample)
        expect(data.official_comparison.length).toBeLessThanOrEqual(4000)
        expect(data.information_gap.length).toBeLessThanOrEqual(2500)
      }
    }
  })

  it('never copies an unverified base price and marks edited costs as estimates', async () => {
    expect(selectionFromProduct(sampleProduct).purchase_price).toBeNull()
    render(sampleProduct)
    await flushPromises()
    expect((wrapper.get('#selection-cost').element as HTMLInputElement).value).toBe('')
    await wrapper.get('#selection-cost').setValue('12.50')
    await wrapper.get('#selection-target').setValue('29')
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    expect(mocks.create.mock.calls[0][0]).toMatchObject({
      purchase_price: 12.5, price_basis: 'estimate', quote_verified_at: '', target_sale_price: 29, observed_sale_price: null,
    })
  })

  it('preserves failed drafts and uses the same idempotency key on retries', async () => {
    mocks.create.mockRejectedValueOnce({ status: 500 })
    render(sampleProduct)
    await flushPromises()
    await wrapper.get('#selection-notes').setValue('我的试售记录')
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('草稿已保留')
    expect((wrapper.get('#selection-notes').element as HTMLTextAreaElement).value).toBe('我的试售记录')
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    expect(mocks.create.mock.calls[0][1]).toBe(mocks.create.mock.calls[1][1])
  })

  it('prevents stale updates from silently replacing another device’s changes', async () => {
    mocks.list.mockResolvedValue([record()])
    mocks.update.mockRejectedValueOnce({ status: 409 })
    render()
    await flushPromises()
    await wrapper.get('[data-selection-id] button').trigger('click')
    await wrapper.get('#selection-notes').setValue('尚未保存的修改')
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('其他页面更新')
    expect((wrapper.get('#selection-notes').element as HTMLTextAreaElement).value).toBe('尚未保存的修改')
    expect(mocks.update.mock.calls[0][1]).toBe(1)
  })

  it('archives records without deleting notes and filters them out by default', async () => {
    mocks.list.mockResolvedValue([record({ notes: '保留此记录' })])
    render()
    await flushPromises()
    const archive = wrapper.findAll('[data-selection-id] button').find(button => button.text() === '归档')!
    await archive.trigger('click')
    await flushPromises()
    expect(mocks.update.mock.calls[0][2]).toMatchObject({ stage: 'archived', notes: '保留此记录' })
    expect(wrapper.find('[data-selection-id]').exists()).toBe(false)
    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(wrapper.find('[data-selection-id]').exists()).toBe(true)
  })

  it('ignores a previous user’s late response after logout', async () => {
    let finish!: (value: SelectionRecord[]) => void
    mocks.list.mockReturnValueOnce(new Promise<SelectionRecord[]>(resolve => { finish = resolve }))
    render()
    mocks.auth.isAuthenticated = false
    mocks.auth.user = null
    await flushPromises()
    finish([record({ title: 'private old response' })])
    await flushPromises()
    expect(wrapper.text()).not.toContain('private old response')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('does not overwrite a newly saved record with an earlier pending read', async () => {
    let finish!: (value: SelectionRecord[]) => void
    mocks.list.mockReturnValueOnce(new Promise<SelectionRecord[]>(resolve => { finish = resolve }))
    render(sampleProduct)
    await wrapper.get('#product-selection-form').trigger('submit')
    await flushPromises()
    finish([])
    await flushPromises()
    expect(wrapper.find('[data-selection-id="record1"]').exists()).toBe(true)
  })
})
