import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RecommendationRollouts } from './RecommendationRollouts'
import { setLocale } from '../../i18n'

const rollout = {
  id: 'rollout-safe',
  recommendation_id: 'recommendation-safe',
  recommendation_fingerprint: 'safe-fingerprint',
  plan_id: 'plan-safe',
  fact_version: 18,
  evidence_ids: ['evidence-safe'],
  account_count: 2,
  baseline_fingerprint: 'baseline-safe',
  baseline_verified: true,
  scheduling_generation: 22,
  status: 'observing',
  stage: 10,
  pending_stage: 0,
  observe_until: '2026-07-25T00:00:00Z',
  recommendation_expires_at: '2026-07-27T00:00:00Z',
  rollback_reasons: [],
  gate: { status: 'unknown', reason_codes: [] },
  latest_operation: {
    id: 'operation-safe',
    action: 'apply_stage',
    target_stage: 10,
    status: 'succeeded',
    attempts: 1,
    created_at: '2026-07-26T00:00:00Z',
    updated_at: '2026-07-26T00:01:00Z',
  },
  last_after_verified: true,
  rollback_verified: false,
  started_at: '2026-07-26T00:00:00Z',
  updated_at: '2026-07-26T00:01:00Z',
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

function renderRollouts() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <RecommendationRollouts userId={7} />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('RecommendationRollouts browser-safe projection', () => {
  it('renders the staged execution view without rendering injected private fields', async () => {
    const unsafeServerRecord = {
      ...rollout,
      user_id: 7,
      account_id: 'private-account-id',
      baseline_weights: [{ account_id: 'private-baseline-account', weight: 90 }],
      url: 'https://private-upstream.example',
      token: 'top-secret-token',
      raw_response: 'private-raw-response',
    }
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input)
        if (url.includes('/recommendations?')) return json([])
        if (url.includes('/rollouts/rollout-safe')) return json(unsafeServerRecord)
        if (url.includes('/rollouts?')) return json([unsafeServerRecord])
        return json([])
      }),
    )

    renderRollouts()

    expect(await screen.findByText('plan-safe')).toBeTruthy()
    expect(screen.getByText('当前 10% · 待写入 无')).toBeTruthy()
    expect(screen.getByText('已验证完整基线')).toBeTruthy()
    expect(screen.getByText('2 个账户')).toBeTruthy()
    expect(screen.getByText('观测证据有效')).toBeTruthy()
    expect(screen.getByText('未知')).toBeTruthy()
    expect((screen.getByRole('button', { name: /推\s*进/ }) as HTMLButtonElement).disabled).toBe(
      false,
    )

    fireEvent.click(screen.getByRole('button', { name: /详\s*情/ }))
    expect(await screen.findByText('safe-fingerprint')).toBeTruthy()
    expect(screen.getByText('baseline-safe')).toBeTruthy()

    const dom = document.body.textContent ?? ''
    for (const secret of [
      'private-account-id',
      'private-baseline-account',
      'private-upstream.example',
      'top-secret-token',
      'private-raw-response',
    ]) {
      expect(dom).not.toContain(secret)
    }
  })

  it('shows malformed or absent numeric values as unknown instead of zero', async () => {
    const unknownRecord = {
      ...rollout,
      id: 'rollout-unknown',
      plan_id: 'plan-unknown',
      stage: null,
      pending_stage: null,
      account_count: null,
      fact_version: null,
      scheduling_generation: null,
      observe_until: undefined,
      gate: { status: 'unknown', reason_codes: [] },
      latest_operation: undefined,
      last_after_verified: false,
    }
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        String(input).includes('/recommendations?') ? json([]) : json([unknownRecord]),
      ),
    )

    renderRollouts()

    expect(await screen.findByText('plan-unknown')).toBeTruthy()
    expect(screen.getByText('当前 未知 · 待写入 未知')).toBeTruthy()
    expect(screen.getByText('未知 个账户')).toBeTruthy()
    expect(screen.queryByText('0 个账户')).toBeNull()
  })

  it('switches rollout business labels and the scroll-region name to English', async () => {
    setLocale('en')
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        String(input).includes('/recommendations?') ? json([]) : json([rollout]),
      ),
    )

    renderRollouts()

    expect(await screen.findByText('plan-safe')).toBeTruthy()
    expect(screen.getByText('Current 10% · pending write None')).toBeTruthy()
    expect(screen.getByText('Complete baseline verified')).toBeTruthy()
    expect(screen.getByText('2 accounts')).toBeTruthy()
    expect(
      screen.getByRole('region', {
        name: 'Real rollout execution details, horizontally scrollable',
      }).tabIndex,
    ).toBe(0)
  })
})
