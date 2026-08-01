import { Navigate } from 'react-router'

export const healthOverviewPath = '/pool-health?view=summary'

export function HealthLegacyRedirect() {
  return <Navigate to={healthOverviewPath} replace />
}
