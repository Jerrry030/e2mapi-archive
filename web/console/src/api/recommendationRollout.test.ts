import { beforeEach, describe, expect, it, vi } from 'vitest'
import { recommendationRolloutApi } from './recommendationRollout'

function mockJSON(body: unknown = {}) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(
    async () =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
  )
}

describe('recommendation rollout API boundary', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('keeps list and detail reads owner-scoped and encodes opaque ids', async () => {
    const fetchMock = mockJSON([])

    await recommendationRolloutApi.list(7, { status: 'observing', plan_id: 'plan/a', limit: 25 })
    await recommendationRolloutApi.get(7, 'rollout/a')

    const listURL = new URL(String(fetchMock.mock.calls[0]?.[0]), 'https://console.example')
    expect(listURL.pathname).toBe('/api/v1/upstream-intelligence/rollouts')
    expect(Object.fromEntries(listURL.searchParams)).toEqual({
      user_id: '7',
      status: 'observing',
      plan_id: 'plan/a',
      limit: '25',
    })
    const detailURL = new URL(String(fetchMock.mock.calls[1]?.[0]), 'https://console.example')
    expect(detailURL.pathname).toBe('/api/v1/upstream-intelligence/rollouts/rollout%2Fa')
    expect(Object.fromEntries(detailURL.searchParams)).toEqual({ user_id: '7' })
  })

  it.each([
    [
      'start',
      () => recommendationRolloutApi.start(7, 'recommendation/a'),
      '/recommendations/recommendation%2Fa/rollout',
    ],
    [
      'advance',
      () => recommendationRolloutApi.advance(7, 'rollout/a'),
      '/rollouts/rollout%2Fa/advance',
    ],
    [
      'rollback',
      () => recommendationRolloutApi.rollback(7, 'rollout/a'),
      '/rollouts/rollout%2Fa/rollback',
    ],
  ])(
    'sends a literal empty object for the server-derived %s action',
    async (_name, run, suffix) => {
      const fetchMock = mockJSON()

      await run()

      const [input, init] = fetchMock.mock.calls[0] ?? []
      const url = new URL(String(input), 'https://console.example')
      expect(url.pathname).toBe(`/api/v1/upstream-intelligence${suffix}`)
      expect(url.searchParams.get('user_id')).toBe('7')
      expect(init).toEqual(expect.objectContaining({ method: 'POST', body: '{}' }))
      expect(String(init?.body)).toBe('{}')
      expect(Object.keys(JSON.parse(String(init?.body)))).toHaveLength(0)
      for (const forbidden of ['account', 'weight', 'gate', 'evidence', 'candidate', 'token']) {
        expect(String(init?.body)).not.toContain(forbidden)
      }
    },
  )
})
