import { useQuery } from '@tanstack/react-query'
import { ownerModelMarketApi, type OwnerModelMarketFilter } from './ownerModelMarket'

export function useOwnerModelMarket(filter: OwnerModelMarketFilter = {}) {
  return useQuery({
    queryKey: ['owner-model-market', filter],
    queryFn: () => ownerModelMarketApi.get(filter),
    retry: false,
    refetchInterval: 30_000,
  })
}
