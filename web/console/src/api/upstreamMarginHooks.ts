import { useQuery } from '@tanstack/react-query'
import type { IntelligenceWindow } from './upstreamIntelligence'
import {
  upstreamMarginApi,
  toUpstreamMarginBrowserView,
  type UpstreamMarginBrowserView,
} from './upstreamMargin'

export function useUpstreamMarginSummary(userId: number | undefined, window: IntelligenceWindow) {
  return useQuery<UpstreamMarginBrowserView>({
    queryKey: ['upstream-intelligence', 'margin', userId ?? 0, window],
    queryFn: async () =>
      toUpstreamMarginBrowserView(await upstreamMarginApi.summary(userId!, window)),
    enabled: Boolean(userId),
  })
}
