import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import VersionBadge from '../VersionBadge.vue'

const mocks = vi.hoisted(() => ({
  auth: { isAdmin: true },
  app: {
    versionLoading: false,
    currentVersion: '0.1.153-custom.a886bae16bd1',
    latestVersion: '0.1.155',
    hasUpdate: true,
    releaseInfo: null,
    buildType: 'managed',
    managedUpdateStatus: null as null | Record<string, string>,
    fetchVersion: vi.fn().mockResolvedValue(null),
    clearVersionCache: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => mocks.auth,
  useAppStore: () => mocks.app,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string>) => params?.version ? `${key}:${params.version}` : key,
  }),
}))

vi.mock('@/api/admin/system', () => ({
  performUpdate: vi.fn(),
  restartService: vi.fn(),
  getRollbackVersions: vi.fn(),
  rollback: vi.fn(),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: false, copyToClipboard: vi.fn() }),
}))

function mountBadge() {
  return mount(VersionBadge, {
    global: {
      stubs: {
        Icon: true,
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
}

describe('VersionBadge managed builds', () => {
  beforeEach(() => {
    mocks.app.currentVersion = '0.1.153-custom.a886bae16bd1'
    mocks.app.latestVersion = '0.1.155'
    mocks.app.hasUpdate = true
    mocks.app.managedUpdateStatus = null
    mocks.app.fetchVersion.mockClear()
  })

  it('keeps the sidebar badge compact and labels the fork sync action', async () => {
    const wrapper = mountBadge()

    expect(wrapper.get('button').text()).toContain('v0.1.153')
    expect(wrapper.get('button').text()).not.toContain('custom.a886bae16bd1')

    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('custom a886bae1')
    expect(wrapper.text()).toContain('version.syncToFork')
    expect(wrapper.text()).not.toContain('version.updateNow')
    wrapper.unmount()
  })

  it('restores a persisted host sync failure after a page reload', async () => {
    mocks.app.managedUpdateStatus = {
      status: 'failed',
      target_version: '0.1.155',
      message: 'sync repository not found: /opt/sub2api-integration',
    }
    const wrapper = mountBadge()

    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('sync repository not found: /opt/sub2api-integration')
    expect(wrapper.text()).toContain('version.retry')
    expect(wrapper.text()).not.toContain('version.syncToFork')
    wrapper.unmount()
  })

  it('ignores a stale sync status once the target version is deployed', async () => {
    mocks.app.currentVersion = '0.1.155-custom.123456789abc'
    mocks.app.hasUpdate = false
    mocks.app.managedUpdateStatus = {
      status: 'pushed',
      target_version: '0.1.154',
      message: 'waiting for CI',
    }
    const wrapper = mountBadge()

    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('version.upToDate')
    expect(wrapper.text()).not.toContain('version.updateWaitingForCI')
    wrapper.unmount()
  })
})
