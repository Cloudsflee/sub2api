import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({
  previewOpenAI5hWake: vi.fn(),
  createOpenAI5hWakeTask: vi.fn(),
  getLatestOpenAI5hWakeTask: vi.fn(),
  getOpenAI5hWakeTask: vi.fn(),
  listOpenAI5hWakeTaskItems: vi.fn(),
	listOpenAI5hWakeTaskEvents: vi.fn(),
  cancelOpenAI5hWakeTask: vi.fn()
}))

vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: apiMocks
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/components/common/BaseDialog.vue', () => ({
  default: {
    name: 'BaseDialog',
    props: ['show', 'title', 'width'],
    emits: ['close'],
    template: '<div v-if="show"><slot /><slot name="footer" /></div>'
  }
}))

vi.mock('@/components/common/ConfirmDialog.vue', () => ({
  default: {
    name: 'ConfirmDialog',
    props: ['show'],
    emits: ['confirm', 'cancel'],
    template: '<button v-if="show" data-testid="confirm-cancel" @click="$emit(\'confirm\')">confirm</button>'
  }
}))

vi.mock('@/components/common/Pagination.vue', () => ({
  default: {
    name: 'Pagination',
    template: '<div />'
  }
}))

vi.mock('@/components/icons/Icon.vue', () => ({
  default: {
    name: 'Icon',
    template: '<i />'
  }
}))

import OpenAI5hWakeDialog from '../OpenAI5hWakeDialog.vue'
import type { OpenAI5hWakePreview, OpenAI5hWakeTask } from '@/api/admin/accounts'

const preview: OpenAI5hWakePreview = {
  total_openai_accounts: 12,
  eligible_accounts: 7,
  unique_quota_pools: 5,
  active_windows: 2,
  estimated_requests: 3,
  excluded: {
    api_key: 1,
    non_oauth: 0,
    spark_shadow: 1,
    non_global: 0,
    disabled: 1,
    unschedulable: 1,
    expired: 0,
    rate_limited: 1,
    cooling_down: 0,
    missing_identity: 0
  }
}

const makeTask = (overrides: Partial<OpenAI5hWakeTask> = {}): OpenAI5hWakeTask => ({
  id: 41,
  status: 'running',
  eligible_account_count: 7,
  active_window_count: 2,
  estimated_request_count: 3,
  total_items: 5,
  processed_items: 1,
  running_item_count: 1,
  woken_count: 1,
  skipped_active_count: 0,
  failed_count: 0,
  cancelled_count: 0,
  alignment_span_seconds: 0,
  created_at: '2026-07-30T00:00:00Z',
  updated_at: '2026-07-30T00:00:01Z',
  ...overrides
})

const emptyItems = {
  items: [],
  total: 0,
  page: 1,
  page_size: 10,
  pages: 1
}

const emptyEvents = {
  items: [],
  total: 0,
  page: 1,
  page_size: 100,
  pages: 1
}

beforeEach(() => {
  vi.clearAllMocks()
  apiMocks.getLatestOpenAI5hWakeTask.mockResolvedValue(null)
  apiMocks.previewOpenAI5hWake.mockResolvedValue(preview)
  apiMocks.listOpenAI5hWakeTaskItems.mockResolvedValue(emptyItems)
	apiMocks.listOpenAI5hWakeTaskEvents.mockResolvedValue(emptyEvents)
})

afterEach(() => {
  vi.useRealTimers()
})

describe('OpenAI5hWakeDialog', () => {
  it('loads a database-wide preview before confirmation', async () => {
    const wrapper = mount(OpenAI5hWakeDialog, { props: { show: true } })
    await flushPromises()

    expect(apiMocks.getLatestOpenAI5hWakeTask).toHaveBeenCalledOnce()
    expect(apiMocks.previewOpenAI5hWake).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="openai-5h-wake-preview"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('7')
    expect(wrapper.text()).toContain('5')
    expect(wrapper.text()).toContain('3')
  })

  it('creates a durable task and polls until completion', async () => {
    vi.useFakeTimers()
    const running = makeTask()
    const completed = makeTask({
      status: 'succeeded',
      processed_items: 5,
      woken_count: 3,
      skipped_active_count: 2,
      finished_at: '2026-07-30T00:00:10Z'
    })
    apiMocks.createOpenAI5hWakeTask.mockResolvedValue({ task: running, reused: false })
    apiMocks.getOpenAI5hWakeTask.mockResolvedValue(completed)

    const wrapper = mount(OpenAI5hWakeDialog, { props: { show: true } })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-start"]').trigger('click')
    await flushPromises()

    expect(apiMocks.createOpenAI5hWakeTask).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="openai-5h-wake-task"]').exists()).toBe(true)

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(apiMocks.getOpenAI5hWakeTask).toHaveBeenCalledWith(41)
	expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledWith(41, 1, 100)
    expect(wrapper.emitted('completed')?.[0]?.[0]).toEqual(completed)
  })

  it('keeps the preview visible and reports task creation failures', async () => {
    apiMocks.createOpenAI5hWakeTask.mockRejectedValue(new Error('task creation failed'))

    const wrapper = mount(OpenAI5hWakeDialog, { props: { show: true } })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-start"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="openai-5h-wake-preview"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="openai-5h-wake-start-error"]').exists()).toBe(true)
  })

  it('restores an active task without loading a new preview', async () => {
    const running = makeTask({ processed_items: 3 })
    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: running }
    })
    await flushPromises()

    expect(apiMocks.previewOpenAI5hWake).not.toHaveBeenCalled()
    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenCalledWith(41, 1, 10)
    expect(wrapper.find('[data-testid="openai-5h-wake-task"]').exists()).toBe(true)
  })

  it('restores the latest terminal task logs and can open a new-task preview', async () => {
    const failed = makeTask({ status: 'failed', processed_items: 5, failed_count: 5 })
    apiMocks.getLatestOpenAI5hWakeTask.mockResolvedValue(failed)
    apiMocks.listOpenAI5hWakeTaskEvents.mockResolvedValue({
      ...emptyEvents,
      items: [{
        id: 9,
        task_id: 41,
        level: 'error',
        code: 'task_processing_failed',
        message: 'database unavailable',
        created_at: '2026-07-30T00:01:00Z'
      }],
      total: 1
    })

    const wrapper = mount(OpenAI5hWakeDialog, { props: { show: true } })
    await flushPromises()

    expect(apiMocks.previewOpenAI5hWake).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="openai-5h-wake-events"]').text()).toContain('database unavailable')
    await wrapper.get('[data-testid="openai-5h-wake-new-task"]').trigger('click')
    await flushPromises()

    expect(apiMocks.previewOpenAI5hWake).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="openai-5h-wake-preview"]').exists()).toBe(true)
  })

  it('continues polling when the initial result page fails to load', async () => {
    vi.useFakeTimers()
    const running = makeTask({ processed_items: 2 })
    apiMocks.listOpenAI5hWakeTaskItems
      .mockRejectedValueOnce(new Error('temporary result failure'))
      .mockResolvedValue(emptyItems)
    apiMocks.getOpenAI5hWakeTask.mockResolvedValue(running)

    mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: running }
    })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(apiMocks.getOpenAI5hWakeTask).toHaveBeenCalledWith(41)
    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenCalledTimes(2)
  })

	it('shows persisted execution errors and the real item attempt count', async () => {
	  apiMocks.listOpenAI5hWakeTaskItems.mockResolvedValue({
		...emptyItems,
		items: [{
		  id: 44,
		  task_id: 41,
		  identity_hash: '0123456789abcdef',
		  member_account_ids: [7],
		  attempted_account_ids: [],
		  status: 'running',
		  attempt_count: 58,
		  created_at: '2026-07-30T00:00:00Z',
		  updated_at: '2026-07-30T00:01:00Z'
		}],
		total: 1
	  })
	  apiMocks.listOpenAI5hWakeTaskEvents.mockResolvedValue({
		...emptyEvents,
		items: [{
		  id: 8,
		  task_id: 41,
		  item_id: 44,
		  level: 'error',
		  code: 'item_complete_failed',
		  message: 'pq: violates check constraint attempted_ids_array_check',
		  created_at: '2026-07-30T00:01:00Z'
		}],
		total: 1
	  })

	  const wrapper = mount(OpenAI5hWakeDialog, {
		props: { show: true, initialTask: makeTask() }
	  })
	  await flushPromises()

	  expect(wrapper.get('[data-testid="openai-5h-wake-events"]').text()).toContain('attempted_ids_array_check')
	  expect(wrapper.get('tbody tr').text()).toContain('58')
	})

  it('requests cancellation and keeps the task visible while the backend stops work', async () => {
    const running = makeTask()
    const cancelling = makeTask({ cancel_requested_at: '2026-07-30T00:00:02Z' })
    apiMocks.cancelOpenAI5hWakeTask.mockResolvedValue(cancelling)

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: running }
    })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-cancel"]').trigger('click')
    await wrapper.get('[data-testid="confirm-cancel"]').trigger('click')
    await flushPromises()

    expect(apiMocks.cancelOpenAI5hWakeTask).toHaveBeenCalledWith(41)
    expect(wrapper.text()).toContain('admin.accounts.openAI5hWake.cancelRequested')
  })

  it('stops dialog polling when closed', async () => {
    vi.useFakeTimers()
    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: makeTask() }
    })
    await flushPromises()
    await wrapper.setProps({ show: false })
    await vi.advanceTimersByTimeAsync(3000)

    expect(apiMocks.getOpenAI5hWakeTask).not.toHaveBeenCalled()
  })
})
