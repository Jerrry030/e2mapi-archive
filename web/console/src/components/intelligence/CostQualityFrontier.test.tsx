import { cleanup, render, screen } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { IntelligenceFrontierPoint } from '../../api/upstreamIntelligence'
import { setLocale } from '../../i18n'
import { CostQualityFrontier } from './CostQualityFrontier'

afterEach(() => {
  cleanup()
  setLocale('zh')
})

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

function point(overrides: Partial<IntelligenceFrontierPoint> = {}): IntelligenceFrontierPoint {
  return {
    rate: {
      observation_id: 'offer-1',
      source: {
        id: 'source-1',
        mode: 'external',
        provider: 'sub2api',
        display_name: '上游 A',
        status: 'active',
        capabilities: { balance: true, groups: true, rates: true, prices: true },
        freshness: 'current',
        last_run_at: null,
        last_success_at: null,
        next_poll_at: null,
      },
      group_key: 'default',
      model_key: 'gpt-test',
      price_dimension: 'input',
      settlement_currency: 'USD',
      per_tokens: 1_000_000,
      group_multiplier: '1',
      recharge_yield: '1',
      published_unit_price: '2',
      effective_multiplier: '1',
      effective_unit_cost: '2',
      evidence: {
        accuracy: 'exact',
        coverage: 'complete',
        freshness: 'current',
        confidence: null,
        observed_at: '2026-07-24T00:00:00Z',
        received_at: '2026-07-24T00:00:01Z',
        fresh_until: '2026-07-24T00:15:00Z',
        missing_fields: [],
      },
      comparable: true,
    },
    link_state: 'linked',
    channel_id: 'channel-1',
    quality_score: '91.5',
    quality_evidence: {
      snapshot_id: 'quality-1',
      window: '5m',
      quality_sample_count: 42,
      minimum_sample_count: 20,
      success_rate: '0.99',
      ttft_p95_ms: '320',
      duration_p95_ms: '900',
      health_state: 'healthy',
      observed_at: '2026-07-24T00:05:00Z',
      fresh_until: '2026-07-24T00:10:00Z',
      freshness: 'current',
    },
    status: 'eligible',
    blocked_reasons: [],
    on_frontier: true,
    ...overrides,
  }
}

describe('CostQualityFrontier', () => {
  it('shows evidence-backed Pareto status and quality sample proof', () => {
    render(<CostQualityFrontier points={[point()]} />)
    expect(screen.getByRole('img', { name: '成本—质量 Pareto 图' })).toBeTruthy()
    expect(screen.getByRole('region', { name: '成本—质量图表，可横向滚动' }).tabIndex).toBe(0)
    expect(screen.getByRole('list', { name: '图例' })).toBeTruthy()
    expect(document.querySelectorAll('[data-chart-status="frontier"]')).toHaveLength(1)
    expect(screen.getByRole('table', { name: '成本—质量候选明细' })).toBeTruthy()
    expect(screen.getByRole('region', { name: '成本—质量候选明细，可横向滚动' }).tabIndex).toBe(0)
    expect(screen.getByRole('columnheader', { name: '有效成本' })).toBeTruthy()
    expect(screen.getByRole('rowheader', { name: /上游 A/ })).toBeTruthy()
    expect(screen.getByText('2 USD / 1000000')).toBeTruthy()
    expect(screen.getAllByText('前沿候选')).toHaveLength(2)
    expect(screen.getByText(/质量证据 42\/20 样本/)).toBeTruthy()
    expect(screen.getByText('已连接')).toBeTruthy()
  })

  it('keeps unknown quality blocked and explains the reason in plain language', () => {
    render(
      <CostQualityFrontier
        points={[
          point({
            link_state: 'unlinked',
            channel_id: undefined,
            quality_score: null,
            quality_evidence: null,
            status: 'blocked',
            blocked_reasons: ['unlinked_quality'],
            on_frontier: false,
          }),
        ]}
      />,
    )
    expect(screen.getByText('成本—质量前沿暂未计算')).toBeTruthy()
    expect(screen.getByText('当前可比组没有可绘制的数值候选')).toBeTruthy()
    expect(screen.queryByRole('img', { name: '成本—质量 Pareto 图' })).toBeNull()
    expect(screen.getByText('尚未连接质量渠道')).toBeTruthy()
    expect(screen.getByText('质量证据 未知')).toBeTruthy()
  })

  it('keeps a non-frontier comparable row visible in the equivalent table', () => {
    render(<CostQualityFrontier points={[point({ on_frontier: false })]} />)
    expect(document.querySelectorAll('[data-chart-status="comparable"]')).toHaveLength(1)
    expect(screen.getByText('非前沿')).toBeTruthy()
    expect(screen.getByText('无')).toBeTruthy()
  })

  it('switches the complete accessible table projection to English', () => {
    setLocale('en')
    render(<CostQualityFrontier points={[point()]} />)

    expect(screen.getByRole('img', { name: 'Cost–quality Pareto chart' })).toBeTruthy()
    expect(screen.getByRole('list', { name: 'Legend' })).toBeTruthy()
    expect(screen.getByText('Blocked, excluded from Pareto')).toBeTruthy()
    expect(screen.getByRole('table', { name: 'Cost–quality candidate details' })).toBeTruthy()
    expect(
      screen.getByRole('region', {
        name: 'Cost–quality candidate details, horizontally scrollable',
      }).tabIndex,
    ).toBe(0)
    expect(screen.getByRole('columnheader', { name: 'Effective cost' })).toBeTruthy()
    expect(screen.getAllByText('Frontier candidate')).toHaveLength(2)
    expect(screen.queryByText('前沿候选')).toBeNull()
    setLocale('zh')
  })

  it('never promotes a blocked numeric point even when its input frontier flag is true', () => {
    render(
      <CostQualityFrontier
        points={[
          point({
            status: 'blocked',
            on_frontier: true,
            blocked_reasons: ['quality_insufficient'],
          }),
        ]}
      />,
    )

    expect(document.querySelectorAll('[data-chart-status="blocked"]')).toHaveLength(1)
    expect(document.querySelectorAll('[data-chart-status="frontier"]')).toHaveLength(0)
    expect(screen.getByText('成本—质量前沿暂未计算')).toBeTruthy()
    expect(screen.getByText('质量样本不足')).toBeTruthy()
  })

  it('separates incomparable cohorts instead of plotting currencies or models together', () => {
    render(
      <CostQualityFrontier
        points={[
          point(),
          point({
            rate: {
              ...point().rate,
              observation_id: 'offer-2',
              model_key: 'other-model',
              settlement_currency: 'EUR',
            },
          }),
        ]}
      />,
    )

    expect(screen.getByRole('combobox', { name: '选择成本—质量可比组' })).toBeTruthy()
    expect(document.querySelectorAll('[data-chart-status="frontier"]')).toHaveLength(1)
  })

  it('renders upstream-controlled labels as inert text in both SVG and the equivalent table', () => {
    const sourcePayload = '<img src=x onerror="window.__e2m_xss=1">'
    const modelPayload = '<script>window.__e2m_xss=2</script>'
    const groupPayload = '<svg onload="window.__e2m_xss=3">'
    const malicious = point({
      rate: {
        ...point().rate,
        source: { ...point().rate.source, display_name: sourcePayload },
        model_key: modelPayload,
        group_key: groupPayload,
      },
    })

    render(<CostQualityFrontier points={[malicious]} />)

    const equivalentTable = screen.getByRole('table', { name: '成本—质量候选明细' })
    expect(equivalentTable.textContent).toContain(sourcePayload)
    expect(equivalentTable.textContent).toContain(modelPayload)
    expect(equivalentTable.textContent).toContain(groupPayload)
    expect(equivalentTable.querySelector('img')).toBeNull()
    expect(equivalentTable.querySelector('script')).toBeNull()
    expect(equivalentTable.querySelector('[onerror], [onload]')).toBeNull()
    expect(document.querySelector('.intelligence-frontier-chart script')).toBeNull()
    expect(document.querySelector('.intelligence-frontier-chart [onerror]')).toBeNull()
  })
})
