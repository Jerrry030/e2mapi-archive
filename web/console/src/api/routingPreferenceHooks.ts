import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getLocale } from '../i18n'
import { routingPreferenceEndpoints } from './routingPreferenceEndpoints'
import type { OwnerRoutingPreference } from './routingPreferenceTypes'

export const ownerRoutingPreferenceQueryKey = ['owner-routing-preference'] as const

export function useOwnerRoutingPreference(enabled = true) {
  return useQuery({
    queryKey: ownerRoutingPreferenceQueryKey,
    queryFn: routingPreferenceEndpoints.get,
    enabled,
    retry: false,
  })
}

export function useUpdateOwnerRoutingPreference() {
  const queryClient = useQueryClient()
  const { message } = App.useApp()

  return useMutation({
    mutationFn: (preference: OwnerRoutingPreference) =>
      routingPreferenceEndpoints.update(preference),
    onSuccess: (result) => {
      queryClient.setQueryData(ownerRoutingPreferenceQueryKey, result)
      void queryClient.invalidateQueries({ queryKey: ['owner-pool-health'] })
      message.success(getLocale() === 'en' ? 'Routing preference saved' : '路由偏好已保存')
    },
  })
}
