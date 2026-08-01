import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../../i18n'
import { EvidenceDrawer } from './EvidenceDrawer'

const { evidenceQuery } = vi.hoisted(() => ({
  evidenceQuery: vi.fn(),
}))

vi.mock('../../api/upstreamIntelligenceHooks', () => ({
  useIntelligenceEvidence: evidenceQuery,
}))

beforeEach(() => {
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
  evidenceQuery.mockReset()
  setLocale('zh')
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('EvidenceDrawer', () => {
  it('localizes evidence kind and collection-run status in English', () => {
    setLocale('en')
    evidenceQuery.mockReturnValue({
      isLoading: false,
      error: null,
      data: {
        id: 'evidence-1',
        kind: 'wallet',
        source: { display_name: 'Source A' },
        fact_version: 7,
        generated_at: '2026-07-26T00:00:00Z',
        run: { id: 'run-1', status: 'partial', fact_count: 3 },
        wallet: null,
        offer: null,
        change: null,
      },
    })

    render(<EvidenceDrawer userId={7} evidenceId="evidence-1" onClose={vi.fn()} />)

    expect(screen.getByText('Wallet')).toBeTruthy()
    expect(screen.getByText('run-1 · Partially succeeded · 3 facts')).toBeTruthy()
    expect(screen.queryByText('wallet')).toBeNull()
    expect(screen.queryByText('partial')).toBeNull()
  })

  it('renders source, model and group evidence values as inert text', () => {
    const sourcePayload = '<img src=x onerror="window.__e2m_xss=1">'
    const modelPayload = '<script>window.__e2m_xss=2</script>'
    const groupPayload = '<svg onload="window.__e2m_xss=3">'
    evidenceQuery.mockReturnValue({
      isLoading: false,
      error: null,
      data: {
        id: 'evidence-xss',
        kind: 'offer',
        source: { display_name: sourcePayload },
        fact_version: 8,
        generated_at: '2026-07-26T00:00:00Z',
        run: null,
        wallet: null,
        change: null,
        offer: {
          model_key: modelPayload,
          group_key: groupPayload,
          published_unit_price: '1',
          group_multiplier: '1',
          recharge_yield: '1',
          effective_multiplier: '1',
          effective_unit_cost: '1',
          formula_version: 'effective-cost/v1',
          evidence: {
            accuracy: 'exact',
            coverage: 'complete',
            freshness: 'current',
            confidence: null,
            observed_at: '2026-07-26T00:00:00Z',
            received_at: '2026-07-26T00:00:01Z',
            fresh_until: '2026-07-26T00:05:00Z',
            missing_fields: [],
          },
        },
      },
    })

    render(<EvidenceDrawer userId={7} evidenceId="evidence-xss" onClose={vi.fn()} />)

    expect(screen.getByText(sourcePayload)).toBeTruthy()
    expect(screen.getByText(modelPayload)).toBeTruthy()
    expect(screen.getByText(groupPayload)).toBeTruthy()
    expect(document.querySelector('img[src="x"]')).toBeNull()
    expect(document.querySelector('script')).toBeNull()
    expect(document.querySelector('[onerror], [onload]')).toBeNull()
  })
})
