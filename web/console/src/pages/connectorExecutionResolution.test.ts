import { describe, expect, it } from 'vitest'
import type { ConnectorTask } from '../api/types'
import {
  buildConnectorExecutionResolutionInput,
  canBuildConfirmedAppliedResult,
  ConnectorExecutionResolutionValidationError,
  validateExecutionEvidence,
  validateExecutionNonce,
} from './connectorExecutionResolution'

const nonce = 'a'.repeat(43)

function task(overrides: Partial<ConnectorTask> = {}): ConnectorTask {
  return {
    id: 'task-1',
    instance_id: 'instance-1',
    connector_id: 'connector-1',
    type: 'gateway.account.traffic_share.set',
    schema_version: 1,
    risk_level: 'L1',
    status: 'executing',
    attempts: 1,
    max_attempts: 3,
    target_account_id: 'account-a',
    target_traffic_share: 25,
    scheduling_fence: {
      scope: 'auto-switch/plan/plan-a',
      version: 7,
      sequence: 2,
    },
    available_at: '2026-07-27T01:00:00Z',
    expires_at: '2026-07-27T02:00:00Z',
    created_at: '2026-07-27T01:00:00Z',
    updated_at: '2026-07-27T01:01:00Z',
    ...overrides,
  }
}

describe('connector execution resolution contract', () => {
  it('constructs an exact traffic-share typed receipt from read-only task facts', () => {
    expect(
      buildConnectorExecutionResolutionInput(task(), 'online', {
        lease_nonce: nonce,
        resolution: 'confirmed_applied',
        evidence_note: ' Independently read the gateway account and confirmed the 25% share. ',
      }),
    ).toEqual({
      lease_nonce: nonce,
      resolution: 'confirmed_applied',
      evidence_note: 'Independently read the gateway account and confirmed the 25% share.',
      result: {
        account_id: 'account-a',
        weight: 25,
        fence: { scope: 'auto-switch/plan/plan-a', version: 7, sequence: 2 },
      },
    })
  })

  it('never sends a result for confirmed-not-applied or revoked-unverifiable', () => {
    const base = {
      lease_nonce: nonce,
      evidence_note: 'Independent gateway readback found no applied mutation.',
    }
    expect(
      buildConnectorExecutionResolutionInput(task(), 'online', {
        ...base,
        resolution: 'confirmed_not_applied',
      }),
    ).not.toHaveProperty('result')
    expect(
      buildConnectorExecutionResolutionInput(task(), 'revoked', {
        ...base,
        resolution: 'connector_revoked_unverifiable',
      }),
    ).not.toHaveProperty('result')
  })

  it('requires revoked status before producing revoked-unverifiable', () => {
    expect(() =>
      buildConnectorExecutionResolutionInput(task(), 'offline', {
        lease_nonce: nonce,
        resolution: 'connector_revoked_unverifiable',
        evidence_note: 'Connector was lost before the outcome could be independently verified.',
      }),
    ).toThrowError(
      expect.objectContaining<Partial<ConnectorExecutionResolutionValidationError>>({
        code: 'connectorMustBeRevoked',
      }),
    )
  })

  it('fails closed when an applied receipt cannot be reconstructed from the summary', () => {
    const incomplete = task({ scheduling_fence: undefined })
    expect(canBuildConfirmedAppliedResult(incomplete)).toBe(false)
    expect(() =>
      buildConnectorExecutionResolutionInput(incomplete, 'online', {
        lease_nonce: nonce,
        resolution: 'confirmed_applied',
        evidence_note: 'Independently confirmed the gateway mutation was applied.',
      }),
    ).toThrowError(
      expect.objectContaining<Partial<ConnectorExecutionResolutionValidationError>>({
        code: 'typedResultUnavailable',
      }),
    )
  })

  it('requires a non-secret remote ID for an applied account creation', () => {
    const createTask = task({
      type: 'gateway.account.create',
      target_channel_id: 'channel-a',
      target_account_id: undefined,
      target_traffic_share: undefined,
    })
    expect(() =>
      buildConnectorExecutionResolutionInput(createTask, 'online', {
        lease_nonce: nonce,
        resolution: 'confirmed_applied',
        evidence_note: 'Independently confirmed that the account was created.',
      }),
    ).toThrowError(expect.objectContaining({ code: 'remoteIdRequired' }))
    expect(
      buildConnectorExecutionResolutionInput(createTask, 'online', {
        lease_nonce: nonce,
        resolution: 'confirmed_applied',
        evidence_note: 'Independently confirmed that the account was created.',
        remote_id: 'remote-account-7',
      }),
    ).toMatchObject({ result: { remote_id: 'remote-account-7', created: true } })
  })

  it('accepts only the exact 32-byte base64url nonce shape', () => {
    expect(validateExecutionNonce(nonce)).toBeNull()
    expect(validateExecutionNonce('')).toBe('nonceRequired')
    expect(validateExecutionNonce(' ' + nonce)).toBe('nonceInvalid')
    expect(validateExecutionNonce('a'.repeat(42))).toBe('nonceInvalid')
    expect(validateExecutionNonce('a'.repeat(42) + '=')).toBe('nonceInvalid')
  })

  it('mirrors the backend evidence secret and size guards', () => {
    expect(validateExecutionEvidence('Verified by independent gateway readback.')).toBeNull()
    expect(validateExecutionEvidence('')).toBe('evidenceRequired')
    expect(validateExecutionEvidence('x'.repeat(1001))).toBe('evidenceTooLong')
    expect(validateExecutionEvidence('Authorization: Bearer secret')).toBe('evidenceSensitive')
    expect(validateExecutionEvidence('https://gateway.example/admin')).toBe('evidenceSensitive')
    expect(validateExecutionEvidence('e2m_conn_not-for-audit')).toBe('evidenceSensitive')
  })
})
