import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
import PaymentSettings from './PaymentSettings'

const configResponse = {
  enabled: false,
  min_amount: 7,
  max_amount: 0,
  daily_limit: 0,
  order_timeout_minutes: 30,
  max_pending_orders: 3,
  enabled_payment_types: [],
  load_balance_strategy: 'round-robin',
  product_name_prefix: 'loaded-prefix',
  product_name_suffix: '',
  help_image_url: '',
  help_text: '',
  visible_method_alipay_source: '',
  visible_method_wxpay_source: '',
  visible_method_alipay_enabled: false,
  visible_method_wxpay_enabled: false,
}

const providerResponse = {
  id: 'payprov-1',
  provider_key: 'easypay',
  name: 'EasyPay main',
  config: { pid: '1001', apiBase: 'https://pay.example.com' },
  secret_configured: { pkey: true },
  supported_types: ['alipay'],
  enabled: true,
  payment_mode: 'qrcode',
  sort_order: 0,
  limits: {},
  refund_enabled: false,
  allow_user_refund: false,
  created_at: '2026-07-17T00:00:00Z',
  updated_at: '2026-07-17T00:00:00Z',
}

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
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

function mockAPI(failedPath?: 'config' | 'providers') {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const isConfig = url.endsWith('/admin/payment/config')
      if (failedPath === (isConfig ? 'config' : 'providers')) {
        return response({ code: 'error', message: 'load failed' }, false)
      }
      return response(isConfig ? configResponse : [providerResponse])
    }),
  )
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

function renderPaymentSettings() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <PaymentSettings />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('PaymentSettings', () => {
  it('renders configured state without ever placing secret plaintext in the DOM', async () => {
    mockAPI()
    mockMatchMedia()
    renderPaymentSettings()

    expect(await screen.findByText('EasyPay main')).toBeTruthy()
    expect(screen.getByText('已配置')).toBeTruthy()
    expect(screen.getByRole('switch', { name: '切换收款渠道 EasyPay main' })).toBeTruthy()
    expect(document.body.textContent).not.toContain('super-secret')
    expect(document.body.textContent).not.toContain('credential_ref:')
  })

  it('restores the last loaded config on reset without refetching', async () => {
    mockAPI()
    mockMatchMedia()
    renderPaymentSettings()

    const input = (await screen.findByLabelText('商品名前缀')) as HTMLInputElement
    expect(input.value).toBe('loaded-prefix')
    fireEvent.change(input, { target: { value: 'changed-prefix' } })
    expect(input.value).toBe('changed-prefix')

    const fetchMock = vi.mocked(fetch)
    const callsBeforeReset = fetchMock.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: '重置' }))

    await waitFor(() => expect(input.value).toBe('loaded-prefix'))
    expect(fetchMock).toHaveBeenCalledTimes(callsBeforeReset)
  })

  it('shows retry UI and disables all payment writes if providers fail to load', async () => {
    mockAPI('providers')
    mockMatchMedia()
    renderPaymentSettings()

    expect(await screen.findByText('收款渠道加载失败')).toBeTruthy()
    expect((screen.getByRole('button', { name: '添加渠道' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect(
      (screen.getByRole('button', { name: '保存收款设置' }) as HTMLButtonElement).disabled,
    ).toBe(true)
    expect((screen.getByRole('button', { name: '重试' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('renders the payment page in English when the locale is English', async () => {
    setLocale('en')
    mockAPI()
    mockMatchMedia()
    renderPaymentSettings()

    expect(await screen.findByText('EasyPay main')).toBeTruthy()
    expect(screen.getByText('Payment channel instances')).toBeTruthy()
    expect(screen.getByText('Global payment settings')).toBeTruthy()
    await waitFor(() =>
      expect(
        (screen.getByRole('button', { name: 'Add channel' }) as HTMLButtonElement).disabled,
      ).toBe(false),
    )
    expect(screen.queryByText('收款渠道实例')).toBeNull()
  })
})
