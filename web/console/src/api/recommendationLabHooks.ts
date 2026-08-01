import { App } from 'antd'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { friendlyErrorMessage } from './errors'
import {
  recommendationLabApi,
  type RecommendationExecutionPolicyInput,
  type RecommendationStatus,
} from './recommendationLab'

const rootKey = ['upstream-intelligence', 'recommendation-lab'] as const

export function useRecommendations(userId?: number, status?: RecommendationStatus) {
  return useQuery({
    queryKey: [...rootKey, 'recommendations', userId ?? 0, status ?? 'all'],
    queryFn: () => recommendationLabApi.recommendations(userId!, status),
    enabled: Boolean(userId),
    refetchInterval: 30_000,
  })
}

export function useRecommendation(userId?: number, recommendationId?: string) {
  return useQuery({
    queryKey: [...rootKey, 'recommendation', userId ?? 0, recommendationId ?? 'none'],
    queryFn: () => recommendationLabApi.recommendation(userId!, recommendationId!),
    enabled: Boolean(userId && recommendationId),
  })
}

export function useRecommendationExperiments(userId?: number, recommendationId?: string) {
  const shadows = useQuery({
    queryKey: [...rootKey, 'shadows', userId ?? 0, recommendationId ?? 'all'],
    queryFn: () => recommendationLabApi.shadows(userId!, recommendationId),
    enabled: Boolean(userId),
  })
  const dryRuns = useQuery({
    queryKey: [...rootKey, 'dry-runs', userId ?? 0, recommendationId ?? 'all'],
    queryFn: () => recommendationLabApi.dryRuns(userId!, recommendationId),
    enabled: Boolean(userId),
  })
  return { shadows, dryRuns }
}

export function useGenerateRecommendations() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (userId: number) => recommendationLabApi.generate(userId),
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: [...rootKey, 'recommendations'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(`推荐快照已生成：${result.recommendations.length} 条可执行候选`)
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useRunRecommendationShadow() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ userId, recommendationId }: { userId: number; recommendationId: string }) =>
      recommendationLabApi.runShadow(userId, recommendationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: rootKey })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('Shadow 排序已完成，未写入远端调度')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useRunRecommendationDryRun() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ userId, recommendationId }: { userId: number; recommendationId: string }) =>
      recommendationLabApi.runDryRun(userId, recommendationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: rootKey })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('Dry-run 计划已生成，未写入远端调度')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useRecommendationExecutionPolicies(userId?: number) {
  return useQuery({
    queryKey: [...rootKey, 'execution-policies', userId ?? 0],
    queryFn: () => recommendationLabApi.executionPolicies(userId!),
    enabled: Boolean(userId),
  })
}

export function useSaveRecommendationExecutionPolicy() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (input: RecommendationExecutionPolicyInput) =>
      recommendationLabApi.saveExecutionPolicy(input),
    onSuccess: (policy) => {
      qc.invalidateQueries({
        queryKey: [...rootKey, 'execution-policies', policy.user_id],
      })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('执行策略已保存')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}
