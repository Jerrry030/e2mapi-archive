import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import { setLocale } from '../i18n'
import Redeem from './Redeem'

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  setSession('client-token', {
    id: 9,
    email: 'redeemer@example.com',
    display_name: 'Redeemer',
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
    status: ok ? 200 : 400,
    statusText: ok ? '' : 'Bad Request',
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

describe('Redeem page', () => {
  it('redeems a code and refreshes the wallet', async () => {
    mockMatchMedia()
    const fetchMock = vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.includes('/platform/wallet')) {
        return response({
          user_id: 9,
          currency: 'CNY',
          available_micros: 1_000_000,
          reserved_micros: 0,
          version: 1,
          updated_at: '2026-08-04T00:00:00Z',
        })
      }
      if (url.includes('/redeem') && init?.method === 'POST') {
        return response({
          type: 'balance',
          amount_micros: 25_000_000,
          currency: 'CNY',
          wallet: {
            user_id: 9,
            currency: 'CNY',
            available_micros: 26_000_000,
            reserved_micros: 0,
            version: 2,
            updated_at: '2026-08-04T00:01:00Z',
          },
        })
      }
      return response({}, false)
    })
    vi.stubGlobal('fetch', fetchMock)

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    render(
      <QueryClientProvider client={queryClient}>
        <AntdApp>
          <Redeem />
        </AntdApp>
      </QueryClientProvider>,
    )
    await screen.findByText('输入兑换码')

    fireEvent.change(screen.getByTestId('redeem-input'), {
      target: { value: 'AAAAAAAA-BBBBBBBB-CCCCCCCC-DDDDDDDD' },
    })
    fireEvent.click(screen.getByTestId('redeem-submit'))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/redeem',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ code: 'AAAAAAAA-BBBBBBBB-CCCCCCCC-DDDDDDDD' }),
        }),
      )
    })
  })
})
