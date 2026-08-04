import { describe, expect, it } from 'vitest'
import { flatMenuPaths, menuForRole } from './consoleMenu'

// The payments feature flag is off in the test environment, so the menu here
// is the always-on native surface; payment entries have their own gating.
describe('console menu', () => {
  it('splits the administrator sidebar into admin and common sections', () => {
    const sections = menuForRole('admin')
    expect(sections.map((node) => node.section)).toEqual(['admin', 'common'])
    expect(sections[0].routes?.map((node) => node.path)).toEqual([
      '/platform-groups',
      '/platform-upstreams',
      '/instances',
      '/users',
      '/system-settings',
    ])
    expect(sections[1].routes?.map((node) => node.path)).toEqual([
      '/',
      '/platform-distribution',
      '/model-market',
      '/connectors',
      '/pool-health',
      '/notifications',
      '/audits',
    ])
  })

  it('keeps owners on a flat common-module list', () => {
    const nodes = menuForRole('client')
    expect(nodes.every((node) => !node.routes?.length)).toBe(true)
    expect(nodes.map((node) => node.path)).toEqual([
      '/',
      '/platform-distribution',
      '/model-market',
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
    // /model-market is deliberately absent here: the path now serves the
    // native platform price list, not the retired intelligence market page.
    const retired = [
      '/supply',
      '/billing',
      '/assigned-keys',
      '/upstream',
      '/upstream-intelligence',
      '/pool-rollout',
      '/operations-center',
    ]
    const paths = flatMenuPaths('admin')
    expect(paths).toContain('/platform-distribution')
    for (const path of retired) expect(paths).not.toContain(path)
  })
})
