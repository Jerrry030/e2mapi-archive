import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
import { RelativeTime } from './common'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  localStorage.clear()
})

describe('RelativeTime', () => {
  it('follows the active locale', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-12T10:00:00Z'))

    setLocale('zh')
    const view = render(<RelativeTime value="2026-07-12T09:00:00Z" />)
    expect(screen.getByText('1 小时前')).toBeTruthy()

    setLocale('en')
    view.rerender(<RelativeTime value="2026-07-12T09:00:00Z" />)
    expect(screen.getByText('an hour ago')).toBeTruthy()
  })
})
