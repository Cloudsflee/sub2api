import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  createGroup,
  updateGroup,
  getModelsListCandidates,
  getUsageSummary,
  getCapacitySummary
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelsListCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn().mockResolvedValue([]),
      create: createGroup,
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn(),
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary
    },
    accounts: {
      list: vi.fn().mockResolvedValue({ items: [], total: 0 }),
      getById: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn()
  })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const publicGroup = {
  id: 42,
  name: 'Public capacity',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  public_status_enabled: true,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  require_oauth_only: false,
  require_privacy_set: false,
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  sort_order: 10,
  created_at: '2026-07-25T00:00:00Z',
  updated_at: '2026-07-25T00:00:00Z'
} as AdminGroup

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>'
})

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] }
  },
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <slot name="cell-name" :value="row.name" :row="row" />
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
})

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const PublicStatusFieldStub = defineComponent({
  props: {
    modelValue: Boolean,
    url: String
  },
  emits: ['update:modelValue'],
  template: `
    <div data-testid="public-status-field" :data-enabled="String(modelValue)" :data-url="url">
      <button type="button" data-testid="public-status-field-toggle" @click="$emit('update:modelValue', !modelValue)">toggle</button>
    </div>
  `
})

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        BaseDialog: BaseDialogStub,
        PublicStatusField: PublicStatusFieldStub,
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: {
          props: ['name'],
          template: '<span :data-icon="name"><slot /></span>'
        },
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        ReasoningEffortPolicyFields: true,
        VueDraggable: { template: '<div><slot /></div>' }
      }
    }
  })
}

describe('GroupsView public account status setting', () => {
  beforeEach(() => {
    localStorage.clear()
    for (const mock of [
      listGroups,
      createGroup,
      updateGroup,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary
    ]) {
      mock.mockReset()
    }
    listGroups.mockResolvedValue({
      items: [publicGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    createGroup.mockResolvedValue(publicGroup)
    updateGroup.mockResolvedValue(publicGroup)
    getModelsListCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('creates a group with the explicit public setting', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
    await wrapper.get('[data-testid="public-status-field-toggle"]').trigger('click')
    await wrapper.get('[data-tour="group-form-name"]').setValue('New public group')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    expect(createGroup.mock.calls[0][0]).toMatchObject({
      name: 'New public group',
      public_status_enabled: true
    })
    wrapper.unmount()
  })

  it('shows the public icon, hydrates edit state, and submits the setting', async () => {
    const wrapper = mountView()
    await flushPromises()

    const indicator = wrapper.get('[data-testid="group-public-status-indicator"]')
    expect(indicator.attributes('data-icon')).toBe('globe')
    expect(indicator.attributes('title')).toBe('admin.groups.publicStatus.indicator')

    await wrapper.get('[data-testid="group-edit"]').trigger('click')
    await flushPromises()

    const field = wrapper.get('[data-testid="public-status-field"]')
    expect(field.attributes('data-enabled')).toBe('true')
    expect(field.attributes('data-url')).toBe(`${window.location.origin}/account-status`)

    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(updateGroup.mock.calls[0][0]).toBe(42)
    expect(updateGroup.mock.calls[0][1]).toMatchObject({ public_status_enabled: true })
    wrapper.unmount()
  })
})
