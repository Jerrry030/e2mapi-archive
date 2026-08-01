import type { InstanceHealthSnapshot } from '../api/types'

export interface HealthSummaryMetrics {
  healthPercent?: number
  scheduledAccounts: number
  totalAccounts: number
  unhealthyAccounts: number
}

export function healthPercent(healthy: number, total: number): number | undefined {
  return total > 0 ? Math.round((healthy / total) * 100) : undefined
}

export function healthSummaryMetrics(snapshots: InstanceHealthSnapshot[]): HealthSummaryMetrics {
  const totalAccounts = snapshots.reduce((sum, item) => sum + item.total_accounts, 0)
  const healthyAccounts = snapshots.reduce((sum, item) => sum + item.healthy_count, 0)
  const scheduledAccounts = snapshots.reduce((sum, item) => sum + item.schedulable_count, 0)
  const unhealthyAccounts = snapshots.reduce(
    (sum, item) => sum + (item.accounts ?? []).filter((account) => !account.healthy).length,
    0,
  )

  return {
    healthPercent: healthPercent(healthyAccounts, totalAccounts),
    scheduledAccounts,
    totalAccounts,
    unhealthyAccounts,
  }
}
