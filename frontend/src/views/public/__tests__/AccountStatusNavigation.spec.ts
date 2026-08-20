import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountStatusView from '../AccountStatusView.vue'

const { authState, fetchPublicSettings, listGroups, listAccounts } = vi.hoisted(() => ({
  authState: { isAdmin: false, isAuthenticated: false, user: null as null | { role: string } },
  fetchPublicSettings: vi.fn(),
  listGroups: vi.fn(),
  listAccounts: vi.fn(),
}))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    siteName: 'Sub2API',
    siteLogo: '',
    publicSettingsLoaded: true,
    fetchPublicSettings,
  }),
}))
vi.mock('@/api/publicAccountStatus', () => ({
  listPublicAccountStatusGroups: listGroups,
  listPublicAccountStatusAccounts: listAccounts,
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const RouterLinkStub = defineComponent({
  props: { to: { type: [String, Object], required: true } },
  template: '<a><slot /></a>',
})

describe('AccountStatusView role navigation', () => {
  beforeEach(() => {
    authState.isAdmin = false
    authState.isAuthenticated = false
    authState.user = null
    fetchPublicSettings.mockReset()
    listGroups.mockReset().mockResolvedValue([])
    listAccounts.mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each([
    {
      name: 'anonymous visitors',
      setup: () => undefined,
      destination: { path: '/login', query: { redirect: '/account-status' } },
      label: 'publicAccountStatus.login',
    },
    {
      name: 'regular users',
      setup: () => {
        authState.isAuthenticated = true
        authState.user = { role: 'user' }
      },
      destination: '/dashboard',
      label: 'publicAccountStatus.backToDashboard',
    },
    {
      name: 'administrators',
      setup: () => {
        authState.isAdmin = true
        authState.isAuthenticated = true
        authState.user = { role: 'admin' }
      },
      destination: '/admin/dashboard',
      label: 'publicAccountStatus.backToAdmin',
    },
  ])('shows dashboard/login and import links for $name', async ({ setup, destination, label }) => {
    setup()
    const wrapper = mount(AccountStatusView, {
      global: {
        stubs: {
          RouterLink: RouterLinkStub,
          Icon: true,
          LocaleSwitcher: true,
          Pagination: true,
          PlatformIcon: true,
          Skeleton: true,
          PublicAccountUsageDetails: true,
        },
      },
    })
    await flushPromises()

    const links = wrapper.findAllComponents(RouterLinkStub)
    const roleLink = links.find((link) => link.text() === label)
    expect(roleLink?.props('to')).toEqual(destination)
    expect(links.some((link) => link.props('to') === '/account-import')).toBe(true)
    wrapper.unmount()
  })
})
