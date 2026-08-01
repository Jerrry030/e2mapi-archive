import { ApiError } from './client'
import { getLocale } from '../i18n'

function shouldUseChineseErrors(): boolean {
  return getLocale() === 'zh'
}

function errorText(message: unknown): string {
  if (message === undefined || message === null) return ''
  if (typeof message === 'string') return message
  if (message instanceof Error) return message.message
  if (typeof message === 'number' || typeof message === 'boolean' || typeof message === 'bigint') {
    return String(message)
  }
  if (Array.isArray(message)) {
    return message.map(errorText).filter(Boolean).join('; ')
  }
  if (typeof message === 'object') {
    const record = message as Record<string, unknown>
    for (const key of ['message', 'error', 'detail', 'reason', 'cause']) {
      const text = errorText(record[key])
      if (text) return text
    }
    try {
      return JSON.stringify(message)
    } catch {
      return String(message)
    }
  }
  return String(message)
}

function normalizeErrorText(message: unknown): string {
  let text = errorText(message).trim()
  if (!text) return ''

  text = text.replace(
    /adapters: instance\s+\S+\s+has no connector; install a connector and configure the gateway credential locally/i,
    '该实例还没有可用连接器。请先安装并上线连接器，再在连接器运行环境中配置网关凭证。',
  )
  text = text.replace(
    /connector install token already exists; wait for it to expire or use the connector page/i,
    '连接器安装令牌已存在，请等待其过期，或前往连接器页面重新处理。',
  )
  text = text.replace(
    /gateway credentials must be configured locally in the connector, not sent to core/i,
    '网关凭证需要配置在本地连接器中，不能上传到主控台。',
  )
  text = text.replace(
    /gateway admin credentials must be configured locally in the connector/i,
    '网关管理凭证需要配置在本地连接器中。',
  )
  text = text.replace(/invalid connector token/i, '连接器令牌无效。')
  text = text.replace(/connector token is required/i, '缺少连接器令牌。')
  text = text.replace(
    /connector token does not match connector_id/i,
    '连接器令牌与连接器 ID 不匹配。',
  )
  text = text.replace(/connector is revoked/i, '连接器已被吊销。')
  text = text.replace(
    /(?:connector gateway:\s*)?instance(?:\s+\S+)?\s+has no connector_id/i,
    '该实例尚未绑定连接器。',
  )
  text = text.replace(/instance not found/i, '实例不存在。')
  text = text.replace(/connector not found/i, '连接器不存在。')
  text = text.replace(/route plan not found/i, '发布计划不存在。')
  text = text.replace(/auto-switch controller is not enabled/i, '自动切换控制器未启用。')
  text = text.replace(/publish engine not configured/i, '发布引擎未配置。')
  text = text.replace(/billing calculator not configured/i, '计费计算器未配置。')
  text = text.replace(/credential vault is not configured/i, '凭证保险箱未配置。')
  text = text.replace(/login required/i, '请先登录。')
  text = text.replace(/user out of scope/i, '当前账号无权访问该用户的数据。')
  text = text.replace(/owner role required(?: for this user)?/i, '需要托管账号权限。')
  text = text.replace(/supplier role required(?: for this user)?/i, '需要供给账号权限。')
  text = text.replace(/platform admin required/i, '需要平台管理员权限。')
  text = text.replace(
    /execution resolution no longer matches the task state/i,
    '任务状态或执行 nonce 已变化。请刷新任务，并重新核验网关结果。',
  )
  text = text.replace(
    /lease_nonce, a supported resolution, and a safe evidence_note are required/i,
    '执行 nonce、裁决结果和不含敏感信息的核验说明均为必填。',
  )
  text = text.replace(
    /only confirmed_applied may include a result/i,
    '只有“已独立确认操作生效”可以携带类型化回执。',
  )
  text = text.replace(
    /traffic-share result does not exactly match the persisted task input/i,
    '流量比例回执与任务的调度身份不一致，请刷新后重新核验。',
  )
  text = text.replace(/write not permitted for this role\/user/i, '当前角色无权执行写入操作。')
  text = text.replace(/registration is disabled/i, '当前未开放自助注册。')
  text = text.replace(/email suffix is not allowed/i, '该邮箱后缀不在允许注册范围内。')
  text = text.replace(/turnstile token is required/i, '请先完成人机验证。')
  text = text.replace(/turnstile verification failed/i, '人机验证失败，请重试。')
  text = text.replace(/invalid enrollment token/i, '安装令牌无效。')
  text = text.replace(/enrollment token is expired or already used/i, '安装令牌已过期或已被使用。')
  text = text.replace(/enrollment_token is required/i, '缺少安装令牌。')
  text = text.replace(/connector_id is required/i, '缺少连接器 ID。')
  text = text.replace(/user_id is required/i, '请选择账号。')
  text = text.replace(/user_id must be a positive integer/i, '账号 ID 必须是正整数。')
  text = text.replace(/user is disabled/i, '该账号已停用。')
  text = text.replace(/name is required/i, '请输入名称。')
  text = text.replace(/name and kind are required/i, '请输入实例名称并选择网关类型。')
  text = text.replace(/instance_id is required/i, '请选择实例。')
  text = text.replace(/cannot edit a revoked offer/i, '已撤销的供给不能编辑。')
  text = text.replace(/cannot allocate a revoked offer/i, '已撤销的供给不能分配。')
  text = text.replace(
    /offer already has an active allocation for this instance/i,
    '该供给已分配到此实例，请勿重复分配。',
  )
  text = text.replace(
    /revoke allocated ledger entries before revoking the offer/i,
    '该供给仍有已分配台账，请先逐条回收后再撤销。',
  )
  text = text.replace(/pool_id is required/i, '请选择上游池。')
  text = text.replace(/pool_id does not exist/i, '上游池不存在。')
  text = text.replace(/instance_id does not exist/i, '实例不存在。')
  text = text.replace(/at least one channel is required/i, '至少需要选择一个渠道。')
  text = text.replace(/max_channels must be >= 0/i, '最大渠道数不能小于 0。')
  text = text.replace(/invalid_json/i, '请求数据格式不正确。')
  text = text.replace(
    /superseded by route-plan scheduling generation\s+(\d+)/i,
    '已被发布计划调度版本 $1 取代。',
  )
  text = text.replace(
    /observation repair drained the replacement; the downstream remains fail-closed until a healthy source recovers/i,
    '已摘除未完成观察的替代渠道；在健康来源恢复前，当前下游保持停止转发。',
  )
  text = text.replace(
    /local ejection succeeded but circuit persistence failed/i,
    '当前下游摘除成功，但质量熔断状态保存失败。',
  )
  text = text.replace(
    /the durable auto-switch decision will repair the circuit on the next sweep/i,
    '持久化的自动切换决策将在下一轮检查中修复熔断状态。',
  )
  text = text.replace(
    /replacement is held by rollout policy; hard-failed binding was isolated/i,
    '替代渠道受发布策略限制；硬故障绑定已被隔离。',
  )
  text = text.replace(
    /replacement cannot be admitted safely; hard-failed binding was isolated/i,
    '替代渠道暂时无法安全接入；硬故障绑定已被隔离。',
  )
  text = text.replace(/replacement failed during scheduling apply/i, '替代渠道调度应用失败。')
  text = text.replace(/replacement failed its observation window/i, '替代渠道未通过观察期。')
  text = text.replace(
    /guarded recovery rollout completed at 100%/i,
    '灰度恢复已完成，流量已恢复至 100%。',
  )

  return text
}

function fallbackByCode(code?: string, status?: number): string {
  switch (code) {
    case 'gateway_error':
      return '网关调用失败，请检查实例连接器、管理地址和本地网关凭证。'
    case 'connector_not_configured':
      return '该实例尚未绑定连接器，请先安装并上线连接器。'
    case 'store_error':
      return '数据读写失败，请稍后重试。'
    case 'validation_failed':
      return '输入内容不完整或格式不正确，请检查后重试。'
    case 'invalid_json':
      return '请求数据格式不正确。'
    case 'invalid_credentials':
      return '邮箱或密码错误。'
    case 'unauthorized':
      return '登录已失效，请重新登录。'
    case 'forbidden':
      return '当前账号无权执行此操作。'
    case 'not_found':
      return '请求的资源不存在或已被删除。'
    case 'duplicate_connector':
      return '连接器安装令牌或连接器已存在，请刷新后重试。'
    case 'vault_unavailable':
      return '凭证保险箱未配置。'
    case 'publish_unavailable':
      return '发布引擎未配置。'
    case 'billing_unavailable':
      return '计费计算器未配置。'
    default:
      if (status === 401) return '登录已失效，请重新登录。'
      if (status === 403) return '当前账号无权执行此操作。'
      if (status === 404) return '请求的资源不存在或已被删除。'
      if (status && status >= 500) return '服务暂时不可用，请稍后重试。'
      return ''
  }
}

export function friendlyErrorMessage(error: unknown): string {
  if (!shouldUseChineseErrors()) {
    if (error instanceof ApiError || error instanceof Error) return error.message
    return errorText(error)
  }
  if (error instanceof ApiError) {
    if (error.code === 'payment_source_conflict') {
      return '该渠道仍被全局收款设置引用；请先关闭收款、隐藏对应支付方式或切换来源。'
    }
    if (error.code === 'notification_target_in_use') {
      return '该接收渠道仍被通知使用，请先切换或停用相关通知，并等待发送中的消息处理完成。'
    }
    const normalized = normalizeErrorText(error.message)
    if (normalized && normalized !== error.message) return normalized
    return fallbackByCode(error.code, error.status) || normalized || '操作失败，请稍后重试。'
  }
  if (error instanceof Error) {
    return normalizeErrorText(error.message) || '操作失败，请稍后重试。'
  }
  return normalizeErrorText(error) || '操作失败，请稍后重试。'
}

export function friendlyInlineError(message?: unknown): string {
  if (message === undefined || message === null || message === '') return ''
  if (!shouldUseChineseErrors()) return errorText(message).trim()
  return normalizeErrorText(message)
}
