import { describe, expect, it } from 'vitest'
import type { AuthUser } from '../api/auth'
import { showDetailedHealthOperations } from './poolHealthAccess'

const user: AuthUser = {
  id: 7,
  email: 'user@example.com',
  roles: ['client'],
  enabled: true,
}

describe('pool health role boundary', () => {
  it('keeps detailed account operations on the active admin surface only', () => {
    expect(showDetailedHealthOperations(user, 'client')).toBe(false)
    expect(showDetailedHealthOperations({ ...user, roles: ['admin'] }, 'admin')).toBe(true)

    const multiRole: AuthUser = { ...user, roles: ['admin', 'client'] }
    expect(showDetailedHealthOperations(multiRole, 'admin')).toBe(true)
    expect(showDetailedHealthOperations(multiRole, 'client')).toBe(false)
    expect(showDetailedHealthOperations(null, undefined)).toBe(false)
  })
})
