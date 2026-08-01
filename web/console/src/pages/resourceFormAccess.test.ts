import { describe, expect, it } from 'vitest'
import type { AuthUser } from '../api/auth'
import type { SecretRef } from '../api/types'
import {
  allowedSecretKinds,
  eligibleInstanceOwners,
  eligibleSecretOwners,
  visibleSecretsForRole,
} from './resourceFormAccess'

const users: AuthUser[] = [
  { id: 1, email: 'client@example.com', roles: ['client'], enabled: true },
  { id: 2, email: 'supplier@example.com', roles: ['supplier'], enabled: true },
  { id: 3, email: 'both@example.com', roles: ['client', 'supplier'], enabled: true },
  { id: 4, email: 'disabled-client@example.com', roles: ['client'], enabled: false },
  { id: 5, email: 'admin@example.com', roles: ['admin'], enabled: true },
]

const secrets: SecretRef[] = [
  { ref: 'notification', user_id: 1, kind: 'notification', name: 'notice', exists: true },
  { ref: 'upstream', user_id: 1, kind: 'upstream', name: 'api', exists: true },
  { ref: 'proxy', user_id: 1, kind: 'proxy', name: 'proxy', exists: true },
]

describe('resource form access rules', () => {
  it('only exposes business-relevant secret kinds for each active role', () => {
    expect(allowedSecretKinds('client')).toEqual(['notification'])
    expect(allowedSecretKinds('supplier')).toEqual(['upstream', 'proxy'])
    expect(allowedSecretKinds('admin')).toEqual(['notification', 'upstream', 'proxy'])
    expect(visibleSecretsForRole(secrets, 'client').map((secret) => secret.kind)).toEqual([
      'notification',
    ])
    expect(visibleSecretsForRole(secrets, 'supplier').map((secret) => secret.kind)).toEqual([
      'upstream',
      'proxy',
    ])
  })

  it('matches admin secret targets to the role required by the selected kind', () => {
    expect(eligibleSecretOwners(users, 'notification').map((user) => user.id)).toEqual([1, 3])
    expect(eligibleSecretOwners(users, 'upstream').map((user) => user.id)).toEqual([2, 3])
    expect(eligibleSecretOwners(users, 'proxy').map((user) => user.id)).toEqual([2, 3])
  })

  it('only offers enabled client accounts when an admin creates an instance', () => {
    expect(eligibleInstanceOwners(users).map((user) => user.id)).toEqual([1, 3])
  })
})
