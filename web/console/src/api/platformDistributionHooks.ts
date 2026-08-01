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

export function useCreatePlatformUpstream() {
  return usePlatformMutation(endpoints.createPlatformUpstream, '上游已接入')
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
