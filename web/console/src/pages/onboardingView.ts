import type {
  OnboardingStage,
  OwnerOnboardingInstance,
  OwnerOnboardingServiceState,
} from '../api/types'

export type OnboardingTone = 'success' | 'processing' | 'warning' | 'error' | 'default'

export function onboardingProgress(item: OwnerOnboardingInstance): number {
  if (item.service_state === 'active') return 100
  if (item.service_state === 'awaiting_verification') return 95
  if (item.service_state === 'verification_failed') return 92
  if (item.service_state === 'degraded') return 90
  if (item.connector_state !== 'ready') return item.connector_state === 'missing' ? 15 : 30
  const stage: Record<OnboardingStage, number> = {
    waiting_connector: 30,
    checking_gateway: 40,
    assigning_keys: 55,
    delivering_bindings: 70,
    publishing: 82,
    verifying: 92,
    active: 100,
    failed_retryable: 70,
    dormant: 50,
  }
  return item.stage ? stage[item.stage] : 40
}

export function onboardingTone(state: OwnerOnboardingServiceState): OnboardingTone {
  if (state === 'active') return 'success'
  if (
    state === 'retrying' ||
    state === 'paused' ||
    state === 'degraded' ||
    state === 'awaiting_verification'
  )
    return 'warning'
  if (state === 'connector_offline' || state === 'verification_failed') return 'error'
  if (state === 'provisioning') return 'processing'
  return 'default'
}

export function onboardingNeedsUserAction(state: OwnerOnboardingServiceState): boolean {
  return [
    'awaiting_connector',
    'connector_offline',
    'gateway_setup_required',
    'connector_update_required',
  ].includes(state)
}

export function onboardingStateCopy(state: OwnerOnboardingServiceState) {
  const copy: Record<OwnerOnboardingServiceState, { label: string; detail: string }> = {
    awaiting_connector: {
      label: '等待安装 Connector',
      detail: '安装并启动 Connector 后，平台会自动继续。',
    },
    connector_offline: {
      label: 'Connector 离线',
      detail: '请检查 Connector 进程和到 Core 的网络连接。',
    },
    gateway_setup_required: {
      label: '需要完成本地网关配置',
      detail: '在 Connector 本地页面填写网关地址和管理凭证，并通过连接测试。',
    },
    connector_update_required: {
      label: '需要更新 Connector',
      detail: '当前 Connector 协议或能力不足，请按安装页更新后重启。',
    },
    waiting_platform: {
      label: '等待平台准备资源',
      detail: 'Connector 已就绪；平台会在资源可用后自动开始交付。',
    },
    provisioning: {
      label: '正在自动交付',
      detail: '平台正在分配资源、安装绑定并发布服务，无需手工操作。',
    },
    awaiting_verification: {
      label: '已部署，等待调用验证',
      detail: '资源已部署，正在等待首个真实请求或主动探测；验证前不会显示为可用线路。',
    },
    verification_failed: {
      label: '调用验证失败',
      detail: '资源已部署，但真实请求或主动探测未通过，当前不会作为可用线路。',
    },
    retrying: { label: '自动重试中', detail: '交付暂未完成，系统会按退避策略自动重试。' },
    paused: { label: '平台服务已暂停', detail: '相关托管资源当前不可用，请联系平台管理员。' },
    degraded: {
      label: '服务验证未完全通过',
      detail: '资源已发布但验证尚未完整通过，请联系平台管理员。',
    },
    active: { label: '托管服务已启用', detail: '资源交付、发布及真实调用验证均已完成。' },
  }
  return copy[state]
}
