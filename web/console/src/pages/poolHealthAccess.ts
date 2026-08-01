import { getActiveRole, isPlatformAdmin, type AuthUser, type UserRole } from '../api/auth'

export function showDetailedHealthOperations(
  user: AuthUser | null,
  activeRole: UserRole | undefined = getActiveRole(user),
): boolean {
  return isPlatformAdmin(user) && activeRole === 'admin'
}
