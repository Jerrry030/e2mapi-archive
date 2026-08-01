import { App } from 'antd'
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CreateInstanceInput,
  CreateSupplyOfferInput,
  NotificationRouteInput,
  RoutePlanInput,
  RouteStrategyFilter,
  UpdateInstanceInput,
  UpstreamChannelInput,
  UpstreamPoolInput,
  UpdateSupplyOfferInput,
  arr,
  endpoints,
} from './endpoints'
import { friendlyErrorMessage } from './errors'
import { getStoredUser, onActiveRoleChange, setStoredUser } from './auth'
import type {
  AccountSwitchInput,
  CreatePaymentProviderInput,
  NotificationDelivery,
  NotificationDeliveryFilter,
  NotificationPersonalChannel,
  PaymentOrderListParams,
  CreateConnectorEnrollmentInput,
  ConnectorTaskExecutionResolveInput,
  RouteStrategy,
  UpdatePaymentConfigInput,
  UpdatePaymentProviderInput,
  UpdateAuthSystemSettingsInput,
  UpdateInstanceMonitorPolicyInput,
  UpdateNotificationTargetInput,
  UpsertSecretInput,
} from './types'
import { t } from '../i18n'

export function useHealth() {
  return useQuery({
    queryKey: ['health'],
    queryFn: endpoints.getHealth,
    refetchInterval: 15_000,
    retry: false,
  })
}

export function usePublicAuthConfig() {
  return useQuery({
    queryKey: ['auth', 'public-config'],
    queryFn: endpoints.publicAuthConfig,
    retry: false,
  })
}

export function useCurrentUser() {
  return useQuery({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      const user = await endpoints.me()
      setStoredUser(user)
      return user
    },
    retry: false,
  })
}

export function useActiveRoleUser() {
  const [user, setUser] = useState(() => getStoredUser())
  useEffect(() => onActiveRoleChange(() => setUser(getStoredUser())), [])
  return user
}

export function useAuthSystemSettings(enabled = true) {
  return useQuery({
    queryKey: ['system', 'auth-settings'],
    queryFn: endpoints.getAuthSystemSettings,
    enabled,
  })
}

export function useUpdateAuthSystemSettings() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpdateAuthSystemSettingsInput) => endpoints.updateAuthSystemSettings(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['system', 'auth-settings'] })
      qc.invalidateQueries({ queryKey: ['auth', 'public-config'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('系统设置已保存')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function usePaymentConfig(enabled = true) {
  return useQuery({
    queryKey: ['admin', 'payment', 'config'],
    queryFn: endpoints.getPaymentConfig,
    enabled,
  })
}

export function useUpdatePaymentConfig() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpdatePaymentConfigInput) => endpoints.updatePaymentConfig(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'payment', 'config'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(t('payment.messages.configSaved'))
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function usePaymentOrders(params: PaymentOrderListParams, enabled = true) {
  return useQuery({
    queryKey: ['admin', 'payment', 'orders', params],
    queryFn: () => endpoints.listPaymentOrders(params),
    enabled,
    placeholderData: (previous) => previous,
  })
}

export function usePaymentOrder(id?: string) {
  return useQuery({
    queryKey: ['admin', 'payment', 'orders', 'detail', id ?? ''],
    queryFn: () => endpoints.getPaymentOrder(id!),
    enabled: Boolean(id),
  })
}

export function useCancelPaymentOrder() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.cancelPaymentOrder(id),
    onSuccess: (order) => {
      qc.invalidateQueries({ queryKey: ['admin', 'payment', 'orders'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      qc.setQueryData(['admin', 'payment', 'orders', 'detail', order.id], (current: unknown) => {
        if (!current || typeof current !== 'object') return current
        return { ...(current as Record<string, unknown>), order }
      })
      message.success(t('payment.orders.messages.cancelled'))
    },
    onError: (e) => {
      qc.invalidateQueries({ queryKey: ['admin', 'payment', 'orders'] })
      message.error(errMsg(e))
    },
  })
}

export function usePaymentProviders(enabled = true) {
  return useQuery({
    queryKey: ['admin', 'payment', 'providers'],
    queryFn: () => endpoints.listPaymentProviders().then(arr),
    enabled,
  })
}

function usePaymentProviderMutation<T>(
  mutationFn: (body: T) => Promise<unknown>,
  successMessage: string,
) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'payment', 'providers'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(successMessage)
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreatePaymentProvider() {
  return usePaymentProviderMutation(
    (body: CreatePaymentProviderInput) => endpoints.createPaymentProvider(body),
    t('payment.messages.providerCreated'),
  )
}

export function useUpdatePaymentProvider() {
  return usePaymentProviderMutation(
    ({ id, body }: { id: string; body: UpdatePaymentProviderInput }) =>
      endpoints.updatePaymentProvider(id, body),
    t('payment.messages.providerUpdated'),
  )
}

export function useDeletePaymentProvider() {
  return usePaymentProviderMutation(
    (id: string) => endpoints.deletePaymentProvider(id),
    t('payment.messages.providerDeleted'),
  )
}

export function useUsers(enabled = true) {
  return useQuery({
    queryKey: ['users'],
    queryFn: () => endpoints.listUsers().then(arr),
    enabled,
  })
}

export function useSecrets(userId?: number) {
  return useQuery({
    queryKey: ['secrets', userId ?? 'all'],
    queryFn: () => endpoints.listSecrets(userId).then(arr),
  })
}

export function useUpsertSecret() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpsertSecretInput) => endpoints.upsertSecret(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['secrets'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('凭证已保存')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useDeleteSecret() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ userId, ref }: { userId?: number; ref: string }) =>
      endpoints.deleteSecret(userId, ref),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['secrets'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('凭证已删除')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useInstances(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['instances', userId ?? 'all'],
    queryFn: () => endpoints.listInstances(userId).then(arr),
    enabled,
    refetchInterval: 20_000,
  })
}

export function useSupplyOffers(supplierId?: number, enabled = true) {
  return useQuery({
    queryKey: ['supply-offers', supplierId ?? 'all'],
    queryFn: () => endpoints.listSupplyOffers(supplierId).then(arr),
    enabled,
  })
}

export function useApprovals(status?: string, userId?: number) {
  return useQuery({
    queryKey: ['approvals', userId ?? 'all', status ?? 'all'],
    queryFn: () => endpoints.listApprovals(userId, status).then(arr),
    refetchInterval: 15_000,
  })
}

export function useSubmitApproval() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: endpoints.submitApproval,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['approvals'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('审批单已提交，待批准后执行')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useDecideApproval() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({
      id,
      decision,
      note,
    }: {
      id: string
      decision: 'approve' | 'reject'
      note?: string
    }) =>
      decision === 'approve'
        ? endpoints.approveApproval(id, 'console')
        : endpoints.rejectApproval(id, 'console', note),
    onSuccess: (ap) => {
      qc.invalidateQueries({ queryKey: ['approvals'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      qc.invalidateQueries({ queryKey: ['accounts'] })
      message.success(
        ap.status === 'executed'
          ? '已批准并执行完成'
          : ap.status === 'rejected'
            ? '已驳回'
            : `状态：${ap.status}`,
      )
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useBillingStatement(userId?: number, period?: string) {
  return useQuery({
    queryKey: ['billing', userId ?? '', period ?? ''],
    queryFn: () => endpoints.getBillingStatement(userId!, period!),
    enabled: !!userId && !!period,
  })
}

export function useHealthSnapshots(instanceId?: string) {
  return useQuery({
    queryKey: ['health-snapshots', instanceId ?? 'all'],
    queryFn: () => endpoints.listHealthSnapshots(instanceId).then(arr),
    refetchInterval: 10_000,
  })
}

export function useInstanceMonitorPolicy(instanceId?: string, enabled = true) {
  return useQuery({
    queryKey: ['instance-monitor-policy', instanceId ?? 'none'],
    queryFn: () => endpoints.getInstanceMonitorPolicy(instanceId!),
    enabled: enabled && !!instanceId,
  })
}

export function useUpdateInstanceMonitorPolicy(instanceId?: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpdateInstanceMonitorPolicyInput) =>
      endpoints.updateInstanceMonitorPolicy(instanceId!, body),
    onSuccess: (policy) => {
      qc.setQueryData(['instance-monitor-policy', policy.instance_id], policy)
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(t('monitorPolicy.saveSuccess'))
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCheckInstanceHealthNow(instanceId?: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: () => endpoints.checkInstanceHealthNow(instanceId!),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['health-snapshots'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
      message.success(t('poolHealth.checkSuccess'))
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useConnectors(userId?: number, status?: string, enabled = true) {
  return useQuery({
    queryKey: ['connectors', userId ?? 'all', status ?? 'all'],
    queryFn: () => endpoints.listConnectors(userId, status).then(arr),
    enabled,
    refetchInterval: 10_000,
  })
}

export function useConnectorTasks(filter?: {
  user_id?: number
  instance_id?: string
  connector_id?: string
  status?: string
  limit?: number
}) {
  return useQuery({
    queryKey: ['connector-tasks', filter ?? {}],
    queryFn: () => endpoints.listConnectorTasks(filter).then(arr),
    refetchInterval: 10_000,
  })
}

export function useResolveConnectorTaskExecution() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ taskId, body }: { taskId: string; body: ConnectorTaskExecutionResolveInput }) =>
      endpoints.resolveConnectorTaskExecution(taskId, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['connector-tasks'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(t('connectors.resolution.success'))
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })
}

export function useCreateConnectorEnrollment() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: CreateConnectorEnrollmentInput) => endpoints.createConnectorEnrollment(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['connectors'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('连接器安装令牌已生成')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRotateConnectorToken() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.rotateConnectorToken(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['connectors'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('连接器 token 已轮换，请立即更新 Connector token 文件')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRevokeConnector() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.revokeConnector(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['connectors'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('连接器已吊销')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateInstanceConnectorInstall() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (instanceId: string) => endpoints.createInstanceConnectorInstall(instanceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['connectors'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('连接器安装命令已生成')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useBindInstanceConnector() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ instanceId, connectorId }: { instanceId: string; connectorId: string }) =>
      endpoints.bindInstanceConnector(instanceId, connectorId),
    onSuccess: (_instance, variables) => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['connectors'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(variables.connectorId ? '实例连接器已绑定' : '实例连接器已解绑')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCapabilities() {
  return useQuery({
    queryKey: ['capabilities'],
    queryFn: () => endpoints.listCapabilities().then(arr),
  })
}

export function useAudits(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['audits', userId ?? 'all'],
    queryFn: () => endpoints.listAudits(userId).then(arr),
    enabled,
  })
}

export function useNotificationRoutes(userId?: number) {
  return useQuery({
    queryKey: ['notification-routes', userId ?? 'all'],
    queryFn: () => endpoints.listNotificationRoutes(userId).then(arr),
  })
}

export function useNotificationChannelStatuses() {
  return useQuery({
    queryKey: ['notification-channel-status'],
    queryFn: () => endpoints.listNotificationChannelStatuses().then(arr),
  })
}

export function useNotificationTargets(userId?: number, enabled = true) {
  return useQuery({
    queryKey: ['notification-targets', userId ?? 'self'],
    queryFn: () => endpoints.listNotificationTargets(userId).then(arr),
    enabled,
  })
}

export function useUpdateNotificationTarget() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({
      channel,
      body,
    }: {
      channel: NotificationPersonalChannel
      body: UpdateNotificationTargetInput
    }) => endpoints.updateNotificationTarget(channel, body),
    onSuccess: (target) => {
      qc.invalidateQueries({ queryKey: ['notification-targets'] })
      qc.invalidateQueries({ queryKey: ['notification-routes'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(target.channel === 'feishu' ? '我的飞书机器人已保存' : '我的 QQ 群已保存')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useDeleteNotificationTarget() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ channel, userId }: { channel: NotificationPersonalChannel; userId?: number }) =>
      endpoints.deleteNotificationTarget(channel, userId),
    onSuccess: (_result, variables) => {
      qc.invalidateQueries({ queryKey: ['notification-targets'] })
      qc.invalidateQueries({ queryKey: ['notification-routes'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(variables.channel === 'feishu' ? '我的飞书机器人已删除' : '我的 QQ 群已删除')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useNotificationDeliveries(filter: NotificationDeliveryFilter = {}) {
  return useQuery({
    queryKey: [
      'notification-deliveries',
      filter.user_id ?? 'all',
      filter.route_id ?? 'all',
      filter.status ?? 'all',
      filter.limit ?? 'default',
    ],
    queryFn: () => endpoints.listNotificationDeliveries(filter).then(arr),
    refetchInterval: (query) => {
      const deliveries = query.state.data as NotificationDelivery[] | undefined
      return deliveries?.some((item) => ['pending', 'processing', 'retrying'].includes(item.status))
        ? 5_000
        : false
    },
  })
}

export function useInstanceAccounts(instanceId: string) {
  return useQuery({
    queryKey: ['instance-accounts', instanceId],
    queryFn: () => endpoints.listInstanceAccounts(instanceId).then(arr),
    refetchInterval: 15_000,
    enabled: !!instanceId,
  })
}

function errMsg(e: unknown): string {
  return friendlyErrorMessage(e)
}

export function useSetSchedulable(instanceId: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (v: { accountId: string; schedulable: boolean; reason?: string }) =>
      endpoints.setAccountSchedulable(instanceId, v.accountId, v.schedulable, v.reason),
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: ['instance-accounts', instanceId] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success(v.schedulable ? '已启用调度' : '已停用调度')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useSwitchUpstream(instanceId: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: AccountSwitchInput) => endpoints.switchUpstream(instanceId, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instance-accounts', instanceId] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('上游账号已切换')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateInstance() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: CreateInstanceInput) => endpoints.createInstance(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['secrets'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('实例已接入')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateInstance() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateInstanceInput }) =>
      endpoints.updateInstance(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['secrets'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('实例已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateNotificationRoute() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: NotificationRouteInput) => endpoints.createNotificationRoute(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notification-routes'] })
      message.success('通知已添加')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateNotificationRoute() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: NotificationRouteInput }) =>
      endpoints.updateNotificationRoute(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notification-routes'] })
      message.success('通知设置已保存')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useDeleteNotificationRoute() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.deleteNotificationRoute(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notification-routes'] })
      message.success('通知已删除')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useTestNotificationRoute() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.testNotificationRoute(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notification-deliveries'] })
      qc.invalidateQueries({ queryKey: ['notification-channel-status'] })
      message.success('测试消息已加入发送队列')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRetryNotificationDelivery() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.retryNotificationDelivery(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notification-deliveries'] })
      qc.invalidateQueries({ queryKey: ['notification-channel-status'] })
      message.success('消息已重新加入发送队列')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useSupplyLedger(offerId?: string) {
  return useQuery({
    queryKey: ['supply-ledger', offerId ?? 'all'],
    queryFn: () => endpoints.listSupplyLedger(offerId).then(arr),
  })
}

export function useAllocateSupplyOffer() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({
      offerId,
      instanceId,
      note,
    }: {
      offerId: string
      instanceId: string
      note?: string
    }) => endpoints.allocateSupplyOffer(offerId, instanceId, note),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['supply-ledger'] })
      qc.invalidateQueries({ queryKey: ['supply-offers'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('供给已分配，台账已记录')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRevokeSupplyLedger() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ ledgerId, note }: { ledgerId: string; note?: string }) =>
      endpoints.revokeSupplyLedger(ledgerId, note),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['supply-ledger'] })
      message.success('分配已回收')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateSupplyOffer() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: CreateSupplyOfferInput) => endpoints.createSupplyOffer(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['supply-offers'] })
      message.success('供给已登记')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateSupplyOffer() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateSupplyOfferInput }) =>
      endpoints.updateSupplyOffer(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['supply-offers'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('供给已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRevokeSupplyOffer() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.revokeSupplyOffer(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['supply-offers'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('供给已撤销')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpstreamPools() {
  return useQuery({
    queryKey: ['upstream-pools'],
    queryFn: () => endpoints.listUpstreamPools().then(arr),
  })
}

export function useUpstreamChannels(poolId?: string) {
  return useQuery({
    queryKey: ['upstream-channels', poolId ?? 'all'],
    queryFn: () => endpoints.listUpstreamChannels(poolId).then(arr),
  })
}

export function useUpstreamKeyDeliveries(enabled = true) {
  return useQuery({
    queryKey: ['upstream-key-deliveries'],
    queryFn: () => endpoints.listUpstreamKeyDeliveries().then(arr),
    enabled,
  })
}

export function useUpsertUpstreamDeliveryKey() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ channelId, value }: { channelId: string; value: string }) =>
      endpoints.upsertUpstreamDeliveryKey(channelId, value),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['upstream-key-deliveries'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('交付 Key 已安全更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useAssignedUpstreamKeys() {
  return useQuery({
    queryKey: ['assigned-upstream-keys'],
    queryFn: () => endpoints.listAssignedUpstreamKeys().then(arr),
  })
}

export function useRoutePlans(userId?: number) {
  return useQuery({
    queryKey: ['route-plans', userId ?? 'all'],
    queryFn: () => endpoints.listRoutePlans(userId).then(arr),
    refetchInterval: 20_000,
  })
}

export function usePublishedBindings(planId?: string) {
  return useQuery({
    queryKey: ['published-bindings', planId ?? 'none'],
    queryFn: () => endpoints.listPublishedBindings(planId).then(arr),
    enabled: !!planId,
  })
}

export function useReconcileRuns(planId?: string) {
  return useQuery({
    queryKey: ['reconcile-runs', planId ?? 'none'],
    queryFn: () => endpoints.listReconcileRuns(planId).then(arr),
    enabled: !!planId,
  })
}

export function useAutoSwitchSummary(planId?: string) {
  return useQuery({
    queryKey: ['auto-switch-summary', planId ?? 'none'],
    queryFn: () => endpoints.getAutoSwitchSummary(planId ?? ''),
    enabled: !!planId,
    refetchInterval: 20_000,
  })
}

export function useOwnerPoolHealth(enabled = true) {
  return useQuery({
    queryKey: ['owner-pool-health'],
    queryFn: endpoints.getOwnerPoolHealth,
    enabled,
    refetchInterval: 20_000,
  })
}

export function useOwnerOnboarding(enabled = true) {
  return useQuery({
    queryKey: ['owner-onboarding'],
    queryFn: endpoints.getOwnerOnboarding,
    enabled,
    refetchInterval: 10_000,
  })
}

export function useOperationsCenter(enabled = true) {
  return useQuery({
    queryKey: ['operations-center'],
    queryFn: endpoints.getOperationsCenter,
    enabled,
    refetchInterval: 20_000,
  })
}

export function useAutoSwitchDecisions(planId?: string) {
  return useQuery({
    queryKey: ['auto-switch-decisions', planId ?? 'none'],
    queryFn: () => endpoints.listAutoSwitchDecisions(planId).then(arr),
    enabled: !!planId,
    refetchInterval: 20_000,
  })
}

export function useRouteStrategies(filter?: RouteStrategyFilter, enabled = true) {
  return useQuery({
    queryKey: [
      'route-strategies',
      filter?.scope ?? 'all',
      filter?.plan_id ?? 'all',
      filter?.pool_id ?? 'all',
      filter?.user_id ?? 'all',
    ],
    queryFn: () => endpoints.listRouteStrategies(filter).then(arr),
    enabled,
  })
}

export function useCreateUpstreamPool() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpstreamPoolInput) => endpoints.createUpstreamPool(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['upstream-pools'] })
      message.success('上游池已创建')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateUpstreamPool() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpstreamPoolInput }) =>
      endpoints.updateUpstreamPool(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['upstream-pools'] })
      message.success('上游池已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateUpstreamChannel() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: UpstreamChannelInput) => endpoints.createUpstreamChannel(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['upstream-channels'] })
      message.success('渠道已创建')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateUpstreamChannel() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpstreamChannelInput }) =>
      endpoints.updateUpstreamChannel(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['upstream-channels'] })
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      message.success('渠道已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useCreateRoutePlan() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: RoutePlanInput) => endpoints.createRoutePlan(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['route-plans'] })
      message.success('发布计划已创建')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpdateRoutePlan() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: RoutePlanInput }) =>
      endpoints.updateRoutePlan(id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['route-plans'] })
      message.success('发布计划已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useReconcileRoutePlan() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: ({ id, dryRun }: { id: string; dryRun: boolean }) =>
      endpoints.reconcileRoutePlan(id, dryRun),
    onSuccess: (plan, vars) => {
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      qc.invalidateQueries({ queryKey: ['instance-accounts'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      qc.invalidateQueries({ queryKey: ['route-plans'] })
      message.success(
        vars.dryRun
          ? `Dry-run 完成：${plan.actions.length} 个动作`
          : `发布完成：${plan.actions.length} 个动作`,
      )
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useRollbackRoutePlan() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.rollbackRoutePlan(id),
    onSuccess: (plan) => {
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      qc.invalidateQueries({ queryKey: ['instance-accounts'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      qc.invalidateQueries({ queryKey: ['route-plans'] })
      message.success(`已回滚：${plan.actions.length} 个动作`)
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useEvaluateAutoSwitch() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (planId: string) => endpoints.evaluateAutoSwitch(planId),
    onSuccess: (decision, planId) => {
      qc.invalidateQueries({ queryKey: ['auto-switch-summary', planId] })
      qc.invalidateQueries({ queryKey: ['auto-switch-decisions', planId] })
      qc.invalidateQueries({ queryKey: ['reconcile-runs', planId] })
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      message.success(decision ? '自动切换评估已生成决策' : '当前无需自动切换')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useObserveAutoSwitchDecision(planId?: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.observeAutoSwitchDecision(id),
    onSuccess: () => {
      if (planId) {
        qc.invalidateQueries({ queryKey: ['auto-switch-summary', planId] })
        qc.invalidateQueries({ queryKey: ['auto-switch-decisions', planId] })
        qc.invalidateQueries({ queryKey: ['reconcile-runs', planId] })
      }
      qc.invalidateQueries({ queryKey: ['published-bindings'] })
      message.success('观察窗口已推进')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useUpsertRouteStrategy(planId?: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (body: RouteStrategy) => endpoints.upsertRouteStrategy(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['route-strategies'] })
      qc.invalidateQueries({ queryKey: ['auto-switch-summary'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('自动切换策略已更新')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}

export function useDeleteRouteStrategy(planId?: string) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: (id: string) => endpoints.deleteRouteStrategy(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['route-strategies'] })
      qc.invalidateQueries({ queryKey: ['auto-switch-summary'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('已恢复继承的自动切换策略')
    },
    onError: (e) => message.error(errMsg(e)),
  })
}
