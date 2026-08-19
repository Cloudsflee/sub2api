import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  createGroup,
  updateGroup,
  getModelsListCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelsListCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn().mockResolvedValue([]),
      create: createGroup,
      update: updateGroup,
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
    },
    accounts: {
      list: vi.fn().mockResolvedValue({ items: [], total: 0 }),
      getById: vi.fn(),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}))

const messages: Record<string, string> = {
  'admin.groups.openAI5hAutoWake.enabled': 'Enabled',
  'admin.groups.openAI5hAutoWake.disabled': 'Disabled',
  'admin.groups.openAI5hAutoWake.paused': 'Paused',
  'admin.groups.openAI5hAutoWake.neverChecked': 'Not checked yet',
  'admin.groups.openAI5hAutoWake.notAvailable': 'Not available',
  'admin.groups.openAI5hAutoWake.reasons.task_created': 'Task created',
  'admin.groups.openAI5hAutoWake.statuses.running': 'Running',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => messages[key] ?? key }),
  }
})

const openAIGroup = {
  id: 42,
  name: 'OpenAI primary',
  description: null,
  platform: 'openai',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  public_status_enabled: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  long_context_pricing_enabled: true,
  model_pricing: [],
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
  allow_live: false,
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
  openai_5h_auto_wake_enabled: true,
  openai_5h_auto_wake_last_checked_at: '2026-08-19T02:30:00Z',
  openai_5h_auto_wake_last_candidate_pool_count: 2,
  openai_5h_auto_wake_last_reason: 'task_created',
  openai_5h_auto_wake_last_task_id: 88,
  openai_5h_auto_wake_last_task_status: 'running',
  created_at: '2026-08-19T00:00:00Z',
  updated_at: '2026-08-19T02:30:00Z',
} as AdminGroup

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>',
})

const TablePageLayoutStub = defineComponent({
  template:
    '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template:
    '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
})

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'options', 'disabled'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue"
      :disabled="disabled"
      @change="$emit('update:modelValue', $event.target.value); $emit('change')"
    >
      <option v-for="option in options" :key="String(option.value)" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `,
})

const ReasoningEffortPolicyFieldsStub = defineComponent({
  setup(_, { expose }) {
    expose({ validate: () => true, resetValidation: () => undefined })
    return {}
  },
  template: '<div />',
})

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        ReasoningEffortPolicyFields: ReasoningEffortPolicyFieldsStub,
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        PublicStatusField: true,
        PlatformIcon: true,
        Icon: true,
        PricingEntryCard: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: { template: '<div><slot /></div>' },
      },
    },
  })
}

describe('GroupsView OpenAI 5h auto wake', () => {
  beforeEach(() => {
    localStorage.clear()
    for (const mock of [
      listGroups,
      createGroup,
      updateGroup,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
    ]) {
      mock.mockReset()
    }
    listGroups.mockResolvedValue({
      items: [openAIGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    createGroup.mockResolvedValue(openAIGroup)
    updateGroup.mockResolvedValue(openAIGroup)
    getModelsListCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false })
  })

  it('shows the create toggle only for OpenAI and submits the enabled value', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
    expect(
      wrapper.find('[data-testid="create-openai-5h-auto-wake-section"]').exists(),
    ).toBe(false)

    await wrapper.get('[data-tour="group-form-platform"]').setValue('openai')
    await flushPromises()
    const toggle = wrapper.get('[data-testid="create-openai-5h-auto-wake-toggle"]')
    expect(toggle.attributes('aria-pressed')).toBe('false')

    await toggle.trigger('click')
    await wrapper.get('[data-tour="group-form-name"]').setValue('Auto wake')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    expect(createGroup.mock.calls[0][0]).toMatchObject({
      platform: 'openai',
      openai_5h_auto_wake_enabled: true,
    })
    wrapper.unmount()
  })

  it('clears the create setting when the platform changes away from OpenAI', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
    const platform = wrapper.get('[data-tour="group-form-platform"]')
    await platform.setValue('openai')
    await wrapper.get('[data-testid="create-openai-5h-auto-wake-toggle"]').trigger('click')
    await platform.setValue('anthropic')
    await flushPromises()

    expect(
      wrapper.find('[data-testid="create-openai-5h-auto-wake-section"]').exists(),
    ).toBe(false)
    await wrapper.get('[data-tour="group-form-name"]').setValue('Anthropic')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup.mock.calls[0][0]).toMatchObject({
      platform: 'anthropic',
      openai_5h_auto_wake_enabled: false,
    })
    wrapper.unmount()
  })

  it('hydrates edit state, renders check history, and submits the setting', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-edit"]').trigger('click')
    await flushPromises()

    expect(
      wrapper.get('[data-testid="edit-openai-5h-auto-wake-toggle"]').attributes(
        'aria-pressed',
      ),
    ).toBe('true')
    expect(wrapper.get('[data-testid="openai-5h-auto-wake-candidate-pools"]').text()).toBe(
      '2',
    )
    expect(wrapper.get('[data-testid="openai-5h-auto-wake-last-task"]').text()).toContain(
      '#88 · Running',
    )
    expect(wrapper.get('[data-testid="openai-5h-auto-wake-reason"]').text()).toBe(
      'Task created',
    )
    expect(wrapper.get('[data-testid="openai-5h-auto-wake-last-checked"]').text()).not.toBe(
      'Not checked yet',
    )

    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(updateGroup.mock.calls[0][0]).toBe(42)
    expect(updateGroup.mock.calls[0][1]).toMatchObject({
      openai_5h_auto_wake_enabled: true,
    })
    wrapper.unmount()
  })
})
