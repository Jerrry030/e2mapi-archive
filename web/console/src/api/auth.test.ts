import { afterEach, describe, expect, it } from 'vitest'
import {
  activeRoleCanUseOwnerSurface,
  activeRoleCanUseSupplierSurface,
  canWriteOwner,
  canWriteSupplier,
  clearSession,
  getStoredUser,
  getToken,
  isPlatformAdmin,
  onActiveRoleChange,
  setSession,
  setStoredUser,
  type AuthUser,
} from './auth'

const platformAdmin: AuthUser = {
  id: 1,
  email: 'admin@e2m.local',
  roles: ['admin'],
  enabled: true,
}

afterEach(() => {
  localStorage.clear()
})

describe('auth session storage', () => {
  it('stores and clears the active session', () => {
    setSession('session-token', platformAdmin)

    expect(getToken()).toBe('session-token')
    expect(getStoredUser()).toEqual(platformAdmin)

    clearSession()

    expect(getToken()).toBeNull()
    expect(getStoredUser()).toBeNull()
  })

  it('returns null for malformed stored users', () => {
    localStorage.setItem('e2m.user', '{not-json')

    expect(getStoredUser()).toBeNull()
  })

  it('notifies subscribers when the current user is refreshed', () => {
    let changes = 0
    const unsubscribe = onActiveRoleChange(() => {
      changes += 1
    })

    setStoredUser(platformAdmin)

    unsubscribe()
    expect(changes).toBe(1)
  })
})

describe('role helpers', () => {
  it('keeps client and supplier write surfaces separate', () => {
    expect(canWriteOwner(platformAdmin)).toBe(true)
    expect(canWriteSupplier(platformAdmin)).toBe(true)
    expect(canWriteOwner({ ...platformAdmin, roles: ['client'] })).toBe(true)
    expect(canWriteSupplier({ ...platformAdmin, roles: ['supplier'] })).toBe(true)
    expect(canWriteOwner(null)).toBe(false)
    expect(canWriteOwner({ ...platformAdmin, roles: ['supplier'] })).toBe(false)
    expect(canWriteSupplier({ ...platformAdmin, roles: ['client'] })).toBe(false)
  })

  it('detects platform admins', () => {
    expect(isPlatformAdmin(platformAdmin)).toBe(true)
    expect(isPlatformAdmin({ ...platformAdmin, roles: ['client'] })).toBe(false)
    expect(isPlatformAdmin(null)).toBe(false)
  })

  it('uses active role to choose the visible business surface', () => {
    const multiRole = {
      ...platformAdmin,
      roles: ['client', 'supplier'],
    } as AuthUser
    expect(activeRoleCanUseOwnerSurface(multiRole)).toBe(true)
    expect(activeRoleCanUseSupplierSurface(multiRole)).toBe(false)

    localStorage.setItem('e2m.activeRole', 'supplier')
    expect(activeRoleCanUseOwnerSurface(multiRole)).toBe(false)
    expect(activeRoleCanUseSupplierSurface(multiRole)).toBe(true)
  })

  it('normalizes legacy stored role values', () => {
    setSession('legacy-token', {
      ...platformAdmin,
      roles: ['platform_admin', 'owner'] as never,
    })

    expect(getStoredUser()?.roles).toEqual(['admin', 'client'])
    expect(isPlatformAdmin(getStoredUser())).toBe(true)
  })
})
