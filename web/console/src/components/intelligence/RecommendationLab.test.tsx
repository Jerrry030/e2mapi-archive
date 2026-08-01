import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpstreamRecommendation } from '../../api/recommendationLab'
import { RecommendationLab } from './RecommendationLab'
import { setLocale } from '../../i18n'

const recommendation: UpstreamRecommendation = {
  id: 'rec-1',
  status: 'open',
  intelligence_fact_version: 11,
  cost_ledger_fact_version: 12,
  link_fact_version: 13,
  plan_generation: 14,
  from_source_id: 'source-current',
  from_channel_id: 'channel-current',
  from_group_key: 'default',
  to_source_id: 'source-candidate',
  to_channel_id: 'channel-candidate',
  to_group_key: 'premium',
  model_key: 'model-safe',
  price_dimension: 'input',
  settlement_currency: 'USD',
  per_tokens: 1_000_000,
  affected_plan_ids: ['plan-1'],
  evidence_ids: ['evidence-1'],
  constraints: [
    { kind: 'quality', status: 'passed', evidence_ids: ['evidence-1'] },
    { kind: 'capacity', status: 'passed', evidence_ids: ['evidence-1'] },
    { kind: 'balance', status: 'passed', evidence_ids: ['evidence-1'] },
  ],
  from_cost: { lower: '2', expected: '2', upper: '2' },
  to_cost: { lower: '1', expected: '1', upper: '1' },
  savings: {
    amount_lower: '1',
    amount_expected: '1',
    amount_upper: '1',
    percent_lower: '0.5',
    percent_expected: '0.5',
    percent_upper: '0.5',
  },
  formula_version: 'formula-v1',
  strategy_version: 'strategy-v1',
  fingerprint: 'a'.repeat(64),
  created_at: '2026-07-26T00:00:00Z',
  expires_at: '2026-07-27T00:00:00Z',
}

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
  vi.unstubAllGlobals()
  localStorage.clear()
  setLocale('zh')
})

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function renderLab() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <RecommendationLab userId={7} />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('RecommendationLab browser projection', () => {
  it('renders only the allowlisted recommendation view even if a response gains private fields', async () => {
    const unsafeServerRecord = {
      ...recommendation,
      user_id: 7,
      affected_downstreams: ['private-instance-id'],
      upstream_url: 'https://private-upstream.example',
      credential: 'top-secret-token',
      raw_response: 'private-raw-response',
    }
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input)
        if (url.includes('/experiments/')) return json([])
        return json([unsafeServerRecord])
      }),
    )

    renderLab()

    expect(await screen.findByText('model-safe')).toBeTruthy()
    expect(screen.getByText('source-current / default')).toBeTruthy()
    expect(screen.getByText('→ source-candidate / premium')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Shadow' }) as HTMLButtonElement).disabled).toBe(
      false,
    )
    expect((screen.getByRole('button', { name: 'Dry-run' }) as HTMLButtonElement).disabled).toBe(
      true,
    )

    const dom = document.body.textContent ?? ''
    expect(dom).not.toContain('private-instance-id')
    expect(dom).not.toContain('private-upstream.example')
    expect(dom).not.toContain('top-secret-token')
    expect(dom).not.toContain('private-raw-response')
  })

  it('switches user-visible business copy and the table region to English', async () => {
    setLocale('en')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json([recommendation])),
    )

    renderLab()

    expect(await screen.findByText('Awaiting Shadow')).toBeTruthy()
    expect(screen.getByText('Quality: Passed')).toBeTruthy()
    expect(screen.getByText('Input')).toBeTruthy()
    expect(
      screen.getByRole('region', {
        name: 'Strategy recommendation details, horizontally scrollable',
      }).tabIndex,
    ).toBe(0)
    expect(screen.queryByText('待 Shadow')).toBeNull()
  })
})
