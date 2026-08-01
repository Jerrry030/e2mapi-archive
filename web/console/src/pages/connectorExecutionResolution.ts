import type {
  ConnectorStatus,
  ConnectorTask,
  ConnectorTaskExecutionResolveInput,
  ConnectorTaskExecutionResolution,
} from '../api/types'

export const EXECUTION_NONCE_LENGTH = 43
export const EXECUTION_EVIDENCE_MAX_RUNES = 1000

export interface ConnectorExecutionResolutionValues {
  resolution: ConnectorTaskExecutionResolution
  lease_nonce: string
  evidence_note: string
  remote_id?: string
}

export type ConnectorExecutionResolutionValidationCode =
  | 'nonceRequired'
  | 'nonceInvalid'
  | 'evidenceRequired'
  | 'evidenceTooLong'
  | 'evidenceSensitive'
  | 'remoteIdRequired'
  | 'remoteIdInvalid'
  | 'typedResultUnavailable'
  | 'connectorMustBeRevoked'
  | 'resolutionUnsupported'

export class ConnectorExecutionResolutionValidationError extends Error {
  readonly code: ConnectorExecutionResolutionValidationCode

  constructor(code: ConnectorExecutionResolutionValidationCode) {
    super(code)
    this.name = 'ConnectorExecutionResolutionValidationError'
    this.code = code
  }
}

const mutationResultTaskTypes = new Set<ConnectorTask['type']>([
  'gateway.account.schedulable.set',
  'gateway.account.switch',
  'gateway.scheduling.barrier',
  'gateway.account.create',
  'gateway.account.update',
  'gateway.account.delete',
])

const connectorSensitiveAssignmentPattern =
  /(?:^|[,{;])(?:access_?token|api_?key|authorization|bearer|cookie|credential|password|private_?key|proxy_?authorization|secret|session|token)[:=]/i

export function validateExecutionNonce(
  value: string,
): ConnectorExecutionResolutionValidationCode | null {
  const trimmed = value.trim()
  if (!trimmed) return 'nonceRequired'
  if (
    value !== trimmed ||
    trimmed.length !== EXECUTION_NONCE_LENGTH ||
    !/^[A-Za-z0-9_-]+$/.test(trimmed)
  ) {
    return 'nonceInvalid'
  }
  return null
}

export function validateExecutionEvidence(
  value: string,
): ConnectorExecutionResolutionValidationCode | null {
  const trimmed = value.trim()
  if (!trimmed) return 'evidenceRequired'
  if (Array.from(trimmed).length > EXECUTION_EVIDENCE_MAX_RUNES) return 'evidenceTooLong'
  if (containsUnsafeEvidence(trimmed)) return 'evidenceSensitive'
  return null
}

export function validateExecutionRemoteID(
  value: string,
): ConnectorExecutionResolutionValidationCode | null {
  const trimmed = value.trim()
  if (!trimmed) return 'remoteIdRequired'
  if (trimmed.length > 128 || !/^[A-Za-z0-9._@+-]+$/.test(trimmed)) return 'remoteIdInvalid'
  return null
}

export function taskNeedsOperatorRemoteID(task: ConnectorTask): boolean {
  return task.type === 'gateway.account.create'
}

export function canBuildConfirmedAppliedResult(task: ConnectorTask): boolean {
  if (task.type === 'gateway.account.traffic_share.set') {
    const fence = task.scheduling_fence
    return Boolean(
      task.target_account_id &&
      Number.isInteger(task.target_traffic_share) &&
      task.target_traffic_share !== undefined &&
      task.target_traffic_share >= 0 &&
      task.target_traffic_share <= 100 &&
      fence?.scope &&
      Number.isInteger(fence.version) &&
      fence.version > 0 &&
      Number.isInteger(fence.sequence) &&
      fence.sequence > 0,
    )
  }
  if (!mutationResultTaskTypes.has(task.type)) return false
  if (
    (task.type === 'gateway.account.update' || task.type === 'gateway.account.delete') &&
    !task.target_account_id
  ) {
    return false
  }
  return true
}

export function buildConnectorExecutionResolutionInput(
  task: ConnectorTask,
  connectorStatus: ConnectorStatus | undefined,
  values: ConnectorExecutionResolutionValues,
): ConnectorTaskExecutionResolveInput {
  const nonceError = validateExecutionNonce(values.lease_nonce)
  if (nonceError) throw new ConnectorExecutionResolutionValidationError(nonceError)
  const evidenceError = validateExecutionEvidence(values.evidence_note)
  if (evidenceError) throw new ConnectorExecutionResolutionValidationError(evidenceError)

  const base: ConnectorTaskExecutionResolveInput = {
    lease_nonce: values.lease_nonce.trim(),
    resolution: values.resolution,
    evidence_note: values.evidence_note.trim(),
  }
  switch (values.resolution) {
    case 'confirmed_not_applied':
      return base
    case 'connector_revoked_unverifiable':
      if (connectorStatus !== 'revoked') {
        throw new ConnectorExecutionResolutionValidationError('connectorMustBeRevoked')
      }
      return base
    case 'confirmed_applied':
      if (!canBuildConfirmedAppliedResult(task)) {
        throw new ConnectorExecutionResolutionValidationError('typedResultUnavailable')
      }
      if (task.type === 'gateway.account.traffic_share.set') {
        return {
          ...base,
          result: {
            account_id: task.target_account_id!,
            weight: task.target_traffic_share!,
            fence: { ...task.scheduling_fence! },
          },
        }
      }
      if (task.type === 'gateway.account.create') {
        const remoteIDError = validateExecutionRemoteID(values.remote_id ?? '')
        if (remoteIDError) throw new ConnectorExecutionResolutionValidationError(remoteIDError)
        return { ...base, result: { remote_id: values.remote_id!.trim(), created: true } }
      }
      if (task.type === 'gateway.account.update' || task.type === 'gateway.account.delete') {
        return { ...base, result: { remote_id: task.target_account_id } }
      }
      return { ...base, result: {} }
    default:
      throw new ConnectorExecutionResolutionValidationError('resolutionUnsupported')
  }
}

function containsUnsafeEvidence(value: string): boolean {
  for (const rune of value) {
    const code = rune.codePointAt(0) ?? 0
    if ((code < 32 || code === 127) && rune !== '\n' && rune !== '\t') return true
  }
  const lower = value.toLowerCase()
  const normalizedSpaces = lower.replace(/\s+/g, ' ')
  const withoutSpaces = lower.replace(/\s/g, '')
  const markers = [
    '://',
    'www.',
    'authorization:',
    'authorization=',
    'bearer ',
    'token=',
    'token:',
    'api_key=',
    'api-key:',
    'x-api-key:',
    'cookie:',
    'cookie=',
    'set-cookie:',
    'proxy-authorization:',
    'header:',
    'headers=',
    'raw_response',
    'raw-response',
  ]
  if (
    markers.some(
      (marker) =>
        lower.includes(marker) ||
        normalizedSpaces.includes(marker) ||
        withoutSpaces.includes(marker),
    ) ||
    connectorSensitiveAssignmentPattern.test(withoutSpaces)
  ) {
    return true
  }
  if (
    ['sk-', 'sk_', 'e2m_conn_', 'e2m_enroll_', 'ghp_', 'github_pat_', 'xox'].some((prefix) =>
      lower.startsWith(prefix),
    )
  ) {
    return true
  }
  if (value.length > 40 && lower.startsWith('eyj') && value.split('.').length === 3) return true
  return value.length >= 40 && /^[A-Fa-f0-9]+$/.test(value)
}
