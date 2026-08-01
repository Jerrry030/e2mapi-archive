import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { setLocale } from '../i18n'
import { effectiveEventLevel } from '../eventLevel'
import { ActivityRiskTag, ChannelTag } from './tags'

afterEach(() => {
  cleanup()
  localStorage.clear()
})

describe('ActivityRiskTag', () => {
  it('derives a safe event level for records created before event_level existed', () => {
    expect(effectiveEventLevel(undefined, 'L2', 'accepted')).toBe('L1')
    expect(effectiveEventLevel(undefined, 'L0', 'accepted')).toBe('L0')
    expect(effectiveEventLevel(undefined, 'L3', 'failed')).toBe('L2')
    expect(effectiveEventLevel(undefined, 'L2', 'running')).toBe('L0')
    expect(effectiveEventLevel(undefined, 'L2', 'retrying')).toBe('L1')
    expect(effectiveEventLevel('L3', 'L0', 'accepted')).toBe('L3')
  })
  it('keeps the L0-L3 level visible with a plain-language meaning', () => {
    setLocale('zh')
    render(<ActivityRiskTag level="L2" />)
    expect(screen.getByText('L2 WARNING')).toBeTruthy()

    cleanup()
    setLocale('en')
    render(<ActivityRiskTag level="L0" />)
    expect(screen.getByText('L0 INFO')).toBeTruthy()
  })
})

describe('ChannelTag', () => {
  it('shows familiar channel names instead of internal enum values', () => {
    render(<ChannelTag channel="feishu" />)
    expect(screen.getByText('飞书')).toBeTruthy()

    cleanup()
    render(<ChannelTag channel="webhook" />)
    expect(screen.getByText('Webhook')).toBeTruthy()
  })
})
