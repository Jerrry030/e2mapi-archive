import { beforeEach, describe, expect, it, vi } from 'vitest'
import { recoveryActionEndpoints } from './recoveryActions'

describe('recovery action endpoints', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    window.localStorage.clear()
  })

  it('targets the exact encoded decision and sends an operator note', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ id: 'decision/1', plan_id: 'plan-1', status: 'approved' }), {
        status: 200,
      }),
    )

    await recoveryActionEndpoints.approve('decision/1', { note: 'on-call checked' })

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/auto-switch-decisions/decision%2F1/approve',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ note: 'on-call checked' }),
      }),
    )
  })

  it('uses the manual recovery endpoint for one exact plan/channel scope', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          plan_id: 'plan/1',
          channel_id: 'channel/1',
          state: 'closed',
          version: 2,
        }),
        { status: 200 },
      ),
    )

    await recoveryActionEndpoints.manualRecover('plan/1', 'channel/1', {
      note: 'upstream confirmed healthy',
    })

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/route-plans/plan%2F1/channels/channel%2F1/recover',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ note: 'upstream confirmed healthy' }),
      }),
    )
  })
})
