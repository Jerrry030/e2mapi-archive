import { beforeEach, describe, expect, it, vi } from 'vitest'
import { recommendationLabApi } from './recommendationLab'

function mockJSON(body: unknown = {}) {
  return vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

describe('recommendation lab API boundary', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it.each([
    ['generate', () => recommendationLabApi.generate(7), '/recommendations/generate'],
    ['shadow', () => recommendationLabApi.runShadow(7, 'rec/a'), '/rec%2Fa/shadow'],
    ['dry-run', () => recommendationLabApi.runDryRun(7, 'rec/a'), '/rec%2Fa/dry-run'],
  ])(
    'sends exactly one empty object for the server-derived %s mutation',
    async (_name, run, suffix) => {
      const fetchMock = mockJSON()

      await run()

      const [input, init] = fetchMock.mock.calls[0] ?? []
      expect(String(input)).toContain(suffix)
      expect(new URL(String(input), 'https://console.example').searchParams.get('user_id')).toBe(
        '7',
      )
      expect(init).toEqual(
        expect.objectContaining({
          method: 'POST',
          body: '{}',
        }),
      )
      expect(JSON.parse(String(init?.body))).toEqual({})
      expect(Object.keys(JSON.parse(String(init?.body)))).toHaveLength(0)
    },
  )

  it('sends only the typed policy editor DTO for an explicit policy write', async () => {
    const fetchMock = mockJSON()
    const input = {
      user_id: 7,
      scope: 'plan' as const,
      plan_id: 'plan-1',
      enabled: false,
      kill_switch: true,
      daily_execution_cap: 2,
      cooldown_seconds: 900,
      minimum_savings: '0.1',
      expected_version: 3,
    }

    await recommendationLabApi.saveExecutionPolicy(input)

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/upstream-intelligence/execution-policies',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(input) }),
    )
    const serialized = String(fetchMock.mock.calls[0]?.[1]?.body)
    for (const forbidden of ['candidate', 'evidence', 'weight', 'gate', 'credential', 'token']) {
      expect(serialized).not.toContain(forbidden)
    }
  })
})
