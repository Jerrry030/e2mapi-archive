import { describe, expect, it } from 'vitest'
import { menuForRole } from './consoleMenu'

describe('console menu', () => {
  it('keeps administrators focused on control-plane operations', () => {
    expect(menuForRole('admin').map((node) => node.path)).toEqual([
      '/',
      '/instances',
      '/platform-distribution',
      '/connectors',
      '/pool-health',
      '/notifications',
      '/audits',
      '/users',
      '/system-settings',
    ])
  })

  it('keeps owners focused on their own gateway pools', () => {
    expect(menuForRole('client').map((node) => node.path)).toEqual([
      '/',
      '/platform-distribution',
      '/instances',
      '/connectors',
      '/pool-health',
      '/notifications',
      '/audits',
    ])
  })

  it('does not expose the retired supplier plane or anonymous navigation', () => {
    expect(menuForRole('supplier')).toEqual([])
    expect(menuForRole()).toEqual([])
  })

  it('restores only the native E2M distribution surface, not retired experiments', () => {
    const retired = [
      '/supply',
      '/billing',
      '/payment-orders',
      '/model-market',
      '/assigned-keys',
      '/upstream',
      '/upstream-intelligence',
      '/pool-rollout',
      '/operations-center',
    ]
    const paths = menuForRole('admin').map((node) => node.path)
    expect(paths).toContain('/platform-distribution')
    for (const path of retired) expect(paths).not.toContain(path)
  })
})
