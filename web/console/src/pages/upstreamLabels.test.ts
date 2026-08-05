import { describe, expect, it } from 'vitest'
import {
  COOLDOWN_RULES_LABEL,
  MODEL_MAPPING_LABEL,
  cooldownRowsFromLabel,
  cooldownRuleCount,
  labelsFromForm,
  labelsWithCooldownRules,
  modelMappingRowsFromLabel,
  preservedLabels,
} from './upstreamLabels'

describe('upstream label serialization', () => {
  it('round-trips model mappings between rows and the label', () => {
    const label = JSON.stringify({ 'gpt-4o-mini': 'real-mini', 'gpt-4o': 'real-4o' })
    const rows = modelMappingRowsFromLabel(label)
    expect(rows).toEqual([
      { from: 'gpt-4o-mini', to: 'real-mini' },
      { from: 'gpt-4o', to: 'real-4o' },
    ])
    const labels = labelsFromForm({ model_mapping: rows })
    expect(JSON.parse(labels[MODEL_MAPPING_LABEL])).toEqual({
      'gpt-4o-mini': 'real-mini',
      'gpt-4o': 'real-4o',
    })
  })

  it('drops incomplete mapping rows instead of writing broken labels', () => {
    const labels = labelsFromForm({
      model_mapping: [{ from: 'a', to: '' }, { from: '', to: 'b' }, {}],
    })
    expect(labels[MODEL_MAPPING_LABEL]).toBeUndefined()
  })

  it('clears the mapping label when every row is removed', () => {
    const labels = labelsFromForm({
      preserved_labels: { [MODEL_MAPPING_LABEL]: JSON.stringify({ a: 'b' }), region: 'cn' },
      model_mapping: [],
    })
    expect(labels[MODEL_MAPPING_LABEL]).toBeUndefined()
    expect(labels.region).toBe('cn')
  })

  it('preserves labels the form has no editor for, including cooldown rules', () => {
    // The console dropped the cooldown-rule and free-form label editors, so a
    // save must carry those stored values through untouched rather than wipe
    // a capability the data plane still honours.
    const stored = {
      region: 'cn',
      [COOLDOWN_RULES_LABEL]: JSON.stringify([{ status: 429, keywords: ['quota'], cooldown_seconds: 300 }]),
      [MODEL_MAPPING_LABEL]: JSON.stringify({ a: 'b' }),
    }
    const preserved = preservedLabels(stored)
    expect(preserved).toEqual({ region: 'cn', [COOLDOWN_RULES_LABEL]: stored[COOLDOWN_RULES_LABEL] })

    const labels = labelsFromForm({
      preserved_labels: preserved,
      model_mapping: modelMappingRowsFromLabel(stored[MODEL_MAPPING_LABEL]),
    })
    expect(labels.region).toBe('cn')
    expect(labels[COOLDOWN_RULES_LABEL]).toBe(stored[COOLDOWN_RULES_LABEL])
    expect(JSON.parse(labels[MODEL_MAPPING_LABEL])).toEqual({ a: 'b' })
  })

  it('still parses stored cooldown rules for callers that read them', () => {
    const rows = cooldownRowsFromLabel(
      JSON.stringify([{ status: 429, keywords: ['quota', 'rate limit'], cooldown_seconds: 300 }]),
    )
    expect(rows).toEqual([{ status: 429, keywords: 'quota, rate limit', cooldown_seconds: 300 }])
  })

  it('rewrites only the cooldown label from the row editor', () => {
    const stored = {
      region: 'cn',
      [MODEL_MAPPING_LABEL]: JSON.stringify({ a: 'b' }),
      [COOLDOWN_RULES_LABEL]: JSON.stringify([{ status: 500, cooldown_seconds: 60 }]),
    }
    const labels = labelsWithCooldownRules(stored, [
      { status: 429, keywords: 'quota, rate limit', cooldown_seconds: 300 },
      { status: 429, keywords: '', cooldown_seconds: 0 }, // invalid row is dropped
      {},
    ])
    expect(labels.region).toBe('cn')
    expect(JSON.parse(labels[MODEL_MAPPING_LABEL])).toEqual({ a: 'b' })
    expect(JSON.parse(labels[COOLDOWN_RULES_LABEL])).toEqual([
      { status: 429, keywords: ['quota', 'rate limit'], cooldown_seconds: 300 },
    ])
  })

  it('round-trips the cooldown editor through parse and serialize', () => {
    const label = JSON.stringify([{ status: 429, keywords: ['quota'], cooldown_seconds: 300 }])
    const rows = cooldownRowsFromLabel(label)
    const labels = labelsWithCooldownRules({}, rows)
    expect(JSON.parse(labels[COOLDOWN_RULES_LABEL])).toEqual(JSON.parse(label))
    expect(cooldownRuleCount(label)).toBe(1)
    expect(cooldownRuleCount(undefined)).toBe(0)
  })

  it('clears the cooldown label when every rule is removed', () => {
    const labels = labelsWithCooldownRules(
      { region: 'cn', [COOLDOWN_RULES_LABEL]: JSON.stringify([{ status: 500, cooldown_seconds: 60 }]) },
      [],
    )
    expect(labels[COOLDOWN_RULES_LABEL]).toBeUndefined()
    expect(labels.region).toBe('cn')
  })

  it('tolerates malformed stored labels instead of crashing the form', () => {
    expect(modelMappingRowsFromLabel('not json')).toEqual([])
    expect(modelMappingRowsFromLabel('[1,2]')).toEqual([])
    expect(cooldownRowsFromLabel('not json')).toEqual([])
    expect(cooldownRowsFromLabel('{"a":1}')).toEqual([])
    expect(modelMappingRowsFromLabel(undefined)).toEqual([])
    expect(preservedLabels(undefined)).toEqual({})
  })
})
