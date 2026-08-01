import { apiClient } from './client'
import type {
  AccountSwitchInput,
  AssignedUpstreamKey,
  AuthPublicConfig,
  AuthResponse,
  AuthSystemSettings,
  AdapterCapability,
  ApprovalRequest,
  AutoSwitchDecision,
  AutoSwitchSummary,
  BillingStatement,
  CreatePaymentProviderInput,
  Connector,
  ConnectorTask,
  ConnectorTaskExecutionResolveInput,
  CreateConnectorEnrollmentInput,
  CreateConnectorEnrollmentResult,
  GatewayAccount,
  GatewayAccountOwnership,
  Health,
  InstanceHealthSnapshot,
  Instance,
  InstanceKind,
  InstanceMonitorPolicy,
  NotificationChannelStatus,
  NotificationDelivery,
  NotificationDeliveryFilter,
  NotificationRoute,
  NotificationPersonalChannel,
  NotificationTarget,
  OperationAudit,
  OperationsCenter,
  OwnerOnboarding,
  OwnerPoolHealth,
  PublishedBinding,
  PaymentConfig,
  PaymentOrder,
  PaymentOrderDetail,
  PaymentOrderListParams,
  PaymentOrderPage,
  PaymentProvider,
  PlatformGroup,
  PlatformApiKey,
  PlatformListResponse,
  PlatformUpstream,
  PlatformUsage,
  PlatformWallet,
  QualityProbeCapability,
  QualityProbeEndpointPath,
  ReconcilePlan,
  ReconcileRun,
  RolloutMode,
  RoutePlan,
  RoutePlanStatus,
  RegisterInput,
  RouteStrategy,
  RotateConnectorTokenResult,
  SecretRef,
  StrategyScope,
  SupplyOffer,
  SupplyOfferKind,
  SupplyLedgerEntry,
  UpdateAuthSystemSettingsInput,
  UpdatePaymentConfigInput,
  UpdatePaymentProviderInput,
  UpdateInstanceMonitorPolicyInput,
  UpdateNotificationTargetInput,
  UpsertSecretInput,
  UpsertSecretResult,
  UpstreamChannel,
  UpstreamKeyDelivery,
  UpstreamChannelStatus,
  UpstreamInventoryState,
  UpstreamPool,
  UpstreamPoolStatus,
} from './types'

export interface CreateInstanceInput {
  user_id?: number
  name: string
  kind: InstanceKind
}

export interface UpdateInstanceInput {
  name: string
  kind: InstanceKind
}

export interface InstanceResponse extends Instance {
  connector_install?: CreateConnectorEnrollmentResult
}

export interface NotificationRouteInput {
  user_id: number
  name: string
  channel: 'qq' | 'feishu' | 'webhook'
  target_ref: string
  min_risk_level: 'L0' | 'L1' | 'L2' | 'L3'
  min_event_level?: 'L0' | 'L1' | 'L2' | 'L3'
  enabled: boolean
  template?: string
  quiet_window?: string
  escalation_after?: string
}

export interface CreateSupplyOfferInput {
  supplier_user_id: number
  kind: SupplyOfferKind
  provider?: string
  credential_ref: string
  proxy_ref?: string
  quota?: number
  unit_price?: string
  labels?: Record<string, string>
}

export type UpdateSupplyOfferInput = CreateSupplyOfferInput

export interface UpstreamPoolInput {
  name: string
  provider?: string
  models?: string[]
  region?: string
  status?: UpstreamPoolStatus
  safety_stock_threshold?: number
  description?: string
  labels?: Record<string, string>
}

export interface UpstreamChannelInput {
  pool_id: string
  source_id: string
  account_ownership: GatewayAccountOwnership
  display_name: string
  provider?: string
  models?: string[]
  probe_capability?: QualityProbeCapability
  probe_endpoint_path?: QualityProbeEndpointPath
  groups?: string[]
  credential_binding_id?: string
  proxy_binding_id?: string
  priority?: number
  weight?: number
  cost_hint?: number
  status?: UpstreamChannelStatus
  inventory_state?: UpstreamInventoryState
  labels?: Record<string, string>
}

export interface RoutePlanInput {
  instance_id: string
  pool_id: string
  tier?: string
  status?: RoutePlanStatus
  max_channels?: number
  rollout?: RolloutMode
  rollout_batch_size?: number
  rollout_canary_count?: number
  labels?: Record<string, string>
}

export interface RouteStrategyFilter {
  scope?: StrategyScope
  plan_id?: string
  pool_id?: string
  user_id?: number
}

export interface PlatformGroupInput {
  name: string
  description?: string
  provider?: string
  models?: string[]
  region?: string
  labels?: Record<string, string>
  status?: UpstreamPoolStatus
  resource_class: 'economy' | 'stable'
}

export type PlatformGroupUpdateInput = Partial<PlatformGroupInput>

export interface PlatformUpstreamInput {
  group_id: string
  name: string
  base_url: string
  api_key: string
  models?: string[]
  prices?: Record<string, { input_micros_per_million: number; output_micros_per_million: number }>
  currency?: string
  priority?: number
  weight?: number
  status?: UpstreamChannelStatus
  labels?: Record<string, string>
  capacity?: { max_concurrency?: number; capacity_percent?: number; max_request_micros?: number }
  allow_insecure?: boolean
}

export type PlatformUpstreamUpdateInput = Partial<PlatformUpstreamInput>

export interface PlatformKeyInput {
  user_id?: number
  group_id: string
  name: string
  models?: string[]
  daily_limit_micros?: number
  expires_at?: string
  status?: 'active' | 'disabled'
}

export interface PlatformWalletAdjustmentInput {
  user_id: number
  amount_micros: number
  currency?: string
  reason: string
}

export interface CreateUserInput {
  email: string
  password: string
  display_name?: string
  roles: import('./auth').UserRole[]
}

export interface UpdateUserInput {
  email: string
  display_name?: string
  roles: import('./auth').UserRole[]
  enabled: boolean
  expected_updated_at: string
}

export const endpoints = {
  getHealth: () => apiClient.request<Health>('/healthz', { base: '' }),

  publicAuthConfig: () =>
    apiClient.request<AuthPublicConfig>('/auth/public-config', { noAuthRedirect: true }),
  login: (email: string, password: string) =>
    apiClient.request<AuthResponse>('/auth/login', {
      method: 'POST',
      body: { email, password },
      noAuthRedirect: true,
    }),
  register: (body: RegisterInput) =>
    apiClient.request<AuthResponse>('/auth/register', {
      method: 'POST',
      body,
      noAuthRedirect: true,
    }),
  logout: () => apiClient.request<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
  me: () => apiClient.request<import('./auth').AuthUser>('/auth/me'),

  listPlatformGroups: () => apiClient.request<PlatformGroup[]>('/platform/groups'),
  createPlatformGroup: (body: PlatformGroupInput) =>
    apiClient.request<PlatformGroup>('/platform/groups', { method: 'POST', body }),
  updatePlatformGroup: (id: string, body: PlatformGroupUpdateInput) =>
    apiClient.request<PlatformGroup>(`/platform/groups/${id}`, { method: 'PUT', body }),
  deletePlatformGroup: (id: string) =>
    apiClient.request<{ id?: string; status?: string }>(`/platform/groups/${id}`, {
      method: 'DELETE',
    }),
  listPlatformUpstreams: (groupId?: string) =>
    apiClient.request<PlatformUpstream[]>('/platform/upstreams', { query: { group_id: groupId } }),
  createPlatformUpstream: (body: PlatformUpstreamInput) =>
    apiClient.request<PlatformUpstream>('/platform/upstreams', { method: 'POST', body }),
  updatePlatformUpstream: (id: string, body: PlatformUpstreamUpdateInput) =>
    apiClient.request<PlatformUpstream>(`/platform/upstreams/${id}`, { method: 'PUT', body }),
  deletePlatformUpstream: (id: string) =>
    apiClient.request<PlatformUpstream>(`/platform/upstreams/${id}`, { method: 'DELETE' }),
  testPlatformUpstream: (id: string) =>
    apiClient.request<import('./types').PlatformUpstreamTestResult>(
      `/platform/upstreams/${id}/test`,
      { method: 'POST' },
    ),
  listPlatformKeys: (userId?: number) =>
    apiClient.request<PlatformApiKey[]>('/platform/api-keys', { query: { user_id: userId } }),
  createPlatformKey: (body: PlatformKeyInput) =>
    apiClient.request<{ key: PlatformApiKey; plaintext_key: string }>('/platform/api-keys', {
      method: 'POST',
      body,
    }),
  getPlatformKeyValue: (id: string, userId?: number) =>
    apiClient.request<{ value: string }>(`/platform/api-keys/${id}/value`, {
      query: { user_id: userId },
    }),
  getPlatformWallet: (userId?: number) =>
    apiClient.request<PlatformWallet>('/platform/wallet', { query: { user_id: userId } }),
  adjustPlatformWallet: (body: PlatformWalletAdjustmentInput, idempotencyKey: string) =>
    apiClient.request<{ wallet: PlatformWallet }>('/platform/wallet-adjustments', {
      method: 'POST',
      body,
      headers: { 'Idempotency-Key': idempotencyKey },
    }),
  listPlatformUsage: (userId?: number, limit = 50) =>
    apiClient.request<PlatformListResponse<PlatformUsage>>('/platform/usage', {
      query: { user_id: userId, limit },
    }),

  getAuthSystemSettings: () => apiClient.request<AuthSystemSettings>('/system/auth-settings'),
  updateAuthSystemSettings: (body: UpdateAuthSystemSettingsInput) =>
    apiClient.request<AuthSystemSettings>('/system/auth-settings', { method: 'PUT', body }),

  getPaymentConfig: () => apiClient.request<PaymentConfig>('/admin/payment/config'),
  updatePaymentConfig: (body: UpdatePaymentConfigInput) =>
    apiClient.request<PaymentConfig>('/admin/payment/config', { method: 'PUT', body }),
  listPaymentProviders: () => apiClient.request<PaymentProvider[]>('/admin/payment/providers'),
  createPaymentProvider: (body: CreatePaymentProviderInput) =>
    apiClient.request<PaymentProvider>('/admin/payment/providers', { method: 'POST', body }),
  updatePaymentProvider: (id: string, body: UpdatePaymentProviderInput) =>
    apiClient.request<PaymentProvider>(`/admin/payment/providers/${id}`, { method: 'PUT', body }),
  deletePaymentProvider: (id: string) =>
    apiClient.request<{ ok: boolean }>(`/admin/payment/providers/${id}`, { method: 'DELETE' }),

  listPaymentOrders: (query: PaymentOrderListParams) =>
    apiClient.request<PaymentOrderPage>('/admin/payment/orders', { query: { ...query } }),
  getPaymentOrder: (id: string) =>
    apiClient.request<PaymentOrderDetail>(`/admin/payment/orders/${id}`),
  cancelPaymentOrder: (id: string) =>
    apiClient.request<PaymentOrder>(`/admin/payment/orders/${id}/cancel`, { method: 'POST' }),
  listUsers: () => apiClient.request<import('./auth').AuthUser[]>('/users'),
  getUser: (id: number) => apiClient.request<import('./auth').AuthUser>(`/users/${id}`),
  createUser: (body: CreateUserInput) =>
    apiClient.request<import('./auth').AuthUser>('/users', { method: 'POST', body }),
  updateUser: (id: number, body: UpdateUserInput) =>
    apiClient.request<import('./auth').AuthUser>(`/users/${id}`, { method: 'PUT', body }),
  resetUserPassword: (id: number, password: string) =>
    apiClient.request<void>(`/users/${id}/reset-password`, {
      method: 'POST',
      body: { password },
    }),

  listInstances: (userId?: number) =>
    apiClient.request<Instance[]>('/instances', { query: { user_id: userId } }),
  createInstance: (body: CreateInstanceInput) =>
    apiClient.request<InstanceResponse>('/instances', { method: 'POST', body }),
  updateInstance: (id: string, body: UpdateInstanceInput) =>
    apiClient.request<Instance>(`/instances/${id}`, { method: 'PUT', body }),
  createInstanceConnectorInstall: (id: string) =>
    apiClient.request<CreateConnectorEnrollmentResult>(`/instances/${id}/connector-install`, {
      method: 'POST',
    }),
  bindInstanceConnector: (id: string, connectorId: string) =>
    apiClient.request<Instance>(`/instances/${id}/connector`, {
      method: 'PUT',
      body: { connector_id: connectorId },
    }),
  getInstanceMonitorPolicy: (id: string) =>
    apiClient.request<InstanceMonitorPolicy>(`/instances/${id}/monitor-policy`),
  updateInstanceMonitorPolicy: (id: string, body: UpdateInstanceMonitorPolicyInput) =>
    apiClient.request<InstanceMonitorPolicy>(`/instances/${id}/monitor-policy`, {
      method: 'PUT',
      body,
    }),
  checkInstanceHealthNow: (id: string) =>
    apiClient.request<InstanceHealthSnapshot>(`/instances/${id}/health-check`, {
      method: 'POST',
    }),

  listSupplyOffers: (supplierId?: number) =>
    apiClient.request<SupplyOffer[]>('/supply-offers', {
      query: { supplier_user_id: supplierId },
    }),
  createSupplyOffer: (body: CreateSupplyOfferInput) =>
    apiClient.request<SupplyOffer>('/supply-offers', { method: 'POST', body }),
  updateSupplyOffer: (id: string, body: UpdateSupplyOfferInput) =>
    apiClient.request<SupplyOffer>(`/supply-offers/${id}`, { method: 'PUT', body }),
  revokeSupplyOffer: (id: string) =>
    apiClient.request<SupplyOffer>(`/supply-offers/${id}/revoke`, { method: 'POST' }),

  listInstanceAccounts: (instanceId: string) =>
    apiClient.request<GatewayAccount[]>(`/instances/${instanceId}/accounts`),
  setAccountSchedulable: (
    instanceId: string,
    accountId: string,
    schedulable: boolean,
    reason?: string,
  ) =>
    apiClient.request<{ ok: boolean }>(
      `/instances/${instanceId}/accounts/${accountId}/schedulable`,
      { method: 'POST', body: { schedulable, reason } },
    ),
  switchUpstream: (instanceId: string, body: AccountSwitchInput) =>
    apiClient.request<{ ok: boolean }>(`/instances/${instanceId}/accounts/switch`, {
      method: 'POST',
      body,
    }),

  listApprovals: (userId?: number, status?: string) =>
    apiClient.request<ApprovalRequest[]>('/approvals', {
      query: { user_id: userId, status },
    }),
  submitApproval: (body: {
    instance_id: string
    action: string
    account_ids: string[]
    schedulable: boolean
    reason?: string
  }) => apiClient.request<ApprovalRequest>('/approvals', { method: 'POST', body }),
  approveApproval: (id: string, decidedBy?: string) =>
    apiClient.request<ApprovalRequest>(`/approvals/${id}/approve`, {
      method: 'POST',
      body: { decided_by: decidedBy },
    }),
  rejectApproval: (id: string, decidedBy?: string, note?: string) =>
    apiClient.request<ApprovalRequest>(`/approvals/${id}/reject`, {
      method: 'POST',
      body: { decided_by: decidedBy, note },
    }),

  getBillingStatement: (userId: number, period: string) =>
    apiClient.request<BillingStatement>('/billing/statement', {
      query: { user_id: userId, period },
    }),

  listHealthSnapshots: (instanceId?: string) =>
    apiClient.request<InstanceHealthSnapshot[]>('/health-snapshots', {
      query: { instance_id: instanceId },
    }),

  allocateSupplyOffer: (offerId: string, instanceId: string, note?: string) =>
    apiClient.request<SupplyLedgerEntry>(`/supply-offers/${offerId}/allocate`, {
      method: 'POST',
      body: { instance_id: instanceId, note },
    }),
  revokeSupplyLedger: (ledgerId: string, note?: string) =>
    apiClient.request<void>(`/supply-ledger/${ledgerId}/revoke`, {
      method: 'POST',
      body: { note },
    }),
  listSupplyLedger: (offerId?: string) =>
    apiClient.request<SupplyLedgerEntry[]>('/supply-ledger', {
      query: { offer_id: offerId },
    }),

  listConnectors: (userId?: number, status?: string) =>
    apiClient.request<Connector[]>('/connectors', { query: { user_id: userId, status } }),
  createConnectorEnrollment: (body: CreateConnectorEnrollmentInput) =>
    apiClient.request<CreateConnectorEnrollmentResult>('/connectors/enrollments', {
      method: 'POST',
      body,
    }),
  rotateConnectorToken: (id: string) =>
    apiClient.request<RotateConnectorTokenResult>(`/connectors/${id}/rotate-token`, {
      method: 'POST',
    }),
  revokeConnector: (id: string) =>
    apiClient.request<Connector>(`/connectors/${id}/revoke`, { method: 'POST' }),
  listConnectorTasks: (filter?: {
    user_id?: number
    instance_id?: string
    connector_id?: string
    status?: string
    limit?: number
  }) => apiClient.request<ConnectorTask[]>('/connector-tasks', { query: filter }),
  resolveConnectorTaskExecution: (id: string, body: ConnectorTaskExecutionResolveInput) =>
    apiClient.request<ConnectorTask>(`/connector-tasks/${id}/resolve-execution`, {
      method: 'POST',
      body,
    }),
  listCapabilities: () => apiClient.request<AdapterCapability[]>('/adapter-capabilities'),
  listAudits: (userId?: number) =>
    apiClient.request<OperationAudit[]>('/audits', { query: { user_id: userId } }),
  listNotificationRoutes: (userId?: number) =>
    apiClient.request<NotificationRoute[]>('/notification-routes', {
      query: { user_id: userId },
    }),
  createNotificationRoute: (body: NotificationRouteInput) =>
    apiClient.request<NotificationRoute>('/notification-routes', {
      method: 'POST',
      body,
    }),
  updateNotificationRoute: (id: string, body: NotificationRouteInput) =>
    apiClient.request<NotificationRoute>(`/notification-routes/${id}`, {
      method: 'PUT',
      body,
    }),
  deleteNotificationRoute: (id: string) =>
    apiClient.request<void>(`/notification-routes/${id}`, { method: 'DELETE' }),
  listNotificationTargets: (userId?: number) =>
    apiClient.request<NotificationTarget[]>('/notification-targets', {
      query: { user_id: userId },
    }),
  updateNotificationTarget: (
    channel: NotificationPersonalChannel,
    body: UpdateNotificationTargetInput,
  ) =>
    apiClient.request<NotificationTarget>(`/notification-targets/${channel}`, {
      method: 'PUT',
      body,
    }),
  deleteNotificationTarget: (channel: NotificationPersonalChannel, userId?: number) =>
    apiClient.request<{ ok: boolean }>(`/notification-targets/${channel}`, {
      method: 'DELETE',
      query: { user_id: userId },
    }),
  listNotificationChannelStatuses: () =>
    apiClient.request<NotificationChannelStatus[]>('/notification-channels/status'),
  testNotificationRoute: (id: string) =>
    apiClient.request<NotificationDelivery>(`/notification-routes/${id}/test`, {
      method: 'POST',
    }),
  listNotificationDeliveries: (filter?: NotificationDeliveryFilter) =>
    apiClient.request<NotificationDelivery[]>('/notification-deliveries', {
      query: { ...filter },
    }),
  retryNotificationDelivery: (id: string) =>
    apiClient.request<NotificationDelivery>(`/notification-deliveries/${id}/retry`, {
      method: 'POST',
    }),

  listSecrets: (userId?: number) =>
    apiClient.request<SecretRef[]>('/secrets', { query: { user_id: userId } }),
  upsertSecret: (body: UpsertSecretInput) =>
    apiClient.request<UpsertSecretResult>('/secrets', { method: 'POST', body }),
  deleteSecret: (userId: number | undefined, ref: string) =>
    apiClient.request<{ ok: boolean }>('/secrets', {
      method: 'DELETE',
      query: { user_id: userId, ref },
    }),

  listUpstreamPools: () => apiClient.request<UpstreamPool[]>('/upstream-pools'),
  createUpstreamPool: (body: UpstreamPoolInput) =>
    apiClient.request<UpstreamPool>('/upstream-pools', { method: 'POST', body }),
  updateUpstreamPool: (id: string, body: UpstreamPoolInput) =>
    apiClient.request<UpstreamPool>(`/upstream-pools/${id}`, { method: 'PUT', body }),

  listUpstreamChannels: (poolId?: string) =>
    apiClient.request<UpstreamChannel[]>('/upstream-channels', {
      query: { pool_id: poolId },
    }),
  createUpstreamChannel: (body: UpstreamChannelInput) =>
    apiClient.request<UpstreamChannel>('/upstream-channels', { method: 'POST', body }),
  updateUpstreamChannel: (id: string, body: UpstreamChannelInput) =>
    apiClient.request<UpstreamChannel>(`/upstream-channels/${id}`, { method: 'PUT', body }),
  listUpstreamKeyDeliveries: () =>
    apiClient.request<UpstreamKeyDelivery[]>('/upstream-key-deliveries'),
  upsertUpstreamDeliveryKey: (channelId: string, value: string) =>
    apiClient.request<UpstreamKeyDelivery>(`/upstream-channels/${channelId}/delivery-key`, {
      method: 'PUT',
      body: { value },
    }),
  listAssignedUpstreamKeys: () => apiClient.request<AssignedUpstreamKey[]>('/owner/assigned-keys'),

  listRoutePlans: (userId?: number) =>
    apiClient.request<RoutePlan[]>('/route-plans', { query: { user_id: userId } }),
  createRoutePlan: (body: RoutePlanInput) =>
    apiClient.request<RoutePlan>('/route-plans', { method: 'POST', body }),
  updateRoutePlan: (id: string, body: RoutePlanInput) =>
    apiClient.request<RoutePlan>(`/route-plans/${id}`, { method: 'PUT', body }),
  reconcileRoutePlan: (id: string, dryRun: boolean) =>
    apiClient.request<ReconcilePlan>(`/route-plans/${id}/reconcile`, {
      method: 'POST',
      query: { dry_run: dryRun },
    }),
  rollbackRoutePlan: (id: string) =>
    apiClient.request<ReconcilePlan>(`/route-plans/${id}/rollback`, { method: 'POST' }),
  listPublishedBindings: (planId?: string) =>
    apiClient.request<PublishedBinding[]>('/published-bindings', { query: { plan_id: planId } }),
  listReconcileRuns: (planId?: string, limit = 20) =>
    apiClient.request<ReconcileRun[]>('/reconcile-runs', { query: { plan_id: planId, limit } }),
  listAutoSwitchDecisions: (planId?: string, limit = 20) =>
    apiClient.request<AutoSwitchDecision[]>('/auto-switch-decisions', {
      query: { plan_id: planId, limit },
    }),
  getAutoSwitchSummary: (planId: string) =>
    apiClient.request<AutoSwitchSummary>(`/route-plans/${planId}/auto-switch-summary`),
  getOwnerPoolHealth: () => apiClient.request<OwnerPoolHealth>('/owner/pool-health'),
  getOwnerOnboarding: () => apiClient.request<OwnerOnboarding>('/owner/onboarding'),
  getOperationsCenter: () => apiClient.request<OperationsCenter>('/operations-center'),
  evaluateAutoSwitch: (planId: string) =>
    apiClient.request<AutoSwitchDecision | null>(`/route-plans/${planId}/auto-switch/evaluate`, {
      method: 'POST',
    }),
  observeAutoSwitchDecision: (id: string) =>
    apiClient.request<AutoSwitchDecision>(`/auto-switch-decisions/${id}/observe`, {
      method: 'POST',
    }),
  listRouteStrategies: (filter?: RouteStrategyFilter) =>
    apiClient.request<RouteStrategy[]>('/route-strategies', { query: { ...filter } }),
  upsertRouteStrategy: (body: RouteStrategy) =>
    apiClient.request<RouteStrategy>('/route-strategies', { method: 'POST', body }),
  deleteRouteStrategy: (id: string) =>
    apiClient.request<void>(`/route-strategies/${id}`, { method: 'DELETE' }),
}

// The API returns null (not []) for empty lists; normalize to arrays.
export function arr<T>(v: T[] | null | undefined): T[] {
  return v ?? []
}
