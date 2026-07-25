import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PublicStatusField from '../PublicStatusField.vue'

const { copyToClipboard } = vi.hoisted(() => ({
  copyToClipboard: vi.fn()
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

function mountField(enabled: boolean) {
  return mount(PublicStatusField, {
    props: {
      modelValue: enabled,
      url: 'https://status.example.test/account-status'
    },
    global: {
      stubs: {
        Icon: {
          props: ['name'],
          template: '<span :data-icon="name"></span>'
        }
      }
    }
  })
}

describe('PublicStatusField', () => {
  beforeEach(() => {
    copyToClipboard.mockReset()
    copyToClipboard.mockResolvedValue(true)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('emits an explicit setting and hides link actions while private', async () => {
    const wrapper = mountField(false)

    expect(wrapper.find('[data-testid="public-status-copy"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="public-status-toggle"]').attributes('aria-checked')).toBe('false')

    await wrapper.get('[data-testid="public-status-toggle"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])
  })

  it('copies and opens the fixed public status URL', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const wrapper = mountField(true)

    await wrapper.get('[data-testid="public-status-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(
      'https://status.example.test/account-status',
      'admin.groups.publicStatus.copied'
    )

    await wrapper.get('[data-testid="public-status-open"]').trigger('click')
    expect(open).toHaveBeenCalledWith(
      'https://status.example.test/account-status',
      '_blank',
      'noopener,noreferrer'
    )
  })
})
