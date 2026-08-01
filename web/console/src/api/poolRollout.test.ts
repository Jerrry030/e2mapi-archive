import { beforeEach, describe, expect, it, vi } from 'vitest'
import { poolRolloutEndpoints, type PoolRolloutTargetInput } from './poolRollout'

describe('pool rollout endpoints', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    window.localStorage.clear()
  })

  it('loads targets, effective instances, and durable operation status', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          pool_id: 'pool-1',
          targets: [],
          instances: [],
          operations: [
            {
              id: 'rolloutop-1',
              pool_id: 'pool-1',
              user_id: 1,
              instance_id: 'inst-1',
              action: 'drain',
              status: 'running',
              desired_fingerprint: 'abc',
              attempts: 1,
              version: 2,
              created_at: '2026-07-21T00:00:00Z',
              updated_at: '2026-07-21T00:00:01Z',
            },
          ],
        }),
        { status: 200 },
      ),
    )

    const preview = await poolRolloutEndpoints.preview('pool-1')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/upstream-pools/pool-1/rollout-targets',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(preview.operations[0]).toMatchObject({ action: 'drain', status: 'running' })
  })

  it('upserts and deletes the exact scoped rule', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: 'rollout-1',
          pool_id: 'pool-1',
          scope: 'instance',
          user_id: 7,
          instance_id: 'inst/one',
          enabled: false,
          rollout: 'canary',
          created_at: '2026-07-21T00:00:00Z',
          updated_at: '2026-07-21T00:00:00Z',
        }),
        { status: 200 },
      ),
    )
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))

    const input: PoolRolloutTargetInput = {
      scope: 'instance',
      user_id: 7,
      instance_id: 'inst/one',
      enabled: false,
      rollout: 'canary',
      rollout_canary_count: 1,
    }
    await poolRolloutEndpoints.upsert('pool 1', input)
    await poolRolloutEndpoints.remove('pool 1', input)

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/upstream-pools/pool%201/rollout-targets')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'PUT', body: JSON.stringify(input) })
    expect(fetchMock.mock.calls[1][0]).toBe(
      '/api/v1/upstream-pools/pool%201/rollout-targets?scope=instance&user_id=7&instance_id=inst%2Fone',
    )
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: 'DELETE' })
  })
})
