// Session token storage + auth state for the console. The token is kept in
// localStorage (the console is an operator tool on a trusted machine) and
// attached to every API request as a Bearer header by client.ts.

export type UserRole = 'admin' | 'client' | 'supplier'
export type UserDeactivationStatus = 'none' | 'draining' | 'failed' | 'completed'
type StoredUserRole = UserRole | 'platform_admin' | 'owner'

export interface AuthUser {
  id: number
  email: string
  display_name?: string
  roles: UserRole[]
  enabled: boolean
  platform_concurrency?: number
  platform_rpm?: number
  deactivation_status?: UserDeactivationStatus
  deactivation_error_code?: string
  deactivation_requested_at?: string | null
  deactivation_completed_at?: string | null
  created_at?: string
  updated_at?: string
}

const TOKEN_KEY = 'e2m.token'
const USER_KEY = 'e2m.user'
const ACTIVE_ROLE_KEY = 'e2m.activeRole'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function normalizeUser(user: AuthUser): AuthUser {
  const roles = Array.isArray(user.roles)
    ? user.roles
        .map((role) => normalizeRole(role as StoredUserRole))
        .filter((role): role is UserRole => Boolean(role))
        .filter((role, index, arr) => arr.indexOf(role) === index)
    : []
  return { ...user, roles }
}

export function getStoredUser(): AuthUser | null {
  const raw = localStorage.getItem(USER_KEY)
  if (!raw) return null
  try {
    return normalizeUser(JSON.parse(raw) as AuthUser)
  } catch {
    return null
  }
}

export function setSession(token: string, user: AuthUser) {
  localStorage.setItem(TOKEN_KEY, token)
  setStoredUser(user)
}

export function setStoredUser(user: AuthUser) {
  const next = normalizeUser(user)
  const activeRole = normalizeRole(localStorage.getItem(ACTIVE_ROLE_KEY) as StoredUserRole | null)
  localStorage.setItem(USER_KEY, JSON.stringify(next))
  if (activeRole && next.roles.includes(activeRole)) {
    localStorage.setItem(ACTIVE_ROLE_KEY, activeRole)
  } else if (activeRole) {
    localStorage.removeItem(ACTIVE_ROLE_KEY)
  }
  window.dispatchEvent(new Event('e2m.activeRoleChanged'))
}

export function clearSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
  localStorage.removeItem(ACTIVE_ROLE_KEY)
}

/** Redirect to /login, remembering where we were. */
export function redirectToLogin() {
  if (window.location.pathname === '/login') return
  const from = encodeURIComponent(window.location.pathname + window.location.search)
  window.location.assign(`/login?from=${from}`)
}

export function hasRole(user: AuthUser | null, role: UserRole): boolean {
  const normalized = normalizeRole(role)
  return Boolean(
    normalized &&
    user?.roles?.some((current) => normalizeRole(current as StoredUserRole) === normalized),
  )
}

export function isPlatformAdmin(user: AuthUser | null): boolean {
  return hasRole(user, 'admin')
}

export function isOwner(user: AuthUser | null): boolean {
  return hasRole(user, 'client')
}

export function isSupplier(user: AuthUser | null): boolean {
  return hasRole(user, 'supplier')
}

export function canWriteOwner(user: AuthUser | null): boolean {
  return isPlatformAdmin(user) || isOwner(user)
}

export function canWriteSupplier(user: AuthUser | null): boolean {
  return isPlatformAdmin(user) || isSupplier(user)
}

export function activeRoleCanUseOwnerSurface(user: AuthUser | null): boolean {
  return isPlatformAdmin(user) || getActiveRole(user) === 'client'
}

export function activeRoleCanUseSupplierSurface(user: AuthUser | null): boolean {
  return isPlatformAdmin(user) || getActiveRole(user) === 'supplier'
}

export function currentUserId(user: AuthUser | null = getStoredUser()): number | undefined {
  return user?.id || undefined
}

export function getActiveRole(user: AuthUser | null = getStoredUser()): UserRole | undefined {
  const normalizedUser = user ? normalizeUser(user) : null
  if (!normalizedUser || normalizedUser.roles.length === 0) return undefined
  const stored = normalizeRole(localStorage.getItem(ACTIVE_ROLE_KEY) as StoredUserRole | null)
  if (stored && normalizedUser.roles.includes(stored)) return stored
  if (normalizedUser.roles.includes('client')) return 'client'
  if (normalizedUser.roles.includes('supplier')) return 'supplier'
  return normalizedUser.roles[0]
}

export function setActiveRole(role: UserRole) {
  const next = normalizeRole(role)
  if (!next) return
  localStorage.setItem(ACTIVE_ROLE_KEY, next)
  window.dispatchEvent(new Event('e2m.activeRoleChanged'))
}

function normalizeRole(role?: StoredUserRole | null): UserRole | undefined {
  switch (role) {
    case 'platform_admin':
    case 'admin':
      return 'admin'
    case 'owner':
    case 'client':
      return 'client'
    case 'supplier':
      return 'supplier'
    default:
      return undefined
  }
}

export function onActiveRoleChange(handler: () => void): () => void {
  window.addEventListener('e2m.activeRoleChanged', handler)
  window.addEventListener('storage', handler)
  return () => {
    window.removeEventListener('e2m.activeRoleChanged', handler)
    window.removeEventListener('storage', handler)
  }
}
