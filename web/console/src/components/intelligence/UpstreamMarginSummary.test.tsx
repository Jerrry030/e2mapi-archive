import { cleanup, render, screen } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { UpstreamMarginBrowserView } from '../../api/upstreamMargin'
import { UpstreamMarginSummary } from './UpstreamMarginSummary'
import { setLocale } from '../../i18n'

afterEach(cleanup)

beforeAll(() => {
  setLocale('zh')
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
})

afterAll(() => {
  Reflect.deleteProperty(window, 'matchMedia')
  setLocale('zh')
})

function view(overrides: Partial<UpstreamMarginBrowserView> = {}): UpstreamMarginBrowserView {
  return {
    window: '24h',
    windowStart: '2026-07-25T12:00:00Z',
    windowEnd: '2026-07-26T12:00:00Z',
    generatedAt: '2026-07-26T12:00:00Z',
    costs: [
      {
        bucket: 'exact',
        factCount: 8,
        amounts: [
          { currency: 'USD', amount: '8' },
          { currency: 'CNY', amount: '56' },
        ],
        reasons: {},
      },
      { bucket: 'estimated', factCount: 1, amounts: [], reasons: {} },
      { bucket: 'unknown', factCount: 1, amounts: [], reasons: { price_unavailable: 1 } },
      { bucket: 'unattributed', factCount: 0, amounts: [], reasons: {} },
      { bucket: 'expired', factCount: 0, amounts: [], reasons: {} },
    ],
    exactFactCount: 7,
    derivedFactCount: 1,
    totalCostFactCount: 10,
    attributableCostFactCount: 9,
    uncoveredCostFactCount: 1,
    attributableCoverage: '0.9',
    minimumAttributableCoverage: '0.9',
    coverageGatePassed: true,
    blockedReasons: ['cross_currency_without_fx'],
    ...overrides,
  }
}

describe('UpstreamMarginSummary', () => {
  it('renders all five purchase-cost columns and explains exact composition', () => {
    render(<UpstreamMarginSummary view={view()} />)

    for (const label of ['精确成本', '估算成本', '未知成本', '未归因成本', '过期成本']) {
      expect(screen.getByText(label)).toBeTruthy()
    }
    expect(screen.getByText('原始精确 7 · 确定性派生 1')).toBeTruthy()
    expect(screen.getByText('8 USD')).toBeTruthy()
    expect(screen.getByText('56 CNY')).toBeTruthy()
  })

  it('does not claim margin when revenue is unavailable and shows the coverage gate', () => {
    render(
      <UpstreamMarginSummary
        view={view({
          attributableCoverage: '0.8',
          coverageGatePassed: false,
          blockedReasons: [],
        })}
      />,
    )

    expect(screen.getByText('采购成本可见，但毛利声明被阻断')).toBeTruthy()
    expect(screen.getByText(/可归因覆盖率低于门槛/)).toBeTruthy()
    expect(screen.getByText(/缺少收入事实，不能计算毛利/)).toBeTruthy()
    expect(screen.getByText('可归因成本覆盖率 80%（门槛 90%）')).toBeTruthy()
    expect(screen.queryByText(/毛利率/)).toBeNull()
    expect(screen.queryByText(/毛利额/)).toBeNull()
  })

  it('keeps multiple currencies separate and states that they are not aggregated', () => {
    render(<UpstreamMarginSummary view={view()} />)

    expect(screen.getByText('币种分别展示')).toBeTruthy()
    expect(screen.getByText('未提供版本化汇率证据，不会把不同币种采购成本相加。')).toBeTruthy()
    expect(screen.queryByText('64')).toBeNull()
  })

  it('switches the purchase-cost guardrail copy to English', () => {
    setLocale('en')
    render(<UpstreamMarginSummary view={view()} />)

    expect(screen.getByText('Purchase cost is visible, but margin claims are blocked')).toBeTruthy()
    expect(screen.getByText('Exact cost')).toBeTruthy()
    expect(screen.getByText('Currencies shown separately')).toBeTruthy()
    expect(screen.queryByText('精确成本')).toBeNull()
    setLocale('zh')
  })
})
