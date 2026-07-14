import { describe, expect, it } from 'vitest'
import { versionDisplay } from '../versionDisplay'

describe('versionDisplay', () => {
  it('compacts a managed version and exposes a short build label', () => {
    expect(versionDisplay('0.1.153-custom.a886bae16bd1')).toEqual({
      compact: '0.1.153',
      buildLabel: 'custom a886bae1',
    })
  })

  it('keeps release versions unchanged', () => {
    expect(versionDisplay('v0.1.155')).toEqual({ compact: '0.1.155', buildLabel: '' })
  })

  it('bounds unknown version strings', () => {
    expect(versionDisplay('custom-build-with-a-very-long-name').compact).toBe('custom-build-wi...')
  })
})
