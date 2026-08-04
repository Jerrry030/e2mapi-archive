import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import { setLocale } from '../i18n'
import Recharge from './Recharge'

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  setSession('client-token', {
    id: 7,
    email: 'buyer@example.com',
    display_name: 'Buyer',
    roles: ['client'],
    enabled: true,
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  setLocale('zh')
})

function response(body: unknown, ok = true) {
  return {
    ok,
    status: ok ? 200 : 500,
    statusText: ok ? '' : 'Internal Server Error',
    text: async () => JSON.stringify(body),
  }
}

function mockMatchMedia() {
  vi.stubGlobal(
    'matchMedia',
    vi.fn().mockImplementation((query: string) => ({
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
}

function renderRecharge() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <Recharge />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('Recharge page', () => {
  it('creates a recharge order and redirects to the provider checkout', async () => {
    mockMatchMedia()
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
    const fetchMock = vi.fn().mockImplementation(async (url: string) => {
      if (url.includes('/platform/wallet')) {
        return response({
          user_id: 7,
          currency: 'CNY',
          available_micros: 12_340_000,
          reserved_micros: 0,
          version: 1,
          updated_at: '2026-08-04T00:00:00Z',
        })
      }
      if (url.includes('/owner/hybrid-supply/recharge-orders')) {
        return response({
          order: { id: 'payord-1', status: 'PENDING' },
          checkout_url: 'https://checkout.stripe.example/session/cs_1',
        })
      }
      return response({}, false)
    })
    vi.stubGlobal('fetch', fetchMock)

    renderRecharge()
    await screen.findByText('余额充值')

    fireEvent.click(screen.getByTestId('recharge-submit'))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/owner/hybrid-supply/recharge-orders',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ amount: '50.00', currency: 'CNY', payment_type: 'stripe' }),
        }),
      )
    })
    await waitFor(() => {
      expect(assign).toHaveBeenCalledWith('https://checkout.stripe.example/session/cs_1')
    })
  })
})
