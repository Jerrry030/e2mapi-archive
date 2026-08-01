import { afterEach, describe, expect, it, vi } from 'vitest'
import { endpoints, type UpdateUserInput } from './endpoints'
import type { CreatePaymentProviderInput, UpdatePaymentProviderInput } from './types'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

function mockResponse(body: unknown, status = 200) {
  const text = body === undefined ? '' : JSON.stringify(body)
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    text: async () => text,
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('administrator operation endpoints', () => {
  it('uses the platform distribution management routes', async () => {
    const fetchMock = mockResponse({ id: 'platform-resource-1' })
    const group = { name: 'Stable', resource_class: 'stable' as const, status: 'active' as const }
    const upstream = {
      name: 'OpenAI compatible',
      models: ['gpt-4o-mini', 'gpt-4.1-mini'],
      priority: 10,
      weight: 2,
      capacity: { max_concurrency: 20, capacity_percent: 80, max_request_micros: 1_000_000 },
    }

    await endpoints.updatePlatformGroup('group-1', group)
    await endpoints.deletePlatformGroup('group-1')
    await endpoints.updatePlatformUpstream('upstream-1', upstream)
    await endpoints.testPlatformUpstream('upstream-1')
    await endpoints.deletePlatformUpstream('upstream-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/platform/groups/group-1',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(group) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/platform/groups/group-1',
      expect.objectContaining({ method: 'DELETE' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/platform/upstreams/upstream-1',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(upstream) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/platform/upstreams/upstream-1/test',
      expect.objectContaining({ method: 'POST' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      5,
      '/api/v1/platform/upstreams/upstream-1',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('uses the reference-compatible payment provider CRUD routes', async () => {
    const fetchMock = mockResponse({ id: 'payprov-1' })
    const create: CreatePaymentProviderInput = {
      provider_key: 'stripe',
      name: 'Stripe main',
      config: { publishableKey: 'pk_test', currency: 'CNY' },
      secrets: { secretKey: 'sk_test', webhookSecret: 'whsec_test' },
      supported_types: ['card'],
      enabled: false,
      payment_mode: '',
      sort_order: 0,
      refund_enabled: false,
      allow_user_refund: false,
    }
    const update: UpdatePaymentProviderInput = { enabled: true, secrets: {} }

    await endpoints.listPaymentProviders()
    await endpoints.createPaymentProvider(create)
    await endpoints.updatePaymentProvider('payprov-1', update)
    await endpoints.deletePaymentProvider('payprov-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/admin/payment/providers',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/admin/payment/providers',
      expect.objectContaining({ method: 'POST', body: JSON.stringify(create) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/admin/payment/providers/payprov-1',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(update) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/admin/payment/providers/payprov-1',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('queries and cancels payment orders through administrator routes', async () => {
    const fetchMock = mockResponse({ items: [], total: 0, page: 2, page_size: 20 })

    await endpoints.listPaymentOrders({
      page: 2,
      page_size: 20,
      keyword: 'ord-1',
      status: 'PENDING',
      payment_type: 'stripe',
      provider_instance_id: 'payprov-1',
      order_type: 'balance',
      user_id: 7,
      start_date: '2026-07-01',
      end_date: '2026-07-17',
    })
    await endpoints.getPaymentOrder('payord-1')
    await endpoints.cancelPaymentOrder('payord-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/admin/payment/orders?page=2&page_size=20&keyword=ord-1&status=PENDING&payment_type=stripe&provider_instance_id=payprov-1&order_type=balance&user_id=7&start_date=2026-07-01&end_date=2026-07-17',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/admin/payment/orders/payord-1',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/admin/payment/orders/payord-1/cancel',
      expect.objectContaining({ method: 'POST' }),
    )
  })
  it('reads, updates, and resets a managed user', async () => {
    const fetchMock = mockResponse({ id: 7, email: 'user@example.com', roles: ['client'] })
    const update: UpdateUserInput = {
      email: 'user@example.com',
      display_name: 'User',
      roles: ['supplier'],
      enabled: true,
      expected_updated_at: '2026-07-11T08:00:00Z',
    }

    await endpoints.getUser(7)
    await endpoints.updateUser(7, update)
    await endpoints.resetUserPassword(7, 'replacement123')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/users/7',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/users/7',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(update) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/users/7/reset-password',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ password: 'replacement123' }),
      }),
    )
  })

  it('binds and unbinds an instance connector with the documented body', async () => {
    const fetchMock = mockResponse({ id: 'inst-1', connector_id: 'connector-1' })

    await endpoints.bindInstanceConnector('inst-1', 'connector-1')
    await endpoints.bindInstanceConnector('inst-1', '')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/instances/inst-1/connector',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ connector_id: 'connector-1' }),
      }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/instances/inst-1/connector',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ connector_id: '' }),
      }),
    )
  })

  it('reads, updates, and immediately checks an instance monitor policy', async () => {
    const fetchMock = mockResponse({ instance_id: 'inst-1', enabled: true })
    const policy = {
      enabled: true,
      check_interval_seconds: 60 as const,
      fail_streak: 2 as const,
      auto_switch: false,
      cooldown_seconds: 300 as const,
      drift_detection: true,
    }

    await endpoints.getInstanceMonitorPolicy('inst-1')
    await endpoints.updateInstanceMonitorPolicy('inst-1', policy)
    await endpoints.checkInstanceHealthNow('inst-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/instances/inst-1/monitor-policy',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/instances/inst-1/monitor-policy',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(policy) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/instances/inst-1/health-check',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('deletes a route strategy using its id', async () => {
    const fetchMock = mockResponse(undefined, 204)

    await endpoints.deleteRouteStrategy('strategy-1')

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/route-strategies/strategy-1',
      expect.objectContaining({ method: 'DELETE' }),
    )
  })

  it('uses the notification delivery status, test, list, and retry routes', async () => {
    const fetchMock = mockResponse([])

    await endpoints.listNotificationChannelStatuses()
    await endpoints.testNotificationRoute('route-1')
    await endpoints.listNotificationDeliveries({
      user_id: 7,
      route_id: 'route-1',
      status: 'failed',
      limit: 50,
    })
    await endpoints.retryNotificationDelivery('delivery-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/notification-channels/status',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/notification-routes/route-1/test',
      expect.objectContaining({ method: 'POST' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/notification-deliveries?user_id=7&route_id=route-1&status=failed&limit=50',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/notification-deliveries/delivery-1/retry',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('resolves a connector execution through the platform-admin endpoint', async () => {
    const fetchMock = mockResponse({ id: 'task-1', status: 'failed' })
    const body = {
      lease_nonce: 'a'.repeat(43),
      resolution: 'confirmed_not_applied' as const,
      evidence_note: 'Independent gateway readback confirmed no mutation was applied.',
    }

    await endpoints.resolveConnectorTaskExecution('task-1', body)

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/connector-tasks/task-1/resolve-execution',
      expect.objectContaining({ method: 'POST', body: JSON.stringify(body) }),
    )
  })

  it('reads the owner-safe managed pool health endpoint', async () => {
    const fetchMock = mockResponse({ capacity: { published: 3, schedulable: 2, isolated: 1 } })

    await endpoints.getOwnerPoolHealth()

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/owner/pool-health',
      expect.objectContaining({ method: 'GET' }),
    )
  })

  it('lists and upserts user and pool route strategies with semantic scope keys', async () => {
    const fetchMock = mockResponse([])
    const userStrategy = {
      scope: 'user' as const,
      user_id: 7,
      type: 'stability_first' as const,
      auto_apply: false,
    }
    const poolStrategy = {
      scope: 'pool' as const,
      pool_id: 'pool-1',
      type: 'cost_first' as const,
      auto_apply: true,
    }

    await endpoints.listRouteStrategies({ scope: 'user', user_id: 7 })
    await endpoints.listRouteStrategies({ scope: 'pool', pool_id: 'pool-1' })
    await endpoints.upsertRouteStrategy(userStrategy)
    await endpoints.upsertRouteStrategy(poolStrategy)

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/route-strategies?scope=user&user_id=7',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/route-strategies?scope=pool&pool_id=pool-1',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/v1/route-strategies',
      expect.objectContaining({ method: 'POST', body: JSON.stringify(userStrategy) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/v1/route-strategies',
      expect.objectContaining({ method: 'POST', body: JSON.stringify(poolStrategy) }),
    )
  })

  it('updates and revokes a supply offer through lifecycle endpoints', async () => {
    const offer = {
      supplier_user_id: 7,
      kind: 'api_key' as const,
      provider: 'OpenAI',
      credential_ref: 'credential_ref:user/7/upstream/openai',
    }
    const fetchMock = mockResponse({ id: 'offer-1', ...offer })

    await endpoints.updateSupplyOffer('offer-1', offer)
    await endpoints.revokeSupplyOffer('offer-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/supply-offers/offer-1',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(offer) }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/supply-offers/offer-1/revoke',
      expect.objectContaining({ method: 'POST' }),
    )
  })
})
