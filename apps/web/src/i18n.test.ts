import { describe, expect, it } from 'vitest'
import { message } from './i18n'

describe('message', () => {
  it('interpolates variables', () => {
    expect(message('en', 'pveVersion', { version: '9.2' })).toBe('PVE 9.2')
  })
})
