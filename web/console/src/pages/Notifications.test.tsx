import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setActiveRole, setSession } from '../api/auth'
import type { NotificationDelivery, NotificationRoute } from '../api/types'
import Notifications from './Notifications'

const route: NotificationRoute = {
  id: 'route-1',
  user_id: 7,
  name: '生产异常提醒',
  channel: 'feishu',
  target_ref: 'system:feishu',
  min_risk_level: 'L0',
  min_event_level: 'L2',
  enabled: false,
  created_at: '2026-07-23T01:00:00Z',
  updated_at: '2026-07-23T01:00:00Z',
}

const personalTargets = [
  {
    user_id: 7,
    channel: 'feishu',
    scope: 'personal',
    target_ref: 'credential_ref:user/7/notification/personal-feishu',
    configured: true,
    endpoint_host: 'open.feishu.cn',
    signing_secret_configured: true,
  },
  {
    user_id: 7,
    channel: 'qq',
    scope: 'personal',
    target_ref: 'credential_ref:user/7/notification/personal-qq',
    configured: false,
  },
]

const failedDelivery: NotificationDelivery = {
  id: 'delivery-1',
  user_id: 7,
  route_id: route.id,
  route_name: route.name,
  kind: 'event',
  channel: 'feishu',
  status: 'failed',
  attempts: 3,
  max_attempts: 3,
  last_error_message: '飞书暂时不可用',
  created_at: '2026-07-23T01:00:00Z',
  updated_at: '2026-07-23T01:01:00Z',
}

function response(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
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

function renderNotifications() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <Notifications />
      </AntdApp>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  setSession('client-token', {
    id: 7,
    email: 'owner@example.com',
    roles: ['client'],
    enabled: true,
  })
  mockMatchMedia()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('Notifications', () => {
  it('keeps a multi-role administrator scoped to self in the client view', async () => {
    setSession('multi-role-token', {
      id: 7,
      email: 'multi-role@example.com',
      roles: ['admin', 'client'],
      enabled: true,
    })
    setActiveRole('client')
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/notification-channels/status')) return response([])
      if (url.includes('/notification-targets?')) return response(personalTargets)
      if (url.includes('/notification-routes?')) return response([])
      if (url.includes('/notification-deliveries?')) return response([])
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderNotifications()

    expect(await screen.findByText('我的接收渠道')).toBeTruthy()
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining('/notification-targets?user_id=7'),
        expect.anything(),
      ),
    )
    expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith('/api/v1/users'))).toBe(
      false,
    )
    expect(screen.queryByPlaceholderText('全部账号')).toBeNull()
  })

  it('drops an administrator-selected user when switching to the client view', async () => {
    setSession('multi-role-token', {
      id: 7,
      email: 'multi-role@example.com',
      roles: ['admin', 'client'],
      enabled: true,
    })
    setActiveRole('admin')
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/users')) {
        return response([
          { id: 7, email: 'self@example.com', roles: ['admin', 'client'], enabled: true },
          { id: 42, email: 'other@example.com', roles: ['client'], enabled: true },
        ])
      }
      if (url.endsWith('/notification-channels/status')) return response([])
      if (url.includes('/notification-targets')) return response(personalTargets)
      if (url.includes('/notification-routes')) return response([])
      if (url.includes('/notification-deliveries')) return response([])
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderNotifications()

    expect(
      await screen.findByText('选择一个站长账号后，可查看和维护该账号自己的飞书机器人或 QQ 群。'),
    ).toBeTruthy()
    expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith('/api/v1/users'))).toBe(
      true,
    )

    setActiveRole('client')
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) =>
          String(input).includes('/notification-routes?user_id=7'),
        ),
      ).toBe(true),
    )
  })

  it('queues a test message for a disabled notification without claiming delivery', async () => {
    let deliveries: NotificationDelivery[] = []
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/notification-channels/status')) {
        return response([
          { channel: 'feishu', configured: true, state: 'healthy' },
          { channel: 'qq', configured: false, state: 'unconfigured' },
        ])
      }
      if (url.includes('/notification-targets?')) return response(personalTargets)
      if (url.includes('/notification-routes?')) return response([route])
      if (url.includes('/notification-deliveries?')) return response(deliveries)
      if (url.endsWith('/notification-routes/route-1/test') && init?.method === 'POST') {
        const queued: NotificationDelivery = {
          ...failedDelivery,
          id: 'delivery-test',
          kind: 'test',
          status: 'pending',
          attempts: 0,
          last_error_message: undefined,
        }
        deliveries = [queued]
        return response(queued, 202)
      }
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderNotifications()

    expect(await screen.findByText('生产异常提醒')).toBeTruthy()
    expect(screen.getByText('运行正常')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '发送测试' }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/notification-routes/route-1/test',
        expect.objectContaining({ method: 'POST' }),
      ),
    )
    expect(await screen.findByText('测试消息已加入发送队列')).toBeTruthy()
    expect(screen.queryByText('测试消息已送达')).toBeNull()
  })

  it('only offers manual resend for a failed delivery and queues the retry', async () => {
    let deliveries = [failedDelivery]
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/notification-channels/status')) {
        return response([
          { channel: 'feishu', configured: true, state: 'failing' },
          { channel: 'qq', configured: false, state: 'unconfigured' },
        ])
      }
      if (url.includes('/notification-targets?')) return response(personalTargets)
      if (url.includes('/notification-routes?')) return response([route])
      if (url.includes('/notification-deliveries?')) return response(deliveries)
      if (url.endsWith('/notification-deliveries/delivery-1/retry') && init?.method === 'POST') {
        const queued = { ...failedDelivery, status: 'pending' as const, attempts: 0 }
        deliveries = [queued]
        return response(queued, 202)
      }
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderNotifications()

    expect(await screen.findByText('飞书暂时不可用')).toBeTruthy()
    fireEvent.click(screen.getByText('重新发送'))
    fireEvent.click(await screen.findByRole('button', { name: '确 定' }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/notification-deliveries/delivery-1/retry',
        expect.objectContaining({ method: 'POST' }),
      ),
    )
    expect(await screen.findByText('消息已重新加入发送队列')).toBeTruthy()
  })
})
