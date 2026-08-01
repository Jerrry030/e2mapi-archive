import { App } from 'antd'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { friendlyErrorMessage } from './errors'
import { recoveryActionEndpoints, type OperatorRecoveryNote } from './recoveryActions'

function useDecisionAction(
  action: 'approve' | 'reject' | 'execute',
  successMessage: string,
  planId?: string,
) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ decisionId, note }: { decisionId: string; note?: string }) => {
      if (action === 'approve') return recoveryActionEndpoints.approve(decisionId, { note })
      if (action === 'reject') return recoveryActionEndpoints.reject(decisionId, { note })
      return recoveryActionEndpoints.execute(decisionId)
    },
    onSuccess: (decision) => {
      const targetPlanId = planId ?? decision.plan_id
      qc.invalidateQueries({ queryKey: ['auto-switch-summary', targetPlanId] })
      qc.invalidateQueries({ queryKey: ['auto-switch-decisions', targetPlanId] })
      qc.invalidateQueries({ queryKey: ['reconcile-runs', targetPlanId] })
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      qc.invalidateQueries({ queryKey: ['operations-center'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      if (action === 'execute' && decision.status === 'failed') {
        message.error(friendlyErrorMessage(decision.error || '批准的切换方案执行失败'))
        return
      }
      message.success(successMessage)
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useApproveAutoSwitchDecision(planId?: string) {
  return useDecisionAction('approve', '切换方案已批准，请确认后执行', planId)
}

export function useRejectAutoSwitchDecision(planId?: string) {
  return useDecisionAction('reject', '切换方案已拒绝', planId)
}

export function useExecuteAutoSwitchDecision(planId?: string) {
  return useDecisionAction('execute', '已执行批准的切换方案', planId)
}

export function useManualRecoverQualityCircuit() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({
      planId,
      channelId,
      note,
    }: {
      planId: string
      channelId: string
      note?: OperatorRecoveryNote['note']
    }) => recoveryActionEndpoints.manualRecover(planId, channelId, { note }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['operations-center'] })
      qc.invalidateQueries({ queryKey: ['auto-switch-summary'] })
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('人工恢复已执行，线路已重新进入真实流量观察')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}
