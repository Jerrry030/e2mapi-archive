// Hand-written TS mirrors of packages/e2m-contracts. Field names match the JSON
// (snake_case) exactly. Replaced by an OpenAPI-generated client in W4.

export type InstanceKind = 'sub2api' | 'newapi' | 'cpa'
export type InstanceStatus = 'unknown' | 'active' | 'degraded' | 'offline' | 'maintenance'

export interface AuthPublicConfig {
  registration_enabled: boolean
  registration_default_role: import('./auth').UserRole
  registration_email_suffix_whitelist: string[]
  invitation_required?: boolean
  turnstile_enabled: boolean
  turnstile_site_key: string
}

export interface RegisterInput {
  email: string
  password: string
  display_name?: string
  turnstile_token?: string
  invitation_code?: string
}

export interface AuthResponse {
  token: string
  user: import('./auth').AuthUser
  expires_at: string
}

export interface AuthSystemSettings {
  registration_enabled: boolean
  registration_email_suffix_whitelist: string[]
  invitation_required?: boolean
  turnstile_enabled: boolean
  turnstile_site_key: string
  turnstile_secret_configured: boolean
  updated_at?: string
}

export interface UpdateAuthSystemSettingsInput {
  registration_enabled: boolean
  registration_email_suffix_whitelist: string[]
  invitation_required?: boolean
  turnstile_enabled: boolean
  turnstile_site_key: string
  turnstile_secret_key?: string
  clear_turnstile_secret?: boolean
}

export type PaymentProviderKey = 'easypay' | 'alipay' | 'wxpay' | 'stripe' | 'airwallex'

export interface PaymentMethodLimit {
  singleMin?: number
  singleMax?: number
  dailyLimit?: number
}

export interface PaymentProvider {
  id: string
  provider_key: PaymentProviderKey
  name: string
  config: Record<string, string>
  secret_configured: Record<string, boolean>
  supported_types: string[]
  enabled: boolean
  payment_mode?: string
  sort_order: number
  limits: Record<string, PaymentMethodLimit>
  refund_enabled: boolean
  allow_user_refund: boolean
  created_at: string
  updated_at: string
}

export interface CreatePaymentProviderInput {
  provider_key: PaymentProviderKey
  name: string
  config: Record<string, string>
  secrets?: Record<string, string>
  supported_types: string[]
  enabled: boolean
  payment_mode?: string
  sort_order: number
  limits?: Record<string, PaymentMethodLimit>
  refund_enabled: boolean
  allow_user_refund: boolean
}

export interface UpdatePaymentProviderInput {
  name?: string
  config?: Record<string, string>
  secrets?: Record<string, string>
  clear_secrets?: string[]
  supported_types?: string[]
  enabled?: boolean
  payment_mode?: string
  sort_order?: number
  limits?: Record<string, PaymentMethodLimit>
  refund_enabled?: boolean
  allow_user_refund?: boolean
}

export interface PaymentConfig {
  enabled: boolean
  min_amount: number
  max_amount: number
  daily_limit: number
  order_timeout_minutes: number
  max_pending_orders: number
  enabled_payment_types: string[]
  load_balance_strategy: 'round-robin' | 'least-amount'
  product_name_prefix: string
  product_name_suffix: string
  help_image_url: string
  help_text: string
  visible_method_alipay_source: '' | 'official_alipay' | 'easypay_alipay'
  visible_method_wxpay_source: '' | 'official_wxpay' | 'easypay_wxpay'
  visible_method_alipay_enabled: boolean
  visible_method_wxpay_enabled: boolean
  updated_at?: string
}

export type UpdatePaymentConfigInput = Omit<PaymentConfig, 'updated_at'>

export type PaymentOrderStatus =
  | 'PENDING'
  | 'PAID'
  | 'RECHARGING'
  | 'COMPLETED'
  | 'EXPIRED'
  | 'CANCELLED'
  | 'FAILED'
  | 'REFUND_REQUESTED'
  | 'REFUNDING'
  | 'REFUND_PENDING'
  | 'PARTIALLY_REFUNDED'
  | 'REFUNDED'
  | 'REFUND_FAILED'

export type PaymentOrderType = 'balance' | 'subscription'

export interface PaymentOrder {
  id: string
  user_id: number
  user_email?: string
  user_name?: string
  amount: string
  pay_amount: string
  currency: string
  fee_rate: string
  payment_type: string
  out_trade_no: string
  payment_trade_no?: string
  status: PaymentOrderStatus
  order_type: PaymentOrderType
  provider_instance_id?: string
  provider_key?: string
  provider_name?: string
  refund_amount: string
  refund_reason?: string
  refund_requested_at?: string
  refund_requested_by?: string
  refund_request_reason?: string
  expires_at: string
  paid_at?: string
  completed_at?: string
  failed_at?: string
  failed_reason?: string
  created_at: string
  updated_at: string
}

export interface PaymentOrderListParams {
  page: number
  page_size: number
  keyword?: string
  status?: PaymentOrderStatus
  payment_type?: string
  provider_instance_id?: string
  order_type?: PaymentOrderType
  user_id?: number
  start_date?: string
  end_date?: string
}

export interface PaymentOrderPage {
  items: PaymentOrder[]
  total: number
  page: number
  page_size: number
}

export interface PaymentOrderDetail {
  order: PaymentOrder
  audit_logs: OperationAudit[]
}

export interface CreateRechargeOrderInput {
  amount: string
  currency?: string
  payment_type: string
  return_url?: string
}

export interface ModelMarketPrice {
  model: string
  currency?: string
  input_micros_per_million?: number
  output_micros_per_million?: number
  available: boolean
}

export interface ModelMarketGroup {
  group_id: string
  group_name: string
  description?: string
  resource_class: string
  models: ModelMarketPrice[]
}

export type RedeemCodeType = 'balance' | 'invitation'
export type RedeemCodeStatus = 'unused' | 'used' | 'disabled' | 'expired'

export interface RedeemCode {
  id: string
  type: RedeemCodeType
  code_prefix: string
  currency: string
  amount_micros: number
  status: RedeemCodeStatus
  batch_id: string
  notes?: string
  expires_at?: string
  used_by?: number
  used_at?: string
  created_by: number
  created_at: string
  updated_at: string
}

export interface RedeemCodePage {
  items: RedeemCode[]
  total: number
  page: number
  page_size: number
}

export interface RedeemCodeListParams {
  type?: RedeemCodeType
  status?: RedeemCodeStatus
  batch_id?: string
  page?: number
  page_size?: number
}

export interface GenerateRedeemCodesInput {
  type: RedeemCodeType
  count: number
  amount?: string
  currency?: string
  notes?: string
  expires_at?: string
}

export interface GenerateRedeemCodesResponse {
  batch_id: string
  codes: string[]
  items: RedeemCode[]
}

export interface RedeemResponse {
  type: RedeemCodeType
  amount_micros: number
  currency: string
  wallet: PlatformWallet
}

export interface RechargeOrderResponse {
  order: PaymentOrder
  checkout_url: string
}

export type SecretKind = 'notification' | 'upstream' | 'proxy'

export interface SecretRef {
  ref: string
  user_id: number
  kind: SecretKind
  name: string
  exists: boolean
  created_at?: string
  updated_at?: string
}

export interface UpsertSecretInput {
  user_id: number
  kind: SecretKind
  name: string
  value: string
}

export interface UpsertSecretResult {
  secret: SecretRef
}
export interface Instance {
  id: string
  user_id: number
  name: string
  kind: InstanceKind
  status: InstanceStatus
  connector_id?: string
  created_at: string
  updated_at: string
}

export type SupplyOfferKind = 'oauth_subscription' | 'api_key'
export type SupplyOfferStatus = 'pending' | 'active' | 'exhausted' | 'revoked'

export interface SupplyOffer {
  id: string
  supplier_user_id: number
  kind: SupplyOfferKind
  provider?: string
  credential_ref: string
  proxy_ref?: string
  status: SupplyOfferStatus
  quota?: number
  unit_price?: string
  labels?: Record<string, string>
  created_at: string
  updated_at: string
}

export type SupplyLedgerEntryStatus = 'allocated' | 'revoked'

export interface SupplyLedgerEntry {
  id: string
  offer_id: string
  supplier_user_id: number
  user_id: number
  instance_id: string
  status: SupplyLedgerEntryStatus
  note?: string
  created_at: string
  updated_at: string
}

export type ConnectorStatus = 'online' | 'offline' | 'revoked'
export type ConnectorTaskStatus =
  'pending' | 'leased' | 'executing' | 'succeeded' | 'failed' | 'expired'
export type ConnectorTaskType =
  | 'gateway.health.get'
  | 'gateway.accounts.list'
  | 'gateway.account.quality.probe'
  | 'gateway.binding.proof'
  | 'gateway.binding.install'
  | 'gateway.account.schedulable.set'
  | 'gateway.account.traffic_share.set'
  | 'gateway.account.switch'
  | 'gateway.scheduling.barrier'
  | 'gateway.account.create'
  | 'gateway.account.update'
  | 'gateway.account.delete'
  | 'upstream.intelligence.collect'

export interface Connector {
  connector_id: string
  user_id: number
  instance_id: string
  name?: string
  status: ConnectorStatus
  version?: string
  protocol_version: number
  gateway?: ConnectorRuntimeState
  last_seen_at?: string | null
  revoked_at?: string | null
  created_at: string
  updated_at: string
}

export interface ConnectorRuntimeState {
  protocol_version: number
  gateway_configured: boolean
  gateway_kind?: string
  gateway_status?: 'missing' | 'configured' | 'ok' | 'error' | string
  error_code?: string
  capabilities?: ConnectorTaskType[]
}

export interface ConnectorEnrollment {
  id: string
  user_id: number
  instance_id: string
  connector_id: string
  name?: string
  expires_at: string
  used_at?: string | null
  created_by?: string
  created_at: string
}

export interface CreateConnectorEnrollmentInput {
  user_id?: number
  instance_id: string
  name?: string
  connector_id?: string
  expires_in_seconds?: number
  local_config_port?: number
}

export interface CreateConnectorEnrollmentResult {
  enrollment: ConnectorEnrollment
  token: string
  core_url: string
  install_command: string
  install_guide?: ConnectorInstallGuide
}

export interface ConnectorInstallGuide {
  connector_id: string
  instance_id: string
  core_url: string
  connector_image_ref: string
  container_name: string
  data_volume_name: string
  enrollment_token_file: string
  local_config_url: string
  local_config_port: number
  warnings?: string[]
  docker_compose_yaml: string
  docker_run_command: string
}

export interface ConnectorTaskError {
  code: string
  retryable?: boolean
}

export interface ConnectorTask {
  id: string
  instance_id: string
  connector_id: string
  type: ConnectorTaskType
  schema_version: number
  risk_level: RiskLevel
  status: ConnectorTaskStatus
  error?: ConnectorTaskError
  attempts: number
  max_attempts: number
  target_channel_id?: string
  target_account_id?: string
  target_traffic_share?: number
  scheduling_fence?: {
    scope: string
    version: number
    sequence: number
  }
  available_at: string
  expires_at: string
  created_at: string
  updated_at: string
}

export type ConnectorTaskExecutionResolution =
  'confirmed_applied' | 'confirmed_not_applied' | 'connector_revoked_unverifiable'

export interface ConnectorGatewayMutationResult {
  remote_id?: string
  created?: boolean
}

export interface ConnectorGatewayTrafficShareSetResult {
  account_id: string
  weight: number
  fence: {
    scope: string
    version: number
    sequence: number
  }
}

export interface ConnectorTaskExecutionResolveInput {
  lease_nonce: string
  resolution: ConnectorTaskExecutionResolution
  evidence_note: string
  result?: ConnectorGatewayMutationResult | ConnectorGatewayTrafficShareSetResult
}

export interface RotateConnectorTokenResult {
  connector: Connector
  connector_token: string
}

export type CapabilityMode = 'read' | 'write'
export type RiskLevel = 'L0' | 'L1' | 'L2' | 'L3'
export type EventLevel = 'L0' | 'L1' | 'L2' | 'L3'

export interface AdapterCapability {
  system: InstanceKind
  name: string
  mode: CapabilityMode
  risk_level: RiskLevel
  supported: boolean
  description?: string
}

export interface OperationAudit {
  id: string
  user_id: number
  instance_id?: string
  actor_type: string
  actor_id: string
  action: string
  risk_level: RiskLevel
  event_level?: EventLevel
  target_type: string
  target_id: string
  request_payload_hash?: string
  result: string
  error_message?: string
  approval_id?: string
  workflow_run_id?: string
  details?: Record<string, string>
  created_at: string
}

export type NotificationChannel = 'qq' | 'feishu' | 'webhook'

export type NotificationSystemChannel = Exclude<NotificationChannel, 'webhook'>
export type NotificationChannelStatusValue = 'unconfigured' | 'unknown' | 'healthy' | 'failing'

export interface NotificationChannelStatus {
  channel: NotificationSystemChannel
  configured: boolean
  state: NotificationChannelStatusValue
  last_success_at?: string
  last_failure_at?: string
  last_error_code?: string
}

export type NotificationPersonalChannel = NotificationSystemChannel

export interface NotificationTarget {
  user_id: number
  channel: NotificationPersonalChannel
  scope: 'personal'
  target_ref: string
  configured: boolean
  endpoint_host?: string
  signing_secret_configured?: boolean
  access_token_configured?: boolean
  group_id_masked?: string
}

export interface UpdateNotificationTargetInput {
  user_id?: number
  webhook_url?: string
  signing_secret?: string
  clear_signing_secret?: boolean
  onebot_url?: string
  access_token?: string
  group_id?: string
}

export type NotificationDeliveryStatus =
  'pending' | 'processing' | 'retrying' | 'succeeded' | 'failed'

export interface NotificationDelivery {
  id: string
  user_id: number
  route_id: string
  route_name: string
  kind: 'event' | 'test'
  channel: NotificationChannel
  status: NotificationDeliveryStatus
  attempts: number
  max_attempts: number
  last_error_message?: string
  created_at: string
  updated_at: string
  sent_at?: string
}

export interface NotificationDeliveryFilter {
  user_id?: number
  route_id?: string
  status?: NotificationDeliveryStatus
  limit?: number
}

export interface NotificationRoute {
  id: string
  user_id: number
  name: string
  channel: NotificationChannel
  target_ref: string
  min_risk_level: RiskLevel
  min_event_level?: EventLevel
  enabled: boolean
  template?: string
  quiet_window?: string
  escalation_after?: string
  created_at: string
  updated_at: string
}

export interface Health {
  status: string
  service: string
  serverTime: string
}

export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'executed' | 'failed'

export interface ApprovalRequest {
  id: string
  user_id: number
  instance_id: string
  action: string
  risk_level: RiskLevel
  account_ids?: string[]
  schedulable?: boolean
  reason?: string
  status: ApprovalStatus
  requested_by: string
  decided_by?: string
  decided_at?: string | null
  result_note?: string
  created_at: string
  updated_at: string
}

export interface BillingLine {
  item: string
  quantity: number
  unit_price: string
  amount: string
  note?: string
}

export interface BillingStatement {
  user_id: number
  user_email?: string
  period: string
  period_start: string
  period_end: string
  instance_count: number
  disposition_count: number
  lines: BillingLine[]
  total: string
  currency: string
  generated_at: string
}

export interface GatewayAccount {
  id: string
  platform?: string
  type?: string
  status?: string
  schedulable: boolean
  priority?: number
  group_ids?: string[]
  proxy_id?: string
  display_name?: string
  balance?: number
  used_quota?: number
}

export interface AccountSwitchInput {
  disable_account_id?: string
  enable_account_id?: string
  reason?: string
}

export interface AccountHealth {
  account_id: string
  display_name?: string
  status?: string
  schedulable: boolean
  healthy: boolean
  fail_streak: number
}

export interface InstanceHealthSnapshot {
  instance_id: string
  instance_name?: string
  user_id?: number
  checked_at: string
  total_accounts: number
  healthy_count: number
  schedulable_count: number
  accounts: AccountHealth[]
  last_error?: string
  auto_switch_note?: string
}

export interface InstanceMonitorPolicy {
  instance_id: string
  enabled: boolean
  check_interval_seconds: 30 | 60 | 300
  fail_streak: 1 | 2 | 3 | 4 | 5
  auto_switch: boolean
  cooldown_seconds: 300 | 900 | 1800
  drift_detection: boolean
  updated_at?: string
}

export type UpdateInstanceMonitorPolicyInput = Pick<
  InstanceMonitorPolicy,
  | 'enabled'
  | 'check_interval_seconds'
  | 'fail_streak'
  | 'auto_switch'
  | 'cooldown_seconds'
  | 'drift_detection'
>
export type UpstreamPoolStatus = 'active' | 'maintenance' | 'retired'
export type UpstreamChannelStatus = 'active' | 'maintenance' | 'retired'
export type UpstreamInventoryState = 'draft' | 'testing' | 'ready' | 'quarantined' | 'retired'
export type GatewayAccountOwnership = 'platform_managed' | 'owner_provided'
export type QualityProbeCapability = '' | 'text_stream'
export type QualityProbeEndpointPath =
  '' | '/v1/messages' | '/v1/responses' | '/v1/chat/completions'
export type RoutePlanStatus = 'draft' | 'published' | 'suspended'
export type RolloutMode = 'immediate' | 'canary' | 'batched'
export type PublishedBindingState = 'pending' | 'active' | 'disabled' | 'failed' | 'revoked'
export type PublishedBindingVerificationStatus =
  | 'published_pending'
  | 'awaiting_first_request'
  | 'probe_verified'
  | 'passive_verified'
  | 'verification_failed'
export type QualityCircuitState = 'closed' | 'open' | 'half_open'
export type ReconcileActionType =
  'create' | 'enable' | 'disable' | 'revoke' | 'update' | 'deprovision' | 'hold' | 'noop'

export interface UpstreamPool {
  id: string
  name: string
  provider?: string
  models?: string[]
  region?: string
  status: UpstreamPoolStatus
  safety_stock_threshold: number
  description?: string
  labels?: Record<string, string>
  resource_class?: 'economy' | 'stable'
  delivery_mode?: 'supply_gateway' | 'connector_managed'
  created_at: string
  updated_at: string
}

// Customer-safe platform product catalog. Operational supplier metadata is
// intentionally absent even though administrators use the same list route.
export interface PlatformGroup {
  id: string
  name: string
  description?: string
  provider?: string
  models: string[]
  region?: string
  labels?: Record<string, string>
  status: UpstreamPoolStatus
  resource_class: 'economy' | 'stable'
}

export interface PlatformPrice {
  input_micros_per_million: number
  output_micros_per_million: number
  input_supplier_micros_per_million: number
  output_supplier_micros_per_million: number
}

export interface PlatformUpstream {
  id: string
  group_id: string
  name: string
  provider?: string
  base_url: string
  api_key_configured: boolean
  api_key_masked?: string
  models: string[]
  prices: Record<string, PlatformPrice>
  currency: string
  capacity: { max_concurrency: number; capacity_percent: number; max_request_micros: number }
  priority: number
  weight: number
  status: UpstreamChannelStatus
  enabled: boolean
  labels?: Record<string, string>
  created_at: string
  updated_at: string
}

export interface PlatformUpstreamTestResult {
  ok: boolean
  status_code?: number
  latency_ms: number
  model_count?: number
  models?: string[]
  error_code?: string
}

export interface PlatformWallet {
  user_id: number
  currency: string
  available_micros: number
  reserved_micros: number
  version: number
  updated_at: string
}

export interface PlatformApiKey {
  id: string
  user_id: number
  group_id: string
  name: string
  resource_class: 'economy' | 'stable'
  prefix: string
  key_version: number
  enabled: boolean
  models: string[]
  daily_limit_micros: number
  expires_at?: string
  last_used_at?: string
  created_at: string
  updated_at: string
}

export interface PlatformUsage {
  id: string
  request_id: string
  user_id: number
  group_id?: string
  virtual_key_id: string
  resource_class: 'economy' | 'stable'
  channel_id?: string
  model: string
  prompt_tokens: number
  completion_tokens: number
  reserved_micros: number
  settled_micros: number
  status: 'reserved' | 'settled' | 'released'
  settlement_reason?: string
  created_at: string
  completed_at?: string
}

export interface PlatformListResponse<T> {
  items: T[]
  count: number
}

export interface UpstreamChannel {
  id: string
  pool_id: string
  source_id?: string
  account_ownership: GatewayAccountOwnership
  display_name: string
  provider?: string
  models?: string[]
  probe_capability?: QualityProbeCapability
  probe_endpoint_path?: QualityProbeEndpointPath
  groups?: string[]
  credential_binding_id: string
  proxy_binding_id?: string
  priority?: number
  weight?: number
  cost_hint?: number
  status: UpstreamChannelStatus
  inventory_state: UpstreamInventoryState
  labels?: Record<string, string>
  created_at: string
  updated_at: string
}

export interface UpstreamKeyDelivery {
  id: string
  channel_id: string
  masked_value: string
  key_version: number
  proof_status: DeliveryKeyProofStatus
  proof_connector_id?: string
  proof_checked_at?: string
  created_at: string
  updated_at: string
}

export type DeliveryKeyProofStatus = 'unverified' | 'verified' | 'mismatch'

export interface AssignedUpstreamKey {
  id: string
  display_name: string
  provider?: string
  masked_value: string
  key_version: number
  proof_status: DeliveryKeyProofStatus
  proof_checked_at?: string
  allocated_at: string
}

export interface RoutePlan {
  id: string
  user_id: number
  instance_id: string
  pool_id: string
  tier?: string
  status: RoutePlanStatus
  max_channels?: number
  rollout?: RolloutMode
  rollout_batch_size?: number
  rollout_canary_count?: number
  labels?: Record<string, string>
  created_at: string
  updated_at: string
}

export interface PublishedBinding {
  id: string
  plan_id: string
  instance_id: string
  channel_id: string
  remote_id?: string
  state: PublishedBindingState
  last_error?: string
  verification_status: PublishedBindingVerificationStatus
  verification_source?: 'publish' | 'probe' | 'passive'
  verified_at?: string
  verification_error_code?: string
  created_at: string
  updated_at: string
}

export interface ReconcileAction {
  type: ReconcileActionType
  channel_id: string
  remote_id?: string
  detail?: string
}

export interface ReconcilePlan {
  instance_id: string
  plan_id: string
  dry_run: boolean
  actions: ReconcileAction[]
  created_at: string
}

export type ReconcileRunKind = 'dry_run' | 'apply' | 'rollback'
export type ReconcileRunTrigger = 'manual' | 'auto' | 'system'
export type ReconcileRunStatus = 'succeeded' | 'partial' | 'failed'

export interface ReconcileRun {
  id: string
  plan_id: string
  instance_id?: string
  user_id?: number
  kind: ReconcileRunKind
  trigger: ReconcileRunTrigger
  actor_type?: string
  actor_id?: string
  status: ReconcileRunStatus
  actions: ReconcileAction[]
  error?: string
  started_at: string
  finished_at: string
}

export type RouteStrategyType = 'stability_first' | 'cost_first' | 'latency_first' | 'balanced'
export type StrategyScope = 'plan' | 'pool' | 'user'

export interface StrategyWeights {
  success?: number
  ttft?: number
  duration?: number
  stability?: number
  cost?: number
}

export interface StrategyThresholds {
  min_samples?: number
  target_success_rate?: number
  floor_success_rate?: number
  max_ttft_p95_ms?: number
  max_duration_p95_ms?: number
  consecutive_failure_limit?: number
  eject_score?: number
}

export interface PenaltyBreakdown {
  error_rate: number
  error_penalty: number
  ttft_penalty: number
  duration_penalty: number
  total_penalty: number
}

export interface RouteStrategy {
  id?: string
  name?: string
  type: RouteStrategyType
  thresholds?: StrategyThresholds
  weights?: StrategyWeights
  auto_apply: boolean
  approval_required?: boolean
  cooldown_seconds?: number
  recovery_observation_seconds?: number
  max_auto_switches_per_hour?: number
  scope?: StrategyScope
  plan_id?: string
  pool_id?: string
  user_id?: number
  created_at?: string
  updated_at?: string
}

export type HealthState =
  'unknown' | 'healthy' | 'degraded' | 'unhealthy' | 'quarantined' | 'recovering'
export type AutoSwitchStatus =
  | 'proposed'
  | 'approved'
  | 'rejected'
  | 'skipped'
  | 'applying'
  | 'observing'
  | 'completed'
  | 'rolled_back'
  | 'failed'

export interface AutoSwitchDecision {
  id: string
  user_id?: number
  plan_id: string
  instance_id?: string
  pool_id?: string
  strategy: RouteStrategyType
  trigger: ReconcileRunTrigger
  trigger_reason?: string
  from_channel_id?: string
  to_channel_id?: string
  risk_level: RiskLevel
  risk_reason?: string
  status: AutoSwitchStatus
  auto_applied: boolean
  fingerprint?: string
  dry_run_result: ReconcilePlan
  error?: string
  observation_note?: string
  created_at: string
  updated_at: string
  applied_at?: string | null
  observe_until?: string | null
  resolved_at?: string | null
  lease_until?: string | null
  lease_version?: number
}

export interface AutoSwitchChannelHealth {
  channel_id: string
  display_name?: string
  status: UpstreamChannelStatus
  live: boolean
  binding_state?: PublishedBindingState
  sample_count: number
  model?: string
  success_rate: number
  upstream_error_rate: number
  ttft_p95: number
  duration_p95: number
  quality_score: number
  health_score: number
  eject_score: number
  quality_below_threshold: boolean
  bad_windows: number
  cohort_percentage: number
  cohort_known: boolean
  cohort_member: boolean
  ejected: boolean
  hard_failure: boolean
  penalties: PenaltyBreakdown
  health_state: HealthState
  circuit_state?: QualityCircuitState
  probe_after?: string | null
  last_probe_at?: string | null
  consecutive_probe_successes: number
  last_score?: number | null
  last_reason?: {
    code?: string
    text?: string
  }
  restore_pending?: boolean
  recovery_ready?: boolean
  recovery_stage?: number
  recovery_stage_started_at?: string | null
  recovery_observe_after?: string | null
  evidence_updated_at?: string | null
  evidence_fresh?: boolean
  evidence_confidence?: number
}

export interface AutoSwitchSummary {
  plan_id: string
  instance_id: string
  pool_id: string
  user_id: number
  strategy: RouteStrategyType
  strategy_source?: StrategyScope
  auto_apply: boolean
  active_decision?: AutoSwitchDecision
  recent_decisions: AutoSwitchDecision[]
  channels: AutoSwitchChannelHealth[]
}

export interface OwnerPoolHealth {
  capacity: {
    published: number
    schedulable: number
    isolated: number
    awaiting_verification: number
    verification_failed: number
  }
  sla: {
    window: '5m'
    success_rate: number | null
    ttft_p95_ms: number | null
    duration_p95_ms: number | null
    sample_count: number
    updated_at: string | null
  }
  incidents: OwnerPoolIncident[]
  switches: OwnerPoolSwitchResult[]
  generated_at: string
}

export interface OperationsCenterSummary {
  published_plans: number
  managed_bindings: number
  schedulable_bindings: number
  isolated_bindings: number
  recovering_bindings: number
  unknown_bindings: number
  fresh_evidence_percent: number
  open_incidents: number
  manual_recovery: number
  onboarding_pending: number
  onboarding_retryable: number
  onboarding_active: number
  onboarding_dormant: number
}

export type OnboardingStage =
  | 'waiting_connector'
  | 'checking_gateway'
  | 'assigning_keys'
  | 'delivering_bindings'
  | 'publishing'
  | 'verifying'
  | 'active'
  | 'failed_retryable'
  | 'dormant'

export type OnboardingStatus = 'pending' | 'running' | 'retryable' | 'active' | 'dormant'

export interface OperationsOnboarding {
  id: string
  user_id: number
  instance_id: string
  pool_id: string
  connector_id?: string
  plan_id?: string
  stage: OnboardingStage
  status: OnboardingStatus
  attempts: number
  delivered_keys: number
  last_error_code?: string
  desired_generation: number
  last_ready_generation: number
  last_ready_at?: string | null
  next_attempt_at?: string | null
  updated_at: string
}

export interface OperationsSourceHealth {
  source_id: string
  display_name: string
  models?: string[]
  total_bindings: number
  schedulable: number
  isolated: number
  recovering: number
  unknown: number
  passive_requests_5m: number
  worst_quality_score?: number | null
  evidence_updated_at?: string | null
  evidence_fresh: boolean
  evidence_confidence: number
  health_state: HealthState
}

export interface OperationsIncident {
  plan_id: string
  instance_id: string
  user_id: number
  channel_id: string
  source_id: string
  display_name: string
  status: 'isolated' | 'probing' | 'recovering' | 'needs_ejection' | 'delivery_failed'
  binding_state: PublishedBindingState
  circuit_state?: QualityCircuitState
  quality_score?: number | null
  penalty: PenaltyBreakdown
  evidence_updated_at?: string | null
  evidence_fresh: boolean
  evidence_confidence: number
  opened_at?: string | null
  next_probe_at?: string | null
  last_probe_at?: string | null
  successful_probes: number
  recovery_stage?: number
  recovery_observe_after?: string | null
  reason?: { code?: string; text?: string }
  connector_recovery_mode: 'automatic' | 'manual'
  connector_last_seen_at?: string | null
  affected_downstreams: number
  affected_requests_5m: number
  current_routes: Array<{ source_id: string; display_name: string }>
  ejection_cohort_percent?: number
  ejection_cohort_role?: 'selected' | 'holdout' | 'unknown'
  recovery_cohort_role?: 'canary' | 'holdout'
}

export interface OperationsTimelineItem {
  id: string
  kind: 'decision' | 'gateway_receipt' | 'connector_task' | 'onboarding_workflow'
  status: string
  plan_id?: string
  instance_id?: string
  user_id?: number
  title: string
  detail?: string
  at: string
}

export interface OperationsCenter {
  generated_at: string
  summary: OperationsCenterSummary
  sources: OperationsSourceHealth[]
  incidents: OperationsIncident[]
  onboarding: OperationsOnboarding[]
  timeline: OperationsTimelineItem[]
}

export type OwnerPoolIncidentStatus =
  'needs_ejection' | 'isolated' | 'recovering' | 'delivery_failed'

export interface OwnerPoolIncident {
  status: OwnerPoolIncidentStatus
  success_rate: number | null
  ttft_p95_ms: number | null
  duration_p95_ms: number | null
  sample_count: number
  detected_at?: string | null
  updated_at?: string | null
  recovery?: {
    successful_probes: number
    required_probes: number
    next_probe_at?: string | null
    last_probe_at?: string | null
    rollout_stage?: number
    observe_after?: string | null
  }
}

export type OwnerPoolSwitchOutcome =
  'pending' | 'in_progress' | 'succeeded' | 'skipped' | 'rolled_back' | 'failed'

export interface OwnerPoolSwitchResult {
  result: OwnerPoolSwitchOutcome
  started_at: string
  finished_at?: string | null
}

export type OwnerConnectorReadiness =
  'missing' | 'offline' | 'setup_required' | 'update_required' | 'ready'

export type OwnerOnboardingServiceState =
  | 'awaiting_connector'
  | 'connector_offline'
  | 'gateway_setup_required'
  | 'connector_update_required'
  | 'waiting_platform'
  | 'provisioning'
  | 'awaiting_verification'
  | 'verification_failed'
  | 'retrying'
  | 'paused'
  | 'degraded'
  | 'active'

export interface OwnerOnboardingInstance {
  instance_id: string
  instance_name: string
  instance_kind: InstanceKind
  connector_state: OwnerConnectorReadiness
  connector_last_seen_at?: string | null
  service_state: OwnerOnboardingServiceState
  stage?: OnboardingStage
  status?: OnboardingStatus
  workflow_count: number
  ready_workflows: number
  delivered_keys: number
  verified_keys: number
  published_bindings: number
  active_bindings: number
  callable_bindings: number
  awaiting_verification_bindings: number
  verification_failed_bindings: number
  blocker_code?: string
  next_attempt_at?: string | null
  updated_at: string
}

export interface OwnerOnboarding {
  generated_at: string
  summary: {
    total_instances: number
    connector_ready: number
    active_instances: number
    action_required: number
    delivered_keys: number
    verified_keys: number
    published_bindings: number
    active_bindings: number
    callable_bindings: number
    awaiting_verification_bindings: number
    verification_failed_bindings: number
  }
  instances: OwnerOnboardingInstance[]
}
