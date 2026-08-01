import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { friendlyErrorMessage } from './errors'
import { poolRolloutEndpoints, type PoolRolloutTargetInput } from './poolRollout'

export function usePoolRolloutPreview(poolId?: string) {
  return useQuery({
    queryKey: ['pool-rollout', poolId ?? 'none'],
    queryFn: () => poolRolloutEndpoints.preview(poolId ?? ''),
    enabled: Boolean(poolId),
    refetchInterval: 5_000,
  })
}

export function useUpsertPoolRolloutTarget() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ poolId, input }: { poolId: string; input: PoolRolloutTargetInput }) =>
      poolRolloutEndpoints.upsert(poolId, input),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['pool-rollout', vars.poolId] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('投放范围已更新')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useDeletePoolRolloutTarget() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({
      poolId,
      input,
    }: {
      poolId: string
      input: Pick<PoolRolloutTargetInput, 'scope' | 'user_id' | 'instance_id'>
    }) => poolRolloutEndpoints.remove(poolId, input),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['pool-rollout', vars.poolId] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('投放规则已删除，恢复上级规则')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}
