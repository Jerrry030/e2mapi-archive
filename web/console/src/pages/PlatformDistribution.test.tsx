import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import type { PlatformApiKey } from '../api/types'
import { setLocale } from '../i18n'
import PlatformDistribution from './PlatformDistribution'

const platformKey: PlatformApiKey = {
  id: 'platform-key-1',
  user_id: 7,
  group_id: 'group-stable',
  name: 'Production key',
  resource_class: 'stable',
  prefix: 'e2m_live_abc123',
  key_version: 1,
  enabled: true,
  models: ['gpt-4o-mini'],
  daily_limit_micros: 0,
  created_at: '2026-08-01T01:00:00Z',
  updated_at: '2026-08-01T01:00:00Z',
}

const plaintextKey = 'e2m_live_abc123_full_secret_value'

function response(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? '' : 'Not Found',
    text: async () => JSON.stringify(body),
  }
}

function mockMatchMedia() {
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
}

function mockPlatformAPI(options: { admin?: boolean } = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.endsWith('/users')) {
      return response([
        {
          id: 7,
          email: 'owner-one@example.com',
          display_name: 'Owner One',
          roles: ['client'],
          enabled: true,
        },
        {
          id: 8,
          email: 'owner-two@example.com',
          display_name: 'Owner Two',
          roles: ['client'],
          enabled: true,
        },
      ])
    }
    if (url.endsWith('/platform/groups')) {
      return response([
        {
          id: 'group-stable',
          name: 'Stable pool',
          models: ['gpt-4o-mini'],
          status: 'active',
          resource_class: 'stable',
        },
      ])
    }
    if (url.endsWith('/platform/upstreams')) return response([])
    if (url.includes('/platform/wallet?')) {
      const userId = Number(new URL(url, 'http://e2m.test').searchParams.get('user_id'))
      return response({
        user_id: userId,
        currency: 'CNY',
        available_micros: 10_000_000,
        reserved_micros: 0,
        version: 1,
        updated_at: '2026-08-01T01:00:00Z',
      })
    }
    if (url.includes('/platform/api-keys?')) {
      const userId = Number(new URL(url, 'http://e2m.test').searchParams.get('user_id'))
      return response([
        {
          ...platformKey,
          user_id: userId,
          prefix: userId === 8 ? 'e2m_live_owner2' : platformKey.prefix,
        },
      ])
    }
    if (url.includes('/platform/usage?')) return response({ items: [], count: 0 })
    if (url.includes(`/platform/api-keys/${platformKey.id}/value`)) {
      const userId = new URL(url, 'http://e2m.test').searchParams.get('user_id')
      return response({ value: userId === '8' ? 'e2m_live_owner2_full_secret' : plaintextKey })
    }
    return response(
      { code: 'not_found', message: `${options.admin ? 'admin' : 'owner'}: ${url}` },
      404,
    )
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderPlatformDistribution() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <PlatformDistribution />
      </AntdApp>
    </QueryClientProvider>,
  )
}

function keyValueRequests(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(([input]) =>
    String(input).includes(`/platform/api-keys/${platformKey.id}/value`),
  )
}

const originalClipboard = Object.getOwnPropertyDescriptor(window.navigator, 'clipboard')

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  mockMatchMedia()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  setLocale('zh')
  if (originalClipboard) {
    Object.defineProperty(window.navigator, 'clipboard', originalClipboard)
  } else {
    Reflect.deleteProperty(window.navigator, 'clipboard')
  }
})

describe('PlatformDistribution API key display and copy', () => {
  it('keeps a listed key masked until the owner explicitly asks to display it', async () => {
    setSession('owner-token', {
      id: 7,
      email: 'owner-one@example.com',
      roles: ['client'],
      enabled: true,
    })
    const fetchMock = mockPlatformAPI()
    renderPlatformDistribution()

    const keyInput = (await screen.findByDisplayValue(
      new RegExp(`^${platformKey.prefix}`),
    )) as HTMLInputElement
    expect(keyInput.value).not.toBe(plaintextKey)
    expect(keyInput.value).toContain('•')
    expect(document.body.textContent).not.toContain(plaintextKey)
    expect(keyValueRequests(fetchMock)).toHaveLength(0)

    fireEvent.click(screen.getByRole('button', { name: '显示 API Key' }))

    await waitFor(() => expect(keyInput.value).toBe(plaintextKey))
    expect(keyValueRequests(fetchMock)).toHaveLength(1)
    expect(keyValueRequests(fetchMock)[0][0]).toBe(
      `/api/v1/platform/api-keys/${platformKey.id}/value`,
    )

    fireEvent.click(screen.getByRole('button', { name: '隐藏 API Key' }))
    expect(keyInput.value).not.toBe(plaintextKey)
    expect(keyInput.value).toContain('•')
  })

  it('retrieves and copies the complete key when the owner uses quick copy', async () => {
    setSession('owner-token', {
      id: 7,
      email: 'owner-one@example.com',
      roles: ['client'],
      enabled: true,
    })
    const clipboardWrite = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      value: { writeText: clipboardWrite },
    })
    const fetchMock = mockPlatformAPI()
    renderPlatformDistribution()

    await screen.findByDisplayValue(new RegExp(`^${platformKey.prefix}`))
    expect(keyValueRequests(fetchMock)).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: '复制 API Key' }))

    await waitFor(() => expect(clipboardWrite).toHaveBeenCalledWith(plaintextKey))
    expect(keyValueRequests(fetchMock)).toHaveLength(1)
  })

  it('clears a loaded plaintext key when an administrator changes customer context', async () => {
    setSession('admin-token', {
      id: 1,
      email: 'admin@example.com',
      roles: ['admin'],
      enabled: true,
    })
    const fetchMock = mockPlatformAPI({ admin: true })
    renderPlatformDistribution()

    const firstKeyInput = (await screen.findByDisplayValue(
      new RegExp(`^${platformKey.prefix}`),
    )) as HTMLInputElement
    fireEvent.click(screen.getByRole('button', { name: '显示 API Key' }))
    await waitFor(() => expect(firstKeyInput.value).toBe(plaintextKey))
    expect(keyValueRequests(fetchMock)).toHaveLength(1)
    expect(String(keyValueRequests(fetchMock)[0][0])).toContain('user_id=7')

    fireEvent.mouseDown(screen.getAllByRole('combobox')[0])
    fireEvent.click(await screen.findByText('Owner Two (#8)'))

    const secondKeyInput = (await screen.findByDisplayValue(/^e2m_live_owner2/)) as HTMLInputElement
    expect(secondKeyInput.value).not.toBe(plaintextKey)
    expect(secondKeyInput.value).toContain('•')
    expect(keyValueRequests(fetchMock)).toHaveLength(1)
  })
})
