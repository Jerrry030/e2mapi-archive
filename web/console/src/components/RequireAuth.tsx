import { useEffect, useState, type ReactNode } from 'react'
import { Navigate } from 'react-router'
import {
  getActiveRole,
  getStoredUser,
  getToken,
  onActiveRoleChange,
  type UserRole,
} from '../api/auth'

// RequireAuth gates the console shell: no token -> /login. The API also
// enforces auth server-side; this only avoids a flash of empty pages.
export function RequireAuth({ children }: { children: ReactNode }) {
  if (!getToken()) {
    const from = encodeURIComponent(window.location.pathname + window.location.search)
    return <Navigate to={`/login?from=${from}`} replace />
  }
  return <>{children}</>
}

export function RequireRole({ roles, children }: { roles: UserRole[]; children: ReactNode }) {
  const [activeRole, setActiveRoleState] = useState<UserRole | undefined>(() =>
    getActiveRole(getStoredUser()),
  )

  useEffect(() => {
    const update = () => setActiveRoleState(getActiveRole(getStoredUser()))
    return onActiveRoleChange(update)
  }, [])

  if (!activeRole || !roles.includes(activeRole)) {
    return <Navigate to="/" replace />
  }
  return <>{children}</>
}
