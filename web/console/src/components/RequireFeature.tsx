import type { ReactNode } from 'react'
import { Navigate } from 'react-router'
import { consoleFeatureFlags, type ConsoleFeatureFlags } from '../config/featureFlags'

export function RequireFeature({
  feature,
  children,
}: {
  feature: keyof ConsoleFeatureFlags
  children: ReactNode
}) {
  if (!consoleFeatureFlags[feature]) return <Navigate to="/" replace />
  return <>{children}</>
}
