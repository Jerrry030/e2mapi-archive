import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  upstreamIntelligenceApi,
  type IntelligenceChangeFilter,
  type IntelligenceLinkInput,
  type IntelligenceOverviewFilter,
  type IntelligenceRateFilter,
  type IntelligenceSourceFilter,
} from './upstreamIntelligence'

export function useIntelligenceOverview(
  userId: number | undefined,
  filter: IntelligenceOverviewFilter,
) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'overview', userId ?? 0, filter],
    queryFn: () => upstreamIntelligenceApi.overview(userId!, filter),
    enabled: Boolean(userId),
    refetchInterval: 30_000,
  })
}

export function useIntelligenceSources(
  userId: number | undefined,
  filter: IntelligenceSourceFilter = {},
) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'sources', userId ?? 0, filter],
    queryFn: () => upstreamIntelligenceApi.sources(userId!, filter),
    enabled: Boolean(userId),
  })
}

export function useIntelligenceRates(userId: number | undefined, filter: IntelligenceRateFilter) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'rates', userId ?? 0, filter],
    queryFn: () => upstreamIntelligenceApi.rates(userId!, filter),
    enabled: Boolean(userId),
  })
}

export function useIntelligenceChanges(
  userId: number | undefined,
  filter: IntelligenceChangeFilter,
) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'changes', userId ?? 0, filter],
    queryFn: () => upstreamIntelligenceApi.changes(userId!, filter),
    enabled: Boolean(userId),
  })
}

export function useIntelligenceFrontier(
  userId: number | undefined,
  filter: IntelligenceRateFilter,
) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'frontier', userId ?? 0, filter],
    queryFn: () => upstreamIntelligenceApi.frontier(userId!, filter),
    enabled: Boolean(userId),
  })
}

export function useIntelligenceLinks(userId: number | undefined) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'links', userId ?? 0],
    queryFn: () => upstreamIntelligenceApi.links(userId!),
    enabled: Boolean(userId),
  })
}

export function useSaveIntelligenceLink() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (input: IntelligenceLinkInput) =>
      input.id
        ? upstreamIntelligenceApi.updateLink(input.id, input)
        : upstreamIntelligenceApi.createLink(input),
    onSuccess: (_data, input) => {
      queryClient.invalidateQueries({
        queryKey: ['upstream-intelligence', 'links', input.user_id],
      })
      queryClient.invalidateQueries({ queryKey: ['upstream-intelligence', 'frontier'] })
      queryClient.invalidateQueries({ queryKey: ['upstream-intelligence', 'overview'] })
    },
  })
}

export function useIntelligenceEvidence(userId: number | undefined, evidenceId?: string) {
  return useQuery({
    queryKey: ['upstream-intelligence', 'evidence', userId ?? 0, evidenceId ?? 'none'],
    queryFn: () => upstreamIntelligenceApi.evidence(userId!, evidenceId!),
    enabled: Boolean(userId && evidenceId),
  })
}
