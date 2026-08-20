import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicAccountImportView from '../PublicAccountImportView.vue'

const {
  authState,
  fetchPublicSettings,
  getGroups,
  getProducts,
  getShops,
  submitJSON,
  submitUpstream,
} = vi.hoisted(() => ({
  authState: { isAdmin: false, isAuthenticated: false, user: null as null | { role: string } },
  fetchPublicSettings: vi.fn(),
  getGroups: vi.fn(),
  getProducts: vi.fn(),
  getShops: vi.fn(),
  submitJSON: vi.fn(),
  submitUpstream: vi.fn(),
}))

vi.mock('@/api/publicAccountImport', async () => {
  const actual = await vi.importActual<typeof import('@/api/publicAccountImport')>(
    '@/api/publicAccountImport'
  )
  return {
    ...actual,
    getPublicAccountImportGroups: getGroups,
    getPublicAccountImportProductsWithETag: getProducts,
    getPublicAccountImportShops: getShops,
    submitPublicAccountImport: submitJSON,
    submitPublicAccountImportUpstream: submitUpstream,
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ siteName: 'Sub2API', siteLogo: '', fetchPublicSettings }),
}))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

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

const RouterLinkStub = defineComponent({
  props: { to: { type: [String, Object], required: true } },
  template: '<a><slot /></a>',
})

function mountView(): VueWrapper {
  return mount(PublicAccountImportView, {
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        Icon: true,
        HelpTooltip: true,
        ConfirmDialog: true,
      },
    },
  })
}

async function selectFirstGroup(wrapper: VueWrapper): Promise<void> {
  const checkbox = wrapper.find('input[type="checkbox"]')
  expect(checkbox.exists()).toBe(true)
  await checkbox.setValue(true)
}

function jsonFile(name: string, content: string): File {
  const file = new File([content], name, { type: 'application/json' })
  Object.defineProperty(file, 'text', { configurable: true, value: async () => content })
  return file
}

async function chooseFiles(wrapper: VueWrapper, files: File[]): Promise<void> {
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', { configurable: true, value: files })
  await input.trigger('change')
}

describe('PublicAccountImportView import submission', () => {
  beforeEach(() => {
    authState.isAdmin = false
    authState.isAuthenticated = false
    authState.user = null
    fetchPublicSettings.mockReset()
    getGroups.mockReset().mockResolvedValue([{ id: 5, name: 'K12' }])
    getProducts.mockReset().mockResolvedValue({
      notModified: false,
      etag: null,
      data: {
        products: [], shop_count: 0, pending_shops: 0, queued_shops: 0,
        refreshing_shops: 0, failed_shops: 0, expired_shops: 0,
        refresh_seconds: 900, shop_sync_statuses: [],
      },
    })
    getShops.mockReset().mockResolvedValue([])
    submitJSON.mockReset().mockResolvedValue({
      total: 1, created: 1, updated: 0, skipped: 0, failed: 0,
    })
    submitUpstream.mockReset().mockResolvedValue({
      total: 1, created: 0, updated: 0, skipped: 1, failed: 0,
      items: [{ index: 1, name: 'api.example.com', action: 'skipped' }],
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('submits pasted JSON and merges pasted content with selected files', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectFirstGroup(wrapper)

    const paste = wrapper.get('[data-testid="public-import-paste"]')
    await paste.setValue('  {"source":"paste"}  ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(submitJSON).toHaveBeenNthCalledWith(
      1,
      { contents: ['{"source":"paste"}'], group_ids: [5] },
      expect.any(String)
    )

    await chooseFiles(wrapper, [jsonFile('account.json', '{"source":"file"}')])
    await paste.setValue('{"source":"second-paste"}')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(submitJSON).toHaveBeenNthCalledWith(
      2,
      {
        contents: ['{"source":"file"}', '{"source":"second-paste"}'],
        group_ids: [5],
      },
      expect.any(String)
    )
    wrapper.unmount()
  })

  it('enforces pasted-item and combined UTF-8 byte limits', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectFirstGroup(wrapper)
    const paste = wrapper.get('[data-testid="public-import-paste"]')

    await paste.setValue('x'.repeat(512 * 1024 + 1))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('publicAccountImport.pasteTooLarge')
    expect(submitJSON).not.toHaveBeenCalled()

    const exactLimit = 'x'.repeat(512 * 1024)
    await chooseFiles(wrapper, [0, 1, 2, 3].map((index) => jsonFile(`${index}.json`, exactLimit)))
    await paste.setValue('x')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('publicAccountImport.combinedTooLarge')
    expect(submitJSON).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('requires login for URL + Key mode', async () => {
    // A cached profile without a live token must not unlock the authenticated mode.
    authState.user = { role: 'user' }
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="public-import-mode-upstream"]').trigger('click')

    expect(wrapper.find('[data-testid="public-import-login-required"]').exists()).toBe(true)
    const login = wrapper.findAllComponents(RouterLinkStub).find((link) => (
      JSON.stringify(link.props('to')).includes('account-import') && link.text() === 'publicAccountImport.loginToImport'
    ))
    expect(login?.props('to')).toEqual({ path: '/login', query: { redirect: '/account-import' } })
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#public-account-import-api-key').attributes('disabled')).toBeDefined()
    expect(submitUpstream).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('submits URL + Key for regular users and clears the key after success', async () => {
    authState.isAuthenticated = true
    authState.user = { role: 'user' }
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="public-import-mode-upstream"]').trigger('click')
    await selectFirstGroup(wrapper)
    await wrapper.get('#public-account-import-name').setValue('Primary')
    await wrapper.get('#public-account-import-base-url').setValue('https://api.example.com/v1?tenant=one')
    const keyInput = wrapper.get('#public-account-import-api-key')
    await keyInput.setValue('sk-secret')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(submitUpstream).toHaveBeenCalledWith({
      name: 'Primary',
      base_url: 'https://api.example.com/v1?tenant=one',
      api_key: 'sk-secret',
      group_ids: [5],
    }, expect.any(String))
    expect((keyInput.element as HTMLInputElement).value).toBe('')
    expect(wrapper.text()).toContain('publicAccountImport.skipped')
    wrapper.unmount()
  })

  it('rotates the idempotency key after an upstream credential edit', async () => {
    authState.isAuthenticated = true
    authState.user = { role: 'user' }
    submitUpstream
      .mockRejectedValueOnce(new Error('request interrupted'))
      .mockResolvedValueOnce({ total: 1, created: 1, updated: 0, skipped: 0, failed: 0 })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="public-import-mode-upstream"]').trigger('click')
    await selectFirstGroup(wrapper)
    await wrapper.get('#public-account-import-base-url').setValue('https://api.example.com/v1')
    const keyInput = wrapper.get('#public-account-import-api-key')
    await keyInput.setValue('sk-first')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const firstIdempotencyKey = submitUpstream.mock.calls[0][1]

    await keyInput.setValue('sk-second')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const secondIdempotencyKey = submitUpstream.mock.calls[1][1]

    expect(secondIdempotencyKey).not.toBe(firstIdempotencyKey)
    wrapper.unmount()
  })

  it('keeps the key available when the upstream import reports a failure', async () => {
    authState.isAuthenticated = true
    authState.user = { role: 'user' }
    submitUpstream.mockResolvedValueOnce({
      total: 1, created: 0, updated: 0, skipped: 0, failed: 1,
      items: [{ index: 1, name: 'api.example.com', action: 'failed' }],
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="public-import-mode-upstream"]').trigger('click')
    await selectFirstGroup(wrapper)
    await wrapper.get('#public-account-import-base-url').setValue('https://api.example.com/v1')
    const keyInput = wrapper.get('#public-account-import-api-key')
    await keyInput.setValue('sk-retry')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect((keyInput.element as HTMLInputElement).value).toBe('sk-retry')
    expect(wrapper.text()).toContain('publicAccountImport.failed')
    wrapper.unmount()
  })

  it('rejects upstream URLs with userinfo or fragments before submission', async () => {
    authState.isAuthenticated = true
    authState.user = { role: 'user' }
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="public-import-mode-upstream"]').trigger('click')
    await selectFirstGroup(wrapper)
    await wrapper.get('#public-account-import-base-url').setValue('https://user:pass@api.example.com/v1#part')
    await wrapper.get('#public-account-import-api-key').setValue('sk-secret')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('publicAccountImport.baseURLInvalid')
    expect(submitUpstream).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('links regular users to their dashboard and exposes account status', async () => {
    authState.isAuthenticated = true
    authState.user = { role: 'user' }
    const wrapper = mountView()
    await flushPromises()
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links[0].props('to')).toBe('/dashboard')
    expect(links[0].text()).toBe('publicAccountImport.backToDashboard')
    expect(links.some((link) => link.props('to') === '/account-status')).toBe(true)
    wrapper.unmount()
  })

  it('links administrators back to the admin dashboard', async () => {
    authState.isAdmin = true
    authState.isAuthenticated = true
    authState.user = { role: 'admin' }
    const wrapper = mountView()
    await flushPromises()
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links[0].props('to')).toBe('/admin/dashboard')
    expect(links[0].text()).toBe('publicAccountImport.backToAdmin')
    wrapper.unmount()
  })
})
