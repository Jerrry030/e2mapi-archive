import { afterEach, describe, expect, it, vi } from 'vitest'
import { routingPreferenceEndpoints } from './routingPreferenceEndpoints'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

function response(body: unknown) {
  return {
    ok: true,
    status: 200,
    statusText: '',
    text: async () => JSON.stringify(body),
  }
}

describe('owner routing preference endpoints', () => {
  it('reads and updates only the owner-safe preference preset', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response({ preference: 'smart_auto', effective_strategy: 'balanced' }))
      .mockResolvedValueOnce(
        response({ preference: 'speed_first', effective_strategy: 'latency_first' }),
      )
    vi.stubGlobal('fetch', fetchMock)

    await routingPreferenceEndpoints.get()
    await routingPreferenceEndpoints.update('speed_first')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/v1/owner/routing-preference',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/v1/owner/routing-preference',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ preference: 'speed_first' }),
      }),
    )
  })
})
