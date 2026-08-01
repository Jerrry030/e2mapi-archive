import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { friendlyErrorMessage } from './errors'
import {
  recommendationRolloutApi,
  type RecommendationRollout,
  type RecommendationRolloutFilter,
} from './recommendationRollout'

const rolloutKey = ['upstream-intelligence', 'recommendation-rollouts'] as const

export function useRecommendationRollouts(
  userId?: number,
  filter: RecommendationRolloutFilter = {},
) {
  return useQuery({
    queryKey: [
      ...rolloutKey,
      'list',
      userId ?? 0,
      filter.status ?? 'all',
      filter.plan_id ?? 'all',
      filter.limit ?? 100,
    ],
    queryFn: () =>
      recommendationRolloutApi.list(userId!, { ...filter, limit: filter.limit ?? 100 }),
    enabled: Boolean(userId),
    refetchInterval: 5_000,
  })
}

export function useRecommendationRollout(userId?: number, rolloutId?: string) {
  return useQuery({
    queryKey: [...rolloutKey, 'detail', userId ?? 0, rolloutId ?? 'none'],
    queryFn: () => recommendationRolloutApi.get(userId!, rolloutId!),
    enabled: Boolean(userId && rolloutId),
    refetchInterval: 3_000,
  })
}

function useRolloutMutation<TVariables>(
  mutate: (variables: TVariables) => Promise<RecommendationRollout>,
  successMessage: string,
) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: mutate,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: rolloutKey })
      qc.invalidateQueries({ queryKey: ['upstream-intelligence', 'recommendation-lab'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(successMessage)
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

type RolloutMutationInput = { userId: number; rolloutId: string }

export function useStartRecommendationRollout() {
  return useRolloutMutation(
    ({ userId, recommendationId }: { userId: number; recommendationId: string }) =>
      recommendationRolloutApi.start(userId, recommendationId),
    '已提交 10% 灰度，服务端正在执行并校验回读',
  )
}

export function useAdvanceRecommendationRollout() {
  return useRolloutMutation(
    ({ userId, rolloutId }: RolloutMutationInput) =>
      recommendationRolloutApi.advance(userId, rolloutId),
    '已请求进入下一灰度阶段',
  )
}

export function useRollbackRecommendationRollout() {
  return useRolloutMutation(
    ({ userId, rolloutId }: RolloutMutationInput) =>
      recommendationRolloutApi.rollback(userId, rolloutId),
    '已请求按完整基线回滚，请等待精确回读验证',
  )
}
