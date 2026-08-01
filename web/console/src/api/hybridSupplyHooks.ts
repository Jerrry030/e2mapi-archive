import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { hybridSupplyEndpoints } from './hybridSupply'
import type { HybridAllocationInput } from './hybridSupplyTypes'

export function useHybridAllocation(instanceId?: string, enabled = true) {
  return useQuery({
    queryKey: ['hybrid-supply', 'allocation', instanceId],
    queryFn: () => hybridSupplyEndpoints.allocation(instanceId!),
    enabled: enabled && Boolean(instanceId),
    retry: false,
  })
}

export function useHybridRoutingExecutions(instanceId?: string, enabled = true) {
  return useQuery({
    queryKey: ['hybrid-supply', 'executions', instanceId],
    queryFn: () => hybridSupplyEndpoints.executions(instanceId!),
    enabled: enabled && Boolean(instanceId),
    retry: false,
    refetchInterval: 5_000,
  })
}

export function useUpdateHybridAllocation(instanceId: string) {
  const client = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (input: HybridAllocationInput) =>
      hybridSupplyEndpoints.updateAllocation(instanceId, input),
    onSuccess: (allocation) => {
      client.setQueryData(['hybrid-supply', 'allocation', instanceId], allocation)
      void message.success('三池流量比例已保存')
    },
  })
}

export function useExecuteHybridRouting(instanceId: string) {
  const client = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ version, model = '' }: { version: number; model?: string }) =>
      hybridSupplyEndpoints.execute(instanceId, version, model),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['hybrid-supply', 'executions', instanceId] })
      void message.success('已提交三池路由执行')
    },
  })
}
