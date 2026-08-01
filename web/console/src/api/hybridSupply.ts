import { apiClient } from './client'
import type {
  HybridAllocation,
  HybridAllocationInput,
  HybridRoutingExecution,
} from './hybridSupplyTypes'

export const hybridSupplyEndpoints = {
  allocation: (instanceId: string) =>
    apiClient.request<HybridAllocation>(`/owner/hybrid-supply/allocations/${instanceId}`),
  updateAllocation: (instanceId: string, input: HybridAllocationInput) =>
    apiClient.request<HybridAllocation>(`/owner/hybrid-supply/allocations/${instanceId}`, {
      method: 'PUT',
      body: input,
    }),
  executions: (instanceId: string, limit = 10) =>
    apiClient.request<HybridRoutingExecution[]>('/owner/hybrid-supply/routing-executions', {
      query: { instance_id: instanceId, limit },
    }),
  execute: (instanceId: string, allocationVersion: number, model = '') =>
    apiClient.request<HybridRoutingExecution>(
      `/owner/hybrid-supply/routing-executions/${instanceId}`,
      {
        method: 'POST',
        body: { expected_allocation_version: allocationVersion, model },
      },
    ),
}
