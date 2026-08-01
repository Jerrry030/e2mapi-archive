import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import { setLocale } from '../i18n'
import OwnerRoutingPreferenceCard from './OwnerRoutingPreferenceCard'

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  setSession('client-token', {
    id: 7,
    email: 'owner@example.com',
    roles: ['client'],
    enabled: true,
  })
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

function response(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? '' : 'Not Found',
    text: async () => JSON.stringify(body),
  }
}

function renderCard() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <OwnerRoutingPreferenceCard />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('OwnerRoutingPreferenceCard', () => {
  it('shows four presets and saves the selected owner preference', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        return response({ preference: 'price_first', effective_strategy: 'cost_first' })
      }
      return response({ preference: 'smart_auto', effective_strategy: 'balanced' })
    })
    vi.stubGlobal('fetch', fetchMock)
    renderCard()

    expect(
      (await screen.findByRole('radio', { name: /智能自动/ })).getAttribute('aria-checked'),
    ).toBe('true')
    expect(screen.getAllByRole('radio')).toHaveLength(4)

    fireEvent.click(screen.getByRole('radio', { name: /价格优先/ }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/owner/routing-preference',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify({ preference: 'price_first' }),
        }),
      ),
    )
    expect(await screen.findByText('路由偏好已保存')).toBeTruthy()
    expect(screen.getByRole('radio', { name: /价格优先/ }).getAttribute('aria-checked')).toBe(
      'true',
    )
    expect(screen.getByTestId('effective-strategy').textContent).toBe('正在使用：价格优先')
    expect(screen.queryByText(/cost_first/)).toBeNull()
  })

  it('keeps the health surface usable when the preference endpoint is unavailable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(response({ code: 'not_found', message: 'not ready' }, 404)),
    )
    renderCard()

    expect(await screen.findByText('路由偏好暂不可用')).toBeTruthy()
    expect(screen.getByRole('button', { name: /重\s*试/ })).toBeTruthy()
    expect(screen.queryAllByRole('radio')).toHaveLength(0)
  })

  it('renders the owner-safe explanation in English', async () => {
    setLocale('en')
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          response({ preference: 'success_first', effective_strategy: 'stability_first' }),
        ),
    )
    renderCard()

    expect(await screen.findByText('Routing preference')).toBeTruthy()
    expect(
      (await screen.findByRole('radio', { name: /Success first/ })).getAttribute('aria-checked'),
    ).toBe('true')
    expect(screen.getByTestId('effective-strategy').textContent).toBe('In use: Success first')
    expect(screen.getByText(/Availability always comes first/)).toBeTruthy()
    expect(screen.queryByText(/stability_first/)).toBeNull()
  })
})
