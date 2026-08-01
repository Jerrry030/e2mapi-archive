import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router'
import { setSession, type AuthUser } from '../api/auth'
import { setLocale } from '../i18n'
import ConsoleLayout from './ConsoleLayout'

const admin: AuthUser = {
  id: 1,
  email: 'admin@example.test',
  display_name: 'Admin',
  roles: ['admin'],
  enabled: true,
}

vi.mock('../api/hooks', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/hooks')>()
  return {
    ...actual,
    useCurrentUser: () => ({ data: admin }),
    useHealth: () => ({ data: { status: 'ok', serverTime: '2026-07-27T00:00:00Z' } }),
  }
})

beforeEach(() => {
  localStorage.clear()
  document.title = ''
  vi.stubEnv('NODE_ENV', 'TEST')
  setLocale('zh')
  setSession('test-token', admin)
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
  setLocale('zh')
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  localStorage.clear()
  document.title = ''
})

describe('ConsoleLayout locale updates', () => {
  it('updates the minimal menu and document title when the locale changes', async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/pool-health']}>
          <Routes>
            <Route element={<ConsoleLayout />}>
              <Route path="/pool-health" element={<div>page</div>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )

    expect(await screen.findByText('自有号池健康')).toBeTruthy()
    await waitFor(() => expect(document.title).toBe('自有号池健康 - E2M Ops'))

    setLocale('en')

    expect(await screen.findByText('Owned pool health')).toBeTruthy()
    expect(await screen.findByText('Core healthy')).toBeTruthy()
    expect(await screen.findByText('Administrator')).toBeTruthy()
    await waitFor(() => expect(document.title).toBe('Owned pool health - E2M Ops'))
  })
})
