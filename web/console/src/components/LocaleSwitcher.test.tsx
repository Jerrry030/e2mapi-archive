import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
import { LocaleSwitcher } from './LocaleSwitcher'

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  )
})

afterEach(() => {
  cleanup()
  setLocale('zh')
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('LocaleSwitcher', () => {
  it('uses a localized accessible name on the native keyboard-reachable button', () => {
    const { rerender } = render(<LocaleSwitcher />)
    const chinese = screen.getByRole('button', { name: '语言：中文' })
    expect(chinese.tagName).toBe('BUTTON')
    expect(chinese.tabIndex).toBe(0)

    setLocale('en')
    rerender(<LocaleSwitcher />)

    const english = screen.getByRole('button', { name: 'Language: English' })
    expect(english.tagName).toBe('BUTTON')
    expect(english.tabIndex).toBe(0)
    expect(screen.queryByRole('button', { name: '语言：English' })).toBeNull()
  })
})
