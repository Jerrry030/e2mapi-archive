import { describe, expect, it } from 'vitest'
import { searchForSystemSettingsView, systemSettingsViewFromSearch } from './systemSettingsView'

describe('system settings view', () => {
  it('defaults unknown values to auth', () => {
    expect(systemSettingsViewFromSearch(new URLSearchParams('view=unknown'), true)).toBe('auth')
  })

  it('falls back to registration and security when payments are disabled', () => {
    expect(systemSettingsViewFromSearch(new URLSearchParams('view=payment'), false)).toBe('auth')
  })

  it('creates a deep link without dropping unrelated query parameters', () => {
    const payment = searchForSystemSettingsView(new URLSearchParams('keep=1'), 'payment')
    expect(payment.toString()).toBe('keep=1&view=payment')
    expect(systemSettingsViewFromSearch(payment, true)).toBe('payment')
    expect(searchForSystemSettingsView(payment, 'auth').toString()).toBe('keep=1')
  })
})
