import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post }
}))

import {
  cancelOpenAI5hWakeTask,
  createOpenAI5hWakeTask,
  getLatestOpenAI5hWakeTask,
  getOpenAI5hWakeTask,
	listOpenAI5hWakeTaskEvents,
  listOpenAI5hWakeTaskItems,
  previewOpenAI5hWake
} from '@/api/admin/accounts'

const task = {
  id: 9,
  status: 'running',
  total_items: 3,
  processed_items: 1
}

describe('admin OpenAI 5h wake API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('uses the preview, create and recovery endpoints', async () => {
    const preview = { eligible_accounts: 4, unique_quota_pools: 3 }
    get.mockResolvedValueOnce({ data: preview })
    post.mockResolvedValueOnce({ data: { task, reused: false } })
    get.mockResolvedValueOnce({ data: { task } })
    get.mockResolvedValueOnce({ data: task })

    await expect(previewOpenAI5hWake()).resolves.toEqual(preview)
    await expect(createOpenAI5hWakeTask()).resolves.toEqual({ task, reused: false })
    await expect(getLatestOpenAI5hWakeTask()).resolves.toEqual(task)
    await expect(getOpenAI5hWakeTask(9)).resolves.toEqual(task)

    expect(get).toHaveBeenNthCalledWith(1, '/admin/accounts/openai-5h-wake/preview')
    expect(post).toHaveBeenNthCalledWith(1, '/admin/accounts/openai-5h-wake/tasks')
    expect(get).toHaveBeenNthCalledWith(2, '/admin/accounts/openai-5h-wake/tasks/latest')
    expect(get).toHaveBeenNthCalledWith(3, '/admin/accounts/openai-5h-wake/tasks/9')
  })

  it('uses paginated item and cancellation endpoints', async () => {
    const page = { items: [], total: 0, page: 2, page_size: 10, pages: 1 }
	const eventPage = { items: [], total: 0, page: 1, page_size: 100, pages: 1 }
    get.mockResolvedValueOnce({ data: page })
	get.mockResolvedValueOnce({ data: eventPage })
    post.mockResolvedValueOnce({ data: { ...task, cancel_requested_at: '2026-07-30T00:00:00Z' } })

    await expect(listOpenAI5hWakeTaskItems(9, 2, 10)).resolves.toEqual(page)
	await expect(listOpenAI5hWakeTaskEvents(9)).resolves.toEqual(eventPage)
    await cancelOpenAI5hWakeTask(9)

    expect(get).toHaveBeenCalledWith('/admin/accounts/openai-5h-wake/tasks/9/items', {
      params: { page: 2, page_size: 10 }
    })
	expect(get).toHaveBeenCalledWith('/admin/accounts/openai-5h-wake/tasks/9/events', {
	  params: { page: 1, page_size: 100 }
	})
    expect(post).toHaveBeenCalledWith('/admin/accounts/openai-5h-wake/tasks/9/cancel')
  })
})
