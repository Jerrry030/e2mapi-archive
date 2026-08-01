import type { AuthUser, UserRole } from '../api/auth'
import type { SecretKind, SecretRef } from '../api/types'

const secretKindsByRole: Record<UserRole, readonly SecretKind[]> = {
  admin: ['notification', 'upstream', 'proxy'],
  client: ['notification'],
  supplier: ['upstream', 'proxy'],
}

export function allowedSecretKinds(role?: UserRole): readonly SecretKind[] {
  return role ? secretKindsByRole[role] : []
}

export function visibleSecretsForRole(secrets: SecretRef[], role?: UserRole): SecretRef[] {
  const allowed = new Set(allowedSecretKinds(role))
  return secrets.filter((secret) => allowed.has(secret.kind))
}

export function requiredRoleForSecretKind(kind: SecretKind): 'client' | 'supplier' {
  return kind === 'notification' ? 'client' : 'supplier'
}

export function eligibleSecretOwners(users: AuthUser[], kind: SecretKind): AuthUser[] {
  const requiredRole = requiredRoleForSecretKind(kind)
  return users.filter((user) => user.enabled && user.roles.includes(requiredRole))
}

export function eligibleInstanceOwners(users: AuthUser[]): AuthUser[] {
  return users.filter((user) => user.enabled && user.roles.includes('client'))
}
