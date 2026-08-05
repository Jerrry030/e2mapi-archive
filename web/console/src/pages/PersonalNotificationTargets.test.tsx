import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import { setLocale } from '../i18n'
import PersonalNotificationTargets from './PersonalNotificationTargets'

function response(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    text: async () => JSON.stringify(body),
  }
}

function renderTargets() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <PersonalNotificationTargets userId={7} />
      </AntdApp>
    </QueryClientProvider>,
  )
}

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

describe('PersonalNotificationTargets', () => {
  it('does not render secret values and keeps omitted edit fields unchanged', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/notification-targets?')) {
        return response([
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
        ])
      }
      if (url.endsWith('/notification-targets/feishu') && init?.method === 'PUT') {
        return response({
          user_id: 7,
          channel: 'feishu',
          scope: 'personal',
          target_ref: 'credential_ref:user/7/notification/personal-feishu',
          configured: true,
          endpoint_host: 'open.feishu.cn',
          signing_secret_configured: true,
        })
      }
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderTargets()

    expect(await screen.findByText('机器人地址：open.feishu.cn')).toBeTruthy()
    expect(document.body.textContent).not.toContain('credential_ref:')
    fireEvent.click(screen.getByRole('button', { name: '修改配置' }))
    expect(
      await screen.findByText('出于安全考虑，已保存内容不会回显。留空表示保持原值。'),
    ).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '安全保存' }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, options]) =>
          String(url).endsWith('/notification-targets/feishu') &&
          (options as RequestInit | undefined)?.method === 'PUT',
      )
      expect(call).toBeTruthy()
      const body = JSON.parse(String((call?.[1] as RequestInit).body))
      expect(body).toEqual({ user_id: 7, clear_signing_secret: false })
      expect(JSON.stringify(body)).not.toContain('open.feishu.cn')
    })
  })

  it('sends typed QQ fields to the dedicated target API', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/notification-targets?')) {
        return response([
          {
            user_id: 7,
            channel: 'feishu',
            scope: 'personal',
            target_ref: 'credential_ref:user/7/notification/personal-feishu',
            configured: false,
          },
          {
            user_id: 7,
            channel: 'qq',
            scope: 'personal',
            target_ref: 'credential_ref:user/7/notification/personal-qq',
            configured: false,
          },
        ])
      }
      if (url.endsWith('/notification-targets/qq') && init?.method === 'PUT') {
        return response({
          user_id: 7,
          channel: 'qq',
          scope: 'personal',
          target_ref: 'credential_ref:user/7/notification/personal-qq',
          configured: true,
          endpoint_host: 'bot.example.com',
          group_id_masked: '12***90',
          access_token_configured: true,
        })
      }
      return response({ code: 'not_found', message: url }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderTargets()

    await screen.findByText('我的 QQ 群')
    fireEvent.click(screen.getAllByRole('button', { name: '立即配置' })[1])
    fireEvent.change(await screen.findByLabelText('OneBot 地址'), {
      target: { value: 'https://bot.example.com' },
    })
    fireEvent.change(screen.getByLabelText('QQ群号'), { target: { value: '1234567890' } })
    fireEvent.change(screen.getByLabelText('访问令牌'), { target: { value: 'qq-token' } })
    fireEvent.click(screen.getByRole('button', { name: '安全保存' }))

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, options]) =>
          String(url).endsWith('/notification-targets/qq') &&
          (options as RequestInit | undefined)?.method === 'PUT',
      )
      expect(call).toBeTruthy()
      expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({
        user_id: 7,
        onebot_url: 'https://bot.example.com',
        access_token: 'qq-token',
        group_id: '1234567890',
      })
    })
  })
})
