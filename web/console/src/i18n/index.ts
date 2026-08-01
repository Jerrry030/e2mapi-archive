import en from './locales/en'
import zh from './locales/zh'

export type LocaleCode = 'en' | 'zh'

const LOCALE_KEY = 'e2m.locale'
const DEFAULT_LOCALE: LocaleCode = 'zh'

const messages = { en, zh }

export const availableLocales = [
  { code: 'zh', name: '中文' },
  { code: 'en', name: 'English' },
] as const

function isLocaleCode(value: string | null | undefined): value is LocaleCode {
  return value === 'en' || value === 'zh'
}

function defaultLocale(): LocaleCode {
  const saved = localStorage.getItem(LOCALE_KEY)
  if (isLocaleCode(saved)) return saved
  if (navigator.language.toLowerCase().startsWith('en')) return 'en'
  return DEFAULT_LOCALE
}

let currentLocale = defaultLocale()
document.documentElement.setAttribute('lang', currentLocale)

export function getLocale(): LocaleCode {
  return currentLocale
}

export function setLocale(locale: LocaleCode) {
  currentLocale = locale
  localStorage.setItem(LOCALE_KEY, locale)
  document.documentElement.setAttribute('lang', locale)
  window.dispatchEvent(new Event('e2m.localeChanged'))
}

export function onLocaleChange(handler: () => void): () => void {
  window.addEventListener('e2m.localeChanged', handler)
  window.addEventListener('storage', handler)
  return () => {
    window.removeEventListener('e2m.localeChanged', handler)
    window.removeEventListener('storage', handler)
  }
}

export function t(path: string, fallback = path, values?: Record<string, string | number>): string {
  const keys = path.split('.')
  let value: unknown = messages[currentLocale]
  for (let index = 0; index < keys.length; index += 1) {
    if (!value || typeof value !== 'object') {
      value = undefined
      break
    }
    const record = value as Record<string, unknown>
    const remaining = keys.slice(index).join('.')
    if (Object.prototype.hasOwnProperty.call(record, remaining)) {
      value = record[remaining]
      break
    }
    value = record[keys[index]]
  }
  const template = typeof value === 'string' ? value : fallback
  if (!values) return template
  return Object.entries(values).reduce(
    (text, [key, next]) => text.split(`{${key}}`).join(String(next)),
    template,
  )
}

export function auditActionLabel(action: string): string {
  const actions = messages[currentLocale].audit.actions as Record<string, string>
  return actions[action] ?? t('audit.actionFallback', action, { value: action })
}

export function auditResultLabel(result: string): string {
  const results = messages[currentLocale].audit.results as Record<string, string>
  return results[result] ?? t('audit.resultFallback', result, { value: result })
}

export function auditActivityLabel(action: string, result: string, errorMessage = ''): string {
  const activities = messages[currentLocale].audit.activities as Record<string, string>
  const activityResult = result === 'success' ? 'accepted' : result
  let activityAction = action
  if (
    action === 'onboarding.workflow' &&
    activityResult === 'accepted' &&
    errorMessage.trim() === 'pool_inactive'
  ) {
    activityAction = 'onboarding.workflow.paused'
  }
  const activity = activities[`${activityAction}.${activityResult}`]
  if (activity) return activity

  const fallbacks = messages[currentLocale].audit.activityResultFallbacks as Record<string, string>
  const template = fallbacks[result]
  if (template) {
    return Object.entries({ action: auditActionLabel(action) }).reduce(
      (text, [key, value]) => text.split(`{${key}}`).join(value),
      template,
    )
  }
  return t('audit.activityFallback', undefined, {
    action: auditActionLabel(action),
    result: auditResultLabel(result),
  })
}

function activityTemplate(action: string, result: string, errorMessage: string, riskLevel: string) {
  let template = auditActivityLabel(action, result, errorMessage)
  if (action === 'connector_task.complete' && riskLevel) {
    const activities = messages[currentLocale].audit.activities as Record<string, string>
    const activityResult = result === 'success' ? 'accepted' : result
    template = activities[`connector_task.complete.${riskLevel}.${activityResult}`] ?? template
  }
  return template
}

function auditDetailValue(details: Record<string, string>, key: string, fallback: string) {
  const value = details[key]?.trim()
  return value || fallback
}

export function auditActivityDescription(
  action: string,
  result: string,
  errorMessage = '',
  instanceName = '',
  riskLevel = '',
  details: Record<string, string> = {},
): string {
  const instanceDisplayName = auditDetailValue(details, 'instance_name', instanceName.trim())
  const name = instanceDisplayName || t('audit.unknownInstance')
  const reasonCode = errorMessage || details.reason_code || ''
  const reason = reasonCode
    ? action.startsWith('connector_task.')
      ? connectorErrorLabel(reasonCode)
      : onboardingErrorLabel(reasonCode)
    : t('audit.unknownReason')
  const values: Record<string, string | number> = {
    instance: name,
    pool: auditDetailValue(details, 'pool_name', t('audit.unknownPool')),
    channel: auditDetailValue(details, 'channel_name', t('audit.unknownChannel')),
    account: auditDetailValue(details, 'account_name', t('audit.unknownAccount')),
    fromAccount: details.from_account_name || t('audit.unknownAccount'),
    toAccount: details.to_account_name || t('audit.noReplacementAccount'),
    accountCount: details.account_count || '0',
    attempts: details.attempts || '1',
    nextAttempt: details.next_attempt_at
      ? formatActivityTime(details.next_attempt_at)
      : t('audit.soon'),
    reason,
    schedulingState:
      details.schedulable === 'false'
        ? t('audit.schedulingDisabled')
        : t('audit.schedulingEnabled'),
    probeStatus:
      details.probe_status === 'available'
        ? t('audit.probeAvailable')
        : t('audit.probeUnavailable'),
  }
  const template = activityTemplate(action, result, errorMessage, riskLevel)
  const description = Object.entries(values).reduce(
    (text, [key, value]) => text.split(`{${key}}`).join(String(value)),
    template,
  )
  // Records created before connector task types were included in the action
  // only have the generic template. Keep their instance context visible while
  // avoiding a duplicate prefix on newer, task-specific descriptions.
  if (action === 'connector_task.complete' && instanceDisplayName) {
    return t('audit.activityInstanceFormat', undefined, {
      name: instanceDisplayName,
      description,
    })
  }
  return description
}

function formatActivityTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(currentLocale === 'zh' ? 'zh-CN' : 'en', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}

export function auditActorTypeLabel(actorType: string): string {
  const actorTypes = messages[currentLocale].audit.actorTypes as Record<string, string>
  return actorTypes[actorType] ?? actorType
}

export function auditActorLabel(actorType: string, actorID: string): string {
  const actorIDs = messages[currentLocale].audit.actorIds as Record<string, string>
  return t('audit.actorFormat', undefined, {
    type: auditActorTypeLabel(actorType),
    id: actorIDs[actorID] ?? actorID,
  })
}

export function auditTargetTypeLabel(targetType: string): string {
  const targetTypes = messages[currentLocale].audit.targetTypes as Record<string, string>
  return targetTypes[targetType] ?? targetType
}

export function connectorTaskTypeLabel(type: string): string {
  const taskTypes = messages[currentLocale].connectors.taskTypes as Record<string, string>
  return taskTypes[type] ?? type
}

export function connectorTaskStatusLabel(status: string): string {
  return t(`connectors.taskStatus.${status}`, status)
}

export function connectorErrorLabel(code: string): string {
  const errors = messages[currentLocale].connectors.errors as Record<string, string>
  return errors[code] ?? code
}

export function capabilityNameLabel(name: string): string {
  const names = messages[currentLocale].capabilities.names as Record<string, string>
  return names[name] ?? name
}

export function capabilityDescriptionLabel(description: string): string {
  const descriptions = messages[currentLocale].capabilities.descriptions as Record<string, string>
  return descriptions[description] ?? description
}

export function capabilityModeLabel(mode: string): string {
  const modes = messages[currentLocale].capabilities.modes as Record<string, string>
  return modes[mode] ?? mode
}

export function operationsTimelineKindLabel(kind: string): string {
  const kinds = messages[currentLocale].operations.timelineKinds as Record<string, string>
  return kinds[kind] ?? kind
}

export function operationsStatusLabel(status: string): string {
  const statuses = messages[currentLocale].operations.statuses as Record<string, string>
  return statuses[status] ?? status
}

export function onboardingStageLabel(stage: string): string {
  const stages = messages[currentLocale].operations.onboardingStages as Record<string, string>
  return stages[stage] ?? stage
}

export function onboardingErrorLabel(code: string): string {
  const errors = messages[currentLocale].operations.onboardingErrors as Record<string, string>
  return errors[code] ?? code
}

export function operationsReasonLabel(code: string, text?: string): string {
  const reasons = messages[currentLocale].operations.reasonCodes as Record<string, string>
  const templates = messages[currentLocale].operations.reasonPatterns as Record<string, string>
  const normalizedText = text?.trim() ?? ''

  const selectedStage = normalizedText.match(/^selected for (\d+)% guarded recovery stage$/)
  if (selectedStage) {
    return t('operations.reasonPatterns.selectedRecoveryStage', undefined, {
      percentage: selectedStage[1],
    })
  }
  const admittedStage = normalizedText.match(
    /^active probes passed; admitted to (\d+)% recovery stage$/,
  )
  if (admittedStage) {
    return t('operations.reasonPatterns.admittedRecoveryStage', undefined, {
      percentage: admittedStage[1],
    })
  }
  const expandedStage = normalizedText.match(/^recovery rollout expanded to (\d+)%$/)
  if (expandedStage) {
    return t('operations.reasonPatterns.expandedRecoveryStage', undefined, {
      percentage: expandedStage[1],
    })
  }
  const repairedIsolation = normalizedText.match(
    /^repaired quality isolation from auto-switch decision (.+)$/,
  )
  if (repairedIsolation) {
    return t('operations.reasonPatterns.repairedIsolation', undefined, {
      decision: repairedIsolation[1],
    })
  }
  const superseded = normalizedText.match(/^superseded by route-plan scheduling generation (\d+)$/)
  if (superseded) {
    return t('operations.reasonPatterns.supersededGeneration', undefined, {
      generation: superseded[1],
    })
  }
  const regressed = normalizedText.match(/^guarded recovery traffic regressed for source (.+)$/)
  if (regressed) {
    return t('operations.reasonPatterns.recoveryRegressed', undefined, {
      source: regressed[1],
    })
  }

  const exactText = templates[normalizedText]
  if (exactText) return exactText
  if (currentLocale === 'zh' && /[\u3400-\u9fff]/.test(normalizedText)) return normalizedText
  return reasons[code] ?? (normalizedText || code || '-')
}

export function reconcileRunLabel(kind: string, trigger: string): string {
  const kinds = messages[currentLocale].operations.reconcileKinds as Record<string, string>
  const triggers = messages[currentLocale].operations.reconcileTriggers as Record<string, string>
  return `${kinds[kind] ?? kind} · ${triggers[trigger] ?? trigger}`
}

export function reconcileDetailLabel(detail: string): string {
  const details = messages[currentLocale].operations.reconcileDetails as Record<string, string>
  const rollout = detail.match(/^held by rollout policy \(([^)]+)\); pending action: (.+)$/)
  if (rollout) {
    const modes = messages[currentLocale].operations.rolloutModes as Record<string, string>
    const actions = messages[currentLocale].operations.reconcileActions as Record<string, string>
    return t('operations.rolloutHeld', undefined, {
      mode: modes[rollout[1]] ?? rollout[1],
      action: actions[rollout[2]] ?? rollout[2],
    })
  }
  return details[detail] ?? detail
}
