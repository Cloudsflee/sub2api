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
import type {
  OpenAI5hWakePreview,
  OpenAI5hWakeTask,
  OpenAI5hWakeTaskEvent,
  OpenAI5hWakeTaskItem
} from '@/api/admin/accounts'

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

type TestPage<T> = Omit<typeof emptyItems, 'items'> & { items: T[] }

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
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

  it('renders account-attempt and wake-request progress events with known labels', async () => {
    apiMocks.listOpenAI5hWakeTaskEvents.mockResolvedValue({
      ...emptyEvents,
      items: [
        {
          id: 2,
          task_id: 41,
          item_id: 1,
          level: 'info',
          code: 'wake_request_started',
          message: 'account_id=7 candidate=1/2',
          created_at: '2026-07-30T00:00:02Z'
        },
        {
          id: 1,
          task_id: 41,
          item_id: 1,
          level: 'info',
          code: 'account_attempt_started',
          message: 'account_id=7 candidate=1/2 phase=usage_check',
          created_at: '2026-07-30T00:00:01Z'
        }
      ],
      total: 2
    })

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: makeTask() }
    })
    await flushPromises()

    const log = wrapper.get('[data-testid="openai-5h-wake-events"]').text()
    expect(log).toContain('admin.accounts.openAI5hWake.events.account_attempt_started')
    expect(log).toContain('admin.accounts.openAI5hWake.events.wake_request_started')
  })

  it('ignores item and event responses from a task that is no longer displayed', async () => {
    const staleItems = deferred<TestPage<OpenAI5hWakeTaskItem>>()
    const staleEvents = deferred<TestPage<OpenAI5hWakeTaskEvent>>()
    apiMocks.listOpenAI5hWakeTaskItems
      .mockReturnValueOnce(staleItems.promise)
      .mockResolvedValue({
        ...emptyItems,
        items: [{
          id: 52,
          task_id: 42,
          identity_hash: 'fresh-identity',
          member_account_ids: [2],
          attempted_account_ids: [2],
          status: 'woken',
          attempt_count: 1,
          created_at: '2026-07-30T00:02:00Z',
          updated_at: '2026-07-30T00:02:01Z'
        }],
        total: 1
      })
    apiMocks.listOpenAI5hWakeTaskEvents
      .mockReturnValueOnce(staleEvents.promise)
      .mockResolvedValue({
        ...emptyEvents,
        items: [{
          id: 12,
          task_id: 42,
          level: 'info',
          code: 'fresh_task_event',
          message: 'fresh-task-event',
          created_at: '2026-07-30T00:02:01Z'
        }],
        total: 1
      })

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: makeTask({ id: 41 }) }
    })
    await flushPromises()
    await wrapper.setProps({ initialTask: makeTask({ id: 42, processed_items: 2 }) })
    await flushPromises()

    staleItems.resolve({
      ...emptyItems,
      items: [{
        id: 51,
        task_id: 41,
        identity_hash: 'stale-identity',
        member_account_ids: [1],
        attempted_account_ids: [1],
        status: 'failed',
        attempt_count: 1,
        created_at: '2026-07-30T00:01:00Z',
        updated_at: '2026-07-30T00:01:01Z'
      }],
      total: 1
    })
    staleEvents.resolve({
      ...emptyEvents,
      items: [{
        id: 11,
        task_id: 41,
        level: 'error',
        code: 'stale_task_event',
        message: 'stale-task-event',
        created_at: '2026-07-30T00:01:01Z'
      }],
      total: 1
    })
    await flushPromises()

    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenNthCalledWith(2, 42, 1, 10)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenNthCalledWith(2, 42, 1, 100)
    expect(wrapper.text()).toContain('fresh-task-event')
    expect(wrapper.text()).toContain('fresh-identi')
    expect(wrapper.text()).not.toContain('stale-task-event')
    expect(wrapper.text()).not.toContain('stale-identi')
  })

  it('switches to an externally supplied terminal task instead of keeping stale details', async () => {
    const first = makeTask({ id: 41, status: 'failed', processed_items: 5, failed_count: 5 })
    const second = makeTask({ id: 42, status: 'succeeded', processed_items: 5, woken_count: 5 })
    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: first }
    })
    await flushPromises()

    await wrapper.setProps({ initialTask: second })
    await flushPromises()

    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenLastCalledWith(42, 1, 10)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenLastCalledWith(42, 1, 100)
    const updates = wrapper.emitted('task-updated') || []
    expect((updates.at(-1)?.[0] as OpenAI5hWakeTask).id).toBe(42)
    expect(wrapper.emitted('completed')).toBeUndefined()
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
    expect(wrapper.emitted('completed')).toBeUndefined()
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

  it('retries terminal task details without resuming task polling', async () => {
    vi.useFakeTimers()
    const running = makeTask({ processed_items: 4 })
    const completed = makeTask({
      status: 'succeeded',
      processed_items: 5,
      woken_count: 5,
      finished_at: '2026-07-30T00:00:10Z'
    })
    apiMocks.getOpenAI5hWakeTask.mockResolvedValue(completed)
    apiMocks.listOpenAI5hWakeTaskEvents
      .mockResolvedValueOnce(emptyEvents)
      .mockRejectedValueOnce(new Error('temporary terminal log failure'))
      .mockResolvedValue({
        ...emptyEvents,
        items: [{
          id: 12,
          task_id: 41,
          level: 'info',
          code: 'task_finished',
          message: 'terminal-details-restored',
          created_at: '2026-07-30T00:00:10Z'
        }],
        total: 1
      })

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: running }
    })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(apiMocks.getOpenAI5hWakeTask).toHaveBeenCalledOnce()
    expect(wrapper.text()).not.toContain('terminal-details-restored')

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()

    expect(wrapper.get('[data-testid="openai-5h-wake-events"]').text()).toContain('terminal-details-restored')
    expect(apiMocks.getOpenAI5hWakeTask).toHaveBeenCalledOnce()
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(3)

    await vi.advanceTimersByTimeAsync(2000)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(3)
  })

  it('keeps retrying successful terminal reads until the final event is visible', async () => {
    vi.useFakeTimers()
    const terminal = makeTask({ status: 'succeeded', processed_items: 5, woken_count: 5 })
    apiMocks.listOpenAI5hWakeTaskEvents
      .mockResolvedValueOnce(emptyEvents)
      .mockResolvedValueOnce(emptyEvents)
      .mockResolvedValueOnce({
        ...emptyEvents,
        items: [{
          id: 12,
          task_id: 41,
          level: 'info',
          code: 'task_finished',
          message: 'status=succeeded',
          created_at: '2026-07-30T00:00:10Z'
        }],
        total: 1
      })

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: terminal }
    })
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(2)

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-testid="openai-5h-wake-events"]').text()).toContain('task_finished')

    await vi.advanceTimersByTimeAsync(60000)
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(3)
    wrapper.unmount()
  })

  it('stops terminal detail retries after the bounded backoff budget', async () => {
    vi.useFakeTimers()
    const terminal = makeTask({ status: 'failed', processed_items: 5, failed_count: 5 })
    apiMocks.listOpenAI5hWakeTaskItems.mockRejectedValue(new Error('items unavailable'))
    apiMocks.listOpenAI5hWakeTaskEvents.mockRejectedValue(new Error('events unavailable'))

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: terminal }
    })
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenCalledTimes(1)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(1)

    for (const delay of [1000, 1000, 2000, 5000, 10000, 30000]) {
      await vi.advanceTimersByTimeAsync(delay)
      await flushPromises()
    }

    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenCalledTimes(7)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(7)
    await vi.advanceTimersByTimeAsync(60000)
    await flushPromises()
    expect(apiMocks.listOpenAI5hWakeTaskItems).toHaveBeenCalledTimes(7)
    expect(apiMocks.listOpenAI5hWakeTaskEvents).toHaveBeenCalledTimes(7)
    wrapper.unmount()
  })

  it('keeps a reopened task creation loading while an older request settles', async () => {
    const staleCreate = deferred<{ task: OpenAI5hWakeTask; reused: boolean }>()
    const freshCreate = deferred<{ task: OpenAI5hWakeTask; reused: boolean }>()
    apiMocks.createOpenAI5hWakeTask
      .mockReturnValueOnce(staleCreate.promise)
      .mockReturnValueOnce(freshCreate.promise)

    const wrapper = mount(OpenAI5hWakeDialog, { props: { show: true } })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-start"]').trigger('click')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-start"]').trigger('click')

    expect(wrapper.get('[data-testid="openai-5h-wake-start"]').attributes('disabled')).toBeDefined()
    staleCreate.resolve({ task: makeTask({ id: 41 }), reused: false })
    await flushPromises()
    expect(wrapper.get('[data-testid="openai-5h-wake-start"]').attributes('disabled')).toBeDefined()

    freshCreate.resolve({ task: makeTask({ id: 42 }), reused: false })
    await flushPromises()
    const reportedTaskIDs = (wrapper.emitted('task-updated') || []).map(args => (args[0] as OpenAI5hWakeTask).id)
    expect(reportedTaskIDs).toEqual([42])
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

  it('keeps a reopened cancellation loading while an older request settles', async () => {
    const staleCancel = deferred<OpenAI5hWakeTask>()
    const freshCancel = deferred<OpenAI5hWakeTask>()
    apiMocks.cancelOpenAI5hWakeTask
      .mockReturnValueOnce(staleCancel.promise)
      .mockReturnValueOnce(freshCancel.promise)

    const wrapper = mount(OpenAI5hWakeDialog, {
      props: { show: true, initialTask: makeTask({ id: 41 }) }
    })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-cancel"]').trigger('click')
    await wrapper.get('[data-testid="confirm-cancel"]').trigger('click')
    await wrapper.setProps({ show: false, initialTask: makeTask({ id: 42 }) })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="openai-5h-wake-cancel"]').trigger('click')
    await wrapper.get('[data-testid="confirm-cancel"]').trigger('click')

    expect(wrapper.get('[data-testid="openai-5h-wake-cancel"]').attributes('disabled')).toBeDefined()
    staleCancel.resolve(makeTask({ id: 41, cancel_requested_at: '2026-07-30T00:00:02Z' }))
    await flushPromises()
    expect(wrapper.get('[data-testid="openai-5h-wake-cancel"]').attributes('disabled')).toBeDefined()

    freshCancel.resolve(makeTask({ id: 42, cancel_requested_at: '2026-07-30T00:00:03Z' }))
    await flushPromises()
    const reportedTaskIDs = (wrapper.emitted('task-updated') || []).map(args => (args[0] as OpenAI5hWakeTask).id)
    expect(reportedTaskIDs.at(-1)).toBe(42)
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
