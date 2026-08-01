import { describe, expect, it } from 'vitest'
import {
  auditActorLabel,
  auditActorTypeLabel,
  auditActionLabel,
  auditActivityDescription,
  auditActivityLabel,
  auditResultLabel,
  auditTargetTypeLabel,
  capabilityDescriptionLabel,
  capabilityModeLabel,
  capabilityNameLabel,
  connectorErrorLabel,
  connectorTaskStatusLabel,
  connectorTaskTypeLabel,
  onboardingErrorLabel,
  onboardingStageLabel,
  operationsStatusLabel,
  operationsTimelineKindLabel,
  operationsReasonLabel,
  reconcileDetailLabel,
  reconcileRunLabel,
  setLocale,
  t,
} from '.'

const auditActions = [
  'account.balance_low',
  'account.deprovision',
  'account.disable_schedulable',
  'account.enable_schedulable',
  'account.provision',
  'account.update',
  'approval.approve',
  'approval.execute',
  'approval.reject',
  'approval.submit',
  'auth.login',
  'auth.register',
  'auth.settings.update',
  'auto_switch.evaluate',
  'connector.enroll',
  'connector.enrollment.create',
  'connector.revoke',
  'connector.token.rotate',
  'connector_task.complete',
  'connector_task.complete.gateway.account.create',
  'connector_task.complete.gateway.account.delete',
  'connector_task.complete.gateway.account.quality.probe',
  'connector_task.complete.gateway.account.schedulable.set',
  'connector_task.complete.gateway.account.switch',
  'connector_task.complete.gateway.account.update',
  'connector_task.complete.gateway.accounts.list',
  'connector_task.complete.gateway.binding.install',
  'connector_task.complete.gateway.binding.proof',
  'connector_task.complete.gateway.health.get',
  'connector_task.complete.gateway.scheduling.barrier',
  'connector_task.create',
  'core.boot',
  'instance.config_drift',
  'instance.connector.bind',
  'instance.connector.unbind',
  'instance.create',
  'instance.monitor_policy.update',
  'instance.update',
  'notification_route.create',
  'notification_route.delete',
  'notification_route.update',
  'onboarding.workflow',
  'onboarding.workflow.active',
  'onboarding.workflow.completed',
  'onboarding.workflow.verified',
  'onboarding.workflow.reconfigured',
  'onboarding.workflow.repaired',
  'onboarding.workflow.failed',
  'onboarding.workflow.paused',
  'onboarding.workflow.progress',
  'onboarding.workflow.retry_scheduled',
  'payment.config.update',
  'payment.provider.create',
  'payment.provider.delete',
  'payment.provider.update',
  'payment.order.cancel',
  'platform_group.create',
  'platform_group.update',
  'platform_key.create',
  'platform_key.view',
  'platform_key.update',
  'platform_key.delete',
  'platform_upstream.create',
  'platform_upstream.update',
  'platform_wallet.adjust',
  'route_plan.create',
  'route_plan.reconcile_apply',
  'route_plan.reconcile_dryrun',
  'route_plan.rollback',
  'route_plan.update',
  'route_strategy.delete',
  'route_strategy.upsert',
  'secret.delete',
  'secret.upsert',
  'supply.allocate',
  'supply.create',
  'supply.revoke',
  'supply.update',
  'supply_offer.revoke',
  'upstream_channel.create',
  'upstream_channel.update',
  'upstream_key.reveal',
  'upstream_key_delivery.upsert',
  'upstream_pool.create',
  'upstream_pool.update',
  'user.create',
  'user.password_reset',
  'user.update',
] as const

const auditActorTypes = ['bot', 'connector', 'operator', 'system', 'user', 'workflow'] as const

const auditTargetTypes = [
  'account',
  'approval',
  'assigned_upstream_key',
  'auth_settings',
  'connector',
  'connector_enrollment',
  'connector_task',
  'instance',
  'notification_route',
  'onboarding_workflow',
  'payment_config',
  'payment_provider',
  'payment_order',
  'virtual_key',
  'wallet_journal',
  'route_plan',
  'route_strategy',
  'secret',
  'service',
  'supply_ledger',
  'supply_offer',
  'upstream_channel',
  'upstream_key_delivery',
  'upstream_pool',
  'user',
] as const

const connectorTaskTypes = [
  'gateway.health.get',
  'gateway.accounts.list',
  'gateway.account.quality.probe',
  'gateway.binding.proof',
  'gateway.binding.install',
  'gateway.account.schedulable.set',
  'gateway.account.switch',
  'gateway.scheduling.barrier',
  'gateway.account.create',
  'gateway.account.update',
  'gateway.account.delete',
] as const

const connectorErrorCodes = [
  'binding_not_found',
  'binding_version_stale',
  'connector_mismatch',
  'connector_task_failed',
  'expired',
  'gateway_auth_failed',
  'gateway_auth_unsupported',
  'gateway_config_unavailable',
  'gateway_kind_unsupported',
  'gateway_not_configured',
  'gateway_redirect_rejected',
  'gateway_rejected',
  'gateway_request_failed',
  'gateway_resource_not_found',
  'gateway_response_invalid',
  'gateway_response_too_large',
  'gateway_test_failed',
  'gateway_timeout',
  'gateway_unavailable',
  'gateway_unreachable',
  'idempotency_conflict',
  'instance_mismatch',
  'invalid_account_id',
  'invalid_gateway_request',
  'invalid_task_input',
  'max_attempts_exceeded',
  'ownership_violation',
  'quality_probe_disabled',
  'quality_probe_rate_limited',
  'quality_probe_scope_unsupported',
  'result_encoding_failed',
  'scheduling_fence_conflict',
  'scheduling_fence_stale',
  'schema_version_unsupported',
  'task_type_unsupported',
] as const

const capabilityNames = [
  'list_accounts',
  'set_account_schedulable',
  'switch_upstream',
  'create_account',
  'update_account',
  'delete_account',
] as const

const capabilityDescriptions = [
  'list gateway accounts',
  'change account scheduling state',
  'switch scheduled upstream account',
  'create a platform-managed gateway account',
  'update a gateway account',
  'delete a platform-managed gateway account after drain',
] as const

const onboardingErrors = [
  'binding_delivery_failed',
  'binding_delivery_invalid',
  'binding_proof_failed',
  'binding_receipt_unavailable',
  'connector_capability_missing',
  'connector_gateway_not_ready',
  'connector_unavailable',
  'delivery_receipt_unverified',
  'gateway_binding_not_active',
  'gateway_unavailable',
  'gateway_verification_failed',
  'instance_unavailable',
  'key_assignment_failed',
  'key_capacity_unavailable',
  'key_catalog_unavailable',
  'onboarding_failed',
  'pool_inactive',
  'pool_unavailable',
  'publish_failed',
  'publish_not_active',
  'route_plan_create_conflict',
  'route_plan_create_failed',
  'route_plan_store_failed',
  'route_plan_suspended',
  'user_ineligible',
] as const

const onboardingStages = [
  'waiting_connector',
  'checking_gateway',
  'assigning_keys',
  'delivering_bindings',
  'publishing',
  'verifying',
  'active',
  'failed_retryable',
  'dormant',
] as const

const operationsStatuses = [
  'active',
  'applying',
  'completed',
  'dormant',
  'expired',
  'failed',
  'leased',
  'observing',
  'partial',
  'pending',
  'proposed',
  'retryable',
  'rolled_back',
  'running',
  'skipped',
  'succeeded',
] as const

const operationsReasonCodes = [
  'circuit_cooldown',
  'circuit_half_open',
  'circuit_healthy',
  'circuit_opened',
  'circuit_probe_failed',
  'circuit_probe_required',
  'circuit_probe_succeeded',
  'circuit_restored',
  'decision_fallback',
  'expired_observation_repaired',
  'gate_auth',
  'gate_balance',
  'gate_maintenance',
  'gate_penalty_threshold',
  'gate_quarantined',
  'gate_recovering',
  'gate_retired',
  'penalty_threshold',
  'probe_passed',
  'probe_claimed',
  'probe_execution_failed',
  'probe_platform_error',
  'probe_scope_unavailable',
  'probe_unsupported',
  'quality_below_threshold',
  'recovery_canary_admitted',
  'recovery_probe_failed',
  'recovery_ready',
  'recovery_regressed',
  'recovery_restore_pending',
  'recovery_stage_expanded',
  'replacement_apply_failed',
  'replacement_observation_failed',
  'restore_apply_failed',
  'restore_pending',
  'restore_scope_unavailable',
] as const

function expectTranslated(values: readonly string[], label: (value: string) => string) {
  for (const value of values) expect(label(value), value).not.toBe(value)
}

describe('audit i18n labels', () => {
  it('renders Chinese business labels for audit keys', () => {
    setLocale('zh')

    expect(auditActionLabel('auth.login')).toBe('登录')
    expect(auditActionLabel('auto_switch.evaluate')).toBe('自动切换评估')
    expect(auditActionLabel('supply.allocate')).toBe('分配供给')
    expect(auditActionLabel('supply.update')).toBe('更新供给')
    expect(auditActionLabel('supply_offer.revoke')).toBe('撤销供给')
    expect(auditActionLabel('user.update')).toBe('更新用户')
    expect(auditActionLabel('user.password_reset')).toBe('重置用户密码')
    expect(auditActionLabel('route_strategy.upsert')).toBe('保存自动切换策略')
    expect(auditActionLabel('instance.monitor_policy.update')).toBe('更新实例监控设置')
    expect(auditActionLabel('onboarding.workflow')).toBe('接入流程')
    expect(auditResultLabel('accepted')).toBe('已完成')
    expect(auditResultLabel('rejected')).toBe('已拒绝')
    expect(auditActorLabel('system', 'health-checker')).toBe('系统：健康检查器')
    expect(auditTargetTypeLabel('upstream_key_delivery')).toBe('Key 交付配置')
    expect(auditTargetTypeLabel('notification_route')).toBe('通知设置')
    expect(auditActionLabel('notification_route.create')).toBe('添加通知设置')
    expect(auditActionLabel('platform_key.view')).toBe('查看平台 API Key')
    expect(auditActivityLabel('platform_key.view', 'accepted')).toBe('平台 API Key 已查看')
  })

  it('renders complete Chinese activity sentences instead of generic result suffixes', () => {
    setLocale('zh')

    expect(auditActivityLabel('connector_task.complete', 'accepted')).toBe(
      '后台操作已完成（旧记录未保留具体操作项）',
    )
    expect(
      auditActivityDescription(
        'connector_task.complete.gateway.accounts.list',
        'accepted',
        '',
        '实例 A',
        'L0',
        { account_count: '3' },
      ),
    ).toBe('已读取实例「实例 A」中的 3 个上游账号')
    expect(auditActivityLabel('connector_task.complete.gateway.binding.install', 'retrying')).toBe(
      '上游密钥暂未下发，系统将自动重试',
    )
    expect(
      auditActivityDescription(
        'connector_task.complete.gateway.health.get',
        'failed',
        'gateway_timeout',
        '实例 A',
        'L0',
      ),
    ).toBe('无法检查实例「实例 A」的连接状态；原因：网关请求超时')
    expect(
      auditActivityDescription('connector_task.complete', 'accepted', '', '实例 A', 'L0'),
    ).toBe('实例「实例 A」：后台检查已完成，无需处理（旧记录未保留具体检查项）')
    expect(auditActivityLabel('onboarding.workflow.progress', 'accepted')).toBe(
      '正在准备上游账号和密钥',
    )
    expect(
      auditActivityDescription('onboarding.workflow.completed', 'accepted', '', '实例 A', 'L2', {
        pool_name: '稳定池',
      }),
    ).toBe('上游池「稳定池」已接入实例「实例 A」；账号、密钥和流量调度均已核验通过')
    expect(
      auditActivityDescription(
        'onboarding.workflow.retry_scheduled',
        'retrying',
        'connector_unavailable',
        '实例 A',
        'L2',
        {
          pool_name: '稳定池',
          attempts: '2',
          next_attempt_at: '2026-07-20T10:00:00Z',
        },
      ),
    ).toContain('上游池「稳定池」暂未接入实例「实例 A」；原因：连接器不可用')
    expect(
      auditActivityDescription(
        'onboarding.workflow.retry_scheduled',
        'retrying',
        '',
        '实例 A',
        'L2',
        {
          pool_name: '稳定池',
          attempts: '2',
          next_attempt_at: '2026-07-20T10:00:00Z',
          reason_code: 'connector_unavailable',
        },
      ),
    ).toContain('上游池「稳定池」暂未接入实例「实例 A」；原因：连接器不可用')
    expect(
      auditActivityDescription(
        'connector_task.complete.gateway.account.switch',
        'accepted',
        '',
        '实例 A',
        'L1',
        { from_account_id: 'opaque-secret-id', to_account_id: 'opaque-secret-id-2' },
      ),
    ).not.toContain('opaque-secret-id')
    expect(auditActivityLabel('onboarding.workflow.paused', 'accepted')).toBe(
      '当前上游资源已停用，接入已暂停',
    )
    expect(auditActivityLabel('onboarding.workflow', 'accepted', 'pool_inactive')).toBe(
      '当前上游资源已停用，接入已暂停',
    )
    expect(auditActivityLabel('route_plan.reconcile_apply', 'rejected')).toBe(
      '配置未发布：实例不支持所需操作',
    )
    expect(t('audit.eventLevels.L0.label')).toBe('L0 INFO')
    expect(t('audit.eventLevels.L2.label')).toBe('L2 WARNING')
  })

  it('renders English labels when locale is switched', () => {
    setLocale('en')

    expect(auditActionLabel('auth.login')).toBe('Login')
    expect(auditActionLabel('route_plan.reconcile_apply')).toBe('Apply route plan')
    expect(auditActionLabel('platform_key.view')).toBe('View platform API key')
    expect(auditActionLabel('instance.monitor_policy.update')).toBe(
      'Update instance monitoring settings',
    )
    expect(auditActionLabel('onboarding.workflow')).toBe('Onboarding workflow')
    expect(auditResultLabel('accepted')).toBe('Completed')
    expect(auditActivityLabel('connector_task.complete', 'accepted')).toBe(
      'Background operation completed (older record without the operation type)',
    )
    expect(
      auditActivityDescription('onboarding.workflow.verified', 'verified', '', 'Instance A', 'L2', {
        pool_name: 'Stable Pool',
      }),
    ).toBe('Upstream pool "Stable Pool" configuration on instance "Instance A" passed verification')
    expect(t('audit.eventLevels.L2.label')).toBe('L2 WARNING')
  })

  it('does not expose an unknown internal action key in customer-facing text', () => {
    setLocale('zh')

    expect(auditActionLabel('unknown.module_action')).toBe('系统操作')
    expect(auditResultLabel('unknown_result')).toBe('unknown_result')
    expect(auditActivityLabel('unknown.module_action', 'failed')).toBe('系统操作失败')
  })

  it('maps every audit action currently emitted by Core', () => {
    setLocale('zh')
    expectTranslated(auditActions, auditActionLabel)
    expectTranslated(auditActorTypes, auditActorTypeLabel)
    expectTranslated(auditTargetTypes, auditTargetTypeLabel)
    expectTranslated(
      [
        'accepted',
        'detected',
        'failed',
        'paused',
        'rejected',
        'retrying',
        'running',
        'success',
        'verified',
      ],
      auditResultLabel,
    )
  })

  it('has a dedicated recent-activity sentence for every supported audit action', () => {
    setLocale('zh')
    for (const action of auditActions) {
      const result =
        action === 'onboarding.workflow.retry_scheduled' || action === 'onboarding.workflow.failed'
          ? 'retrying'
          : action === 'onboarding.workflow.progress'
            ? 'running'
            : action === 'onboarding.workflow.verified'
              ? 'verified'
              : action === 'onboarding.workflow.paused'
                ? 'paused'
                : 'accepted'
      const generic =
        result === 'retrying'
          ? `${auditActionLabel(action)}失败`
          : `${auditActionLabel(action)}已完成`
      expect(auditActivityLabel(action, result), action).not.toBe(generic)
    }
  })
})

describe('connector task i18n labels', () => {
  it('renders Chinese business labels for dotted protocol values', () => {
    setLocale('zh')

    expect(connectorTaskTypeLabel('gateway.accounts.list')).toBe('读取网关账号')
    expect(connectorTaskTypeLabel('gateway.health.get')).toBe('检查网关健康')
    expect(connectorTaskTypeLabel('gateway.account.schedulable.set')).toBe('设置账号调度状态')
    expect(connectorTaskTypeLabel('gateway.account.switch')).toBe('切换网关账号')
    expect(connectorTaskTypeLabel('gateway.binding.install')).toBe('安装本地凭证绑定')
    expect(connectorErrorLabel('scheduling_fence_stale')).toBe('调度任务已被新版本取代')
    expect(connectorTaskStatusLabel('executing')).toBe('执行结果待确认')
  })

  it('renders English business labels when locale is switched', () => {
    setLocale('en')

    expect(connectorTaskTypeLabel('gateway.accounts.list')).toBe('Read gateway accounts')
    expect(connectorTaskStatusLabel('executing')).toBe('Outcome uncertain')
  })

  it('falls back to the protocol value when no translation exists', () => {
    setLocale('zh')

    expect(connectorTaskTypeLabel('gateway.unknown.action')).toBe('gateway.unknown.action')
  })

  it('maps every Connector task type in the protocol allowlist', () => {
    setLocale('zh')
    expectTranslated(connectorTaskTypes, connectorTaskTypeLabel)
    expectTranslated(connectorErrorCodes, connectorErrorLabel)
  })
})

describe('capability i18n labels', () => {
  it('renders Chinese labels for gateway capabilities', () => {
    setLocale('zh')

    expect(capabilityNameLabel('list_accounts')).toBe('读取网关账号')
    expect(capabilityModeLabel('write')).toBe('写入')
    expect(capabilityDescriptionLabel('list gateway accounts')).toBe('读取网关账号列表')
    expect(capabilityNameLabel('create_account')).toBe('创建网关账号')
    expect(t('capabilities.risks.L0.label')).toBe('L0 只读')
  })

  it('falls back to raw values when no translation exists', () => {
    setLocale('zh')

    expect(capabilityNameLabel('custom_capability')).toBe('custom_capability')
    expect(capabilityDescriptionLabel('custom description')).toBe('custom description')
  })

  it('renders English risk labels when locale is switched', () => {
    setLocale('en')

    expect(t('capabilities.risks.L0.label')).toBe('L0 Read only')
  })

  it('maps every executable gateway capability', () => {
    setLocale('zh')
    expectTranslated(capabilityNames, capabilityNameLabel)
    expectTranslated(capabilityDescriptions, capabilityDescriptionLabel)
  })
})

describe('protocol-valued UI labels', () => {
  it('resolves translation object keys that contain dots', () => {
    setLocale('zh')
    expect(t('poolHealth.events.upstream.auto_switch', 'upstream.auto_switch')).toBe('自动切换')
    expect(t('poolHealth.events.instance.config_drift', 'instance.config_drift')).toBe('配置漂移')
  })

  it('maps every payment method exposed by the order filters', () => {
    setLocale('zh')
    expectTranslated(
      [
        'alipay',
        'wxpay',
        'alipay_direct',
        'wxpay_direct',
        'stripe',
        'airwallex',
        'easypay',
        'card',
        'link',
      ],
      (method) => t(`payment.methods.${method}`, method),
    )
  })

  it('localizes operations-center timeline values', () => {
    setLocale('zh')
    expect(operationsTimelineKindLabel('connector_task')).toBe('连接器任务')
    expect(operationsStatusLabel('succeeded')).toBe('成功')
    expect(onboardingStageLabel('waiting_connector')).toBe('等待连接器')
    expect(onboardingErrorLabel('connector_gateway_not_ready')).toBe('连接器网关尚未就绪')
    expect(operationsReasonLabel('probe_unsupported')).toBe('当前连接器不支持主动恢复探测')
    expect(
      operationsReasonLabel('recovery_restore_pending', 'selected for 25% guarded recovery stage'),
    ).toBe('已选入 25% 灰度恢复批次')
    expect(reconcileRunLabel('dry_run', 'system')).toBe('预检 · 系统')
    expect(reconcileDetailLabel('provision managed channel onto gateway')).toBe(
      '在网关创建平台托管账号',
    )
    expect(reconcileDetailLabel('held by rollout policy (canary); pending action: create')).toBe(
      '受灰度发布策略限制，待执行：创建',
    )
  })

  it('maps every operations-center protocol value in both locales', () => {
    for (const locale of ['zh', 'en'] as const) {
      setLocale(locale)
      expectTranslated(
        ['connector_task', 'decision', 'gateway_receipt', 'onboarding_workflow'],
        operationsTimelineKindLabel,
      )
      expectTranslated(operationsStatuses, operationsStatusLabel)
      expectTranslated(onboardingStages, onboardingStageLabel)
      expectTranslated(onboardingErrors, onboardingErrorLabel)
      expectTranslated(operationsReasonCodes, operationsReasonLabel)
      expect(reconcileRunLabel('apply', 'manual')).not.toContain('apply')
    }
    setLocale('zh')
  })
})

describe('health summary i18n labels', () => {
  it('renders Chinese and English health overview labels', () => {
    setLocale('zh')

    expect(t('poolHealth.summary.refresh')).toBe('刷新')
    expect(t('poolHealth.summary.columns.instance')).toBe('实例')
    expect(t('poolHealth.summary.noDataAlert')).toBe('尚无账号健康数据，当前无法判断整体健康状态。')

    setLocale('en')

    expect(t('poolHealth.summary.refresh')).toBe('Refresh')
    expect(t('poolHealth.summary.columns.instance')).toBe('Instance')
    expect(t('poolHealth.summary.noDataAlert')).toBe(
      'No account health data is available, so overall health cannot be determined.',
    )
    expect(t('poolHealth.owner.isolated')).toBe('Quality isolated')
    expect(t('poolHealth.owner.incident.recovering')).toBe('Recovery probing')
    expect(t('poolHealth.summary.unhealthyAlert', undefined, { count: 2 })).toBe(
      '2 unhealthy accounts found. Open the instance accounts page to disable or switch them.',
    )
  })
})
