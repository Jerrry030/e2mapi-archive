import type {
  RouteStrategy,
  RouteStrategyType,
  StrategyScope,
  StrategyThresholds,
  StrategyWeights,
} from '../api/types'

const weightFields: (keyof StrategyWeights)[] = ['success', 'ttft', 'duration', 'stability', 'cost']

function compact<T extends Record<string, unknown>>(input: T): Partial<T> {
  return Object.fromEntries(
    Object.entries(input).filter(
      ([, value]) => value !== undefined && value !== null && value !== '',
    ),
  ) as Partial<T>
}

function customWeights(values: Record<string, unknown>): StrategyWeights | undefined {
  const weights = Object.fromEntries(
    weightFields
      .map((key) => [key, values[`weight_${key}`]])
      .filter(([, value]) => typeof value === 'number'),
  ) as StrategyWeights
  return Object.keys(weights).length ? weights : undefined
}

function customThresholds(values: Record<string, unknown>): StrategyThresholds | undefined {
  const thresholds = compact({
    min_samples: values.threshold_min_samples,
    target_success_rate: values.threshold_target_success_rate,
    floor_success_rate: values.threshold_floor_success_rate,
    max_ttft_p95_ms: values.threshold_max_ttft_p95_ms,
    max_duration_p95_ms: values.threshold_max_duration_p95_ms,
    consecutive_failure_limit: values.threshold_consecutive_failure_limit,
    eject_score: values.threshold_eject_score,
  }) as StrategyThresholds
  return Object.keys(thresholds).length ? thresholds : undefined
}

export function strategyValidationError(values: Record<string, unknown>) {
  const weights = customWeights(values)
  if (weights) {
    const supplied = weightFields.filter((key) => typeof weights[key] === 'number')
    const sum = supplied.reduce((total, key) => total + (weights[key] ?? 0), 0)
    if (supplied.length !== weightFields.length || Math.abs(sum - 1) > 0.001) {
      return '自定义评分权重需要完整填写，且五项合计必须为 1'
    }
  }
  const thresholds = customThresholds(values)
  if (
    typeof thresholds?.floor_success_rate === 'number' &&
    typeof thresholds.target_success_rate === 'number' &&
    thresholds.floor_success_rate > thresholds.target_success_rate
  ) {
    return '成功率硬底线不能高于目标成功率'
  }
  if (
    typeof thresholds?.eject_score === 'number' &&
    (thresholds.eject_score <= 0 || thresholds.eject_score > 100)
  ) {
    return '摘除分数线必须大于 0 且不超过 100'
  }
  return undefined
}

export function strategyFromForm(
  values: Partial<RouteStrategy> & Record<string, unknown>,
  scope: StrategyScope,
): RouteStrategy {
  return {
    name: String(values.name ?? '').trim() || undefined,
    scope,
    plan_id: scope === 'plan' ? String(values.plan_id ?? '') : undefined,
    pool_id: scope === 'pool' ? String(values.pool_id ?? '') : undefined,
    user_id: scope === 'user' ? Number(values.user_id) : undefined,
    type: (values.type ?? 'stability_first') as RouteStrategyType,
    auto_apply: !!values.auto_apply,
    approval_required: !!values.approval_required,
    cooldown_seconds: values.cooldown_seconds,
    recovery_observation_seconds: values.recovery_observation_seconds,
    max_auto_switches_per_hour: values.max_auto_switches_per_hour,
    thresholds: customThresholds(values),
    weights: customWeights(values),
  }
}
