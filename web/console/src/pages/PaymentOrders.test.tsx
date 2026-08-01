import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import type { PaymentOrder } from '../api/types'
import { setLocale } from '../i18n'
import PaymentOrders from './PaymentOrders'

const pendingOrder: PaymentOrder = {
  id: 'payord-1',
  user_id: 7,
  user_email: 'buyer@example.com',
  amount: '100.00',
  pay_amount: '101.00',
  currency: 'CNY',
  fee_rate: '1.00',
  payment_type: 'alipay',
  out_trade_no: 'E2M-ORDER-1',
  status: 'PENDING',
  order_type: 'balance',
  provider_instance_id: 'payprov-1',
  provider_key: 'alipay',
  provider_name: '支付宝主通道',
  refund_amount: '0.00',
  expires_at: '2026-07-17T01:30:00Z',
  created_at: '2026-07-17T01:00:00Z',
  updated_at: '2026-07-17T01:00:00Z',
}

beforeEach(() => {
  localStorage.clear()
  setLocale('zh')
  setSession('admin-token', {
    id: 1,
    email: 'admin@example.com',
    display_name: 'Admin',
    roles: ['admin'],
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

function mockAPI(orders: PaymentOrder[] = [pendingOrder], failure?: 'list' | 'detail') {
  let currentOrders = [...orders]
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/users')) return response([])
    if (url.endsWith('/admin/payment/providers')) {
      return response([
        {
          id: 'payprov-1',
          provider_key: 'alipay',
          name: '支付宝主通道',
          config: {},
          secret_configured: {},
          supported_types: ['alipay'],
          enabled: true,
          sort_order: 0,
          limits: {},
          refund_enabled: false,
          allow_user_refund: false,
          created_at: '2026-07-17T00:00:00Z',
          updated_at: '2026-07-17T00:00:00Z',
        },
      ])
    }
    if (url.endsWith('/admin/payment/orders/payord-1/cancel') && init?.method === 'POST') {
      currentOrders = currentOrders.map((order) =>
        order.id === 'payord-1'
          ? ({ ...order, status: 'CANCELLED' } as typeof pendingOrder)
          : order,
      )
      return response(currentOrders[0])
    }
    if (url.endsWith('/admin/payment/orders/payord-1')) {
      if (failure === 'detail') return response({ code: 'error', message: 'detail failed' }, false)
      return response({ order: currentOrders[0], audit_logs: [] })
    }
    if (url.includes('/admin/payment/orders?')) {
      if (failure === 'list') return response({ code: 'error', message: 'list failed' }, false)
      return response({ items: currentOrders, total: currentOrders.length, page: 1, page_size: 20 })
    }
    return response({ code: 'not_found', message: url }, false)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderOrders() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <PaymentOrders />
      </AntdApp>
    </QueryClientProvider>,
  )
}

describe('PaymentOrders', () => {
  it('renders the execution boundary and teaching empty state', async () => {
    mockMatchMedia()
    mockAPI([])
    renderOrders()

    expect(await screen.findByText('当前提供订单查询与待支付订单取消能力')).toBeTruthy()
    expect(
      await screen.findByText('暂无收款订单；接入支付执行链路后，订单会自动显示在这里。'),
    ).toBeTruthy()
  })

  it('shows order details and cancels only a pending order without a provider trade number', async () => {
    mockMatchMedia()
    const fetchMock = mockAPI()
    renderOrders()

    expect(await screen.findByText('E2M-ORDER-1')).toBeTruthy()
    expect(screen.getByText('支付宝')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '详情' }))
    expect(await screen.findByText('收款订单详情')).toBeTruthy()

    const cancelButtons = screen.getAllByRole('button', { name: '取消订单' })
    fireEvent.click(cancelButtons[0])
    fireEvent.click(await screen.findByRole('button', { name: '确认取消' }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/admin/payment/orders/payord-1/cancel',
        expect.objectContaining({ method: 'POST' }),
      ),
    )
    expect(await screen.findByText('订单已取消')).toBeTruthy()
  })

  it('shows retry and no cancellation action when the order list fails', async () => {
    mockMatchMedia()
    mockAPI([pendingOrder], 'list')
    renderOrders()

    expect(await screen.findByText('收款订单加载失败')).toBeTruthy()
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '取消订单' })).toBeNull()
  })

  it('hides drawer cancellation when detail loading fails', async () => {
    mockMatchMedia()
    mockAPI([pendingOrder], 'detail')
    renderOrders()

    expect(await screen.findByText('E2M-ORDER-1')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '详情' }))
    expect(await screen.findByText('订单详情加载失败')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: '取消订单' })).toHaveLength(1)
  })

  it('renders the administrator order surface in English', async () => {
    setLocale('en')
    mockMatchMedia()
    mockAPI([])
    renderOrders()

    expect(await screen.findByText('Payment orders')).toBeTruthy()
    expect(
      screen.getByText('Order queries and pending-order cancellation are available'),
    ).toBeTruthy()
    expect(screen.queryByText('收款订单')).toBeNull()
  })
  it('does not offer cancellation after a provider transaction exists', async () => {
    mockMatchMedia()
    mockAPI([{ ...pendingOrder, payment_trade_no: 'provider-trade-1' }])
    renderOrders()

    expect(await screen.findByText('E2M-ORDER-1')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '取消订单' })).toBeNull()
  })
})
