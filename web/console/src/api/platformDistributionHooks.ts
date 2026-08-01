import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { endpoints } from './endpoints'
import type { PlatformWalletAdjustmentInput } from './endpoints'
import { friendlyErrorMessage } from './errors'

const errorText = (error: unknown) => friendlyErrorMessage(error)

export function usePlatformGroups(enabled: boolean) {
  return useQuery({
    queryKey: ['platform', 'groups'],
    queryFn: endpoints.listPlatformGroups,
    enabled,
  })
}

export function usePlatformUpstreams(enabled: boolean) {
  return useQuery({
    queryKey: ['platform', 'upstreams'],
    queryFn: () => endpoints.listPlatformUpstreams(),
    enabled,
  })
}

export function usePlatformWallet(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['platform', 'wallet', userId ?? 'self'],
    queryFn: () => endpoints.getPlatformWallet(userId),
    enabled,
  })
}

export function usePlatformKeys(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['platform', 'keys', userId ?? 'self'],
    queryFn: () => endpoints.listPlatformKeys(userId),
    enabled,
  })
}

export function usePlatformUsage(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['platform', 'usage', userId ?? 'self'],
    queryFn: () => endpoints.listPlatformUsage(userId),
    enabled,
  })
}

function usePlatformMutation<T, R>(mutationFn: (input: T) => Promise<R>, success: string) {
  const queryClient = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['platform'] })
      message.success(success)
    },
    onError: (error) => message.error(errorText(error)),
  })
}

export function useCreatePlatformGroup() {
  return usePlatformMutation(endpoints.createPlatformGroup, '平台分组已创建')
}

export function useUpdatePlatformGroup() {
  return usePlatformMutation(
    ({ id, input }: { id: string; input: import('./endpoints').PlatformGroupUpdateInput }) =>
      endpoints.updatePlatformGroup(id, input),
    '平台分组已更新',
  )
}

export function useDeletePlatformGroup() {
  return usePlatformMutation(endpoints.deletePlatformGroup, '平台分组已进入安全退役流程')
}

export function useCreatePlatformUpstream() {
  return usePlatformMutation(endpoints.createPlatformUpstream, '上游已接入')
}

export function useUpdatePlatformUpstream() {
  return usePlatformMutation(
    ({ id, input }: { id: string; input: import('./endpoints').PlatformUpstreamUpdateInput }) =>
      endpoints.updatePlatformUpstream(id, input),
    '上游已更新',
  )
}

export function useDeletePlatformUpstream() {
  return usePlatformMutation(endpoints.deletePlatformUpstream, '上游已安全下线')
}

export function useTestPlatformUpstream() {
  const { message } = App.useApp()
  return useMutation({
    mutationFn: endpoints.testPlatformUpstream,
    onSuccess: (result) => {
      if (result.ok) {
        message.success(
          `连接正常（${result.latency_ms} ms${result.model_count === undefined ? '' : `，${result.model_count} 个模型`}）`,
        )
      } else {
        const reason =
          result.error_code || (result.status_code ? `HTTP ${result.status_code}` : '上游不可达')
        message.warning(`连接测试未通过：${reason}`)
      }
    },
    onError: (error) => message.error(errorText(error)),
  })
}

export function useCreatePlatformKey() {
  return usePlatformMutation(endpoints.createPlatformKey, 'API Key 已创建')
}

export function usePlatformKeyValue() {
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, userId }: { id: string; userId?: number }) =>
      endpoints.getPlatformKeyValue(id, userId),
    onError: (error) => message.error(errorText(error)),
  })
}

export function useAdjustPlatformWallet() {
  return usePlatformMutation<
    PlatformWalletAdjustmentInput,
    { wallet: import('./types').PlatformWallet }
  >((input) => endpoints.adjustPlatformWallet(input, crypto.randomUUID()), '客户余额已调整')
}
