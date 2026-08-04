import { describe, expect, it } from 'vitest'
import {
  COOLDOWN_RULES_LABEL,
  MODEL_MAPPING_LABEL,
  cooldownRowsFromLabel,
  labelsFromForm,
  modelMappingRowsFromLabel,
  otherLabelsText,
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

  it('round-trips cooldown rules, splitting and rejoining keywords', () => {
    const label = JSON.stringify([
      { status: 429, keywords: ['quota', 'rate limit'], cooldown_seconds: 300 },
    ])
    const rows = cooldownRowsFromLabel(label)
    expect(rows).toEqual([{ status: 429, keywords: 'quota, rate limit', cooldown_seconds: 300 }])
    const labels = labelsFromForm({ cooldown_rules: rows })
    expect(JSON.parse(labels[COOLDOWN_RULES_LABEL])).toEqual([
      { status: 429, keywords: ['quota', 'rate limit'], cooldown_seconds: 300 },
    ])
  })

  it('keeps a keyword-free rule as a status-only match', () => {
    const labels = labelsFromForm({
      cooldown_rules: [{ status: 503, keywords: '', cooldown_seconds: 60 }],
    })
    expect(JSON.parse(labels[COOLDOWN_RULES_LABEL])).toEqual([
      { status: 503, keywords: [], cooldown_seconds: 60 },
    ])
  })

  it('drops incomplete rows instead of writing broken labels', () => {
    const labels = labelsFromForm({
      model_mapping: [{ from: 'a', to: '' }, { from: '', to: 'b' }, {}],
      cooldown_rules: [{ status: 429 }, { cooldown_seconds: 30 }],
    })
    expect(labels[MODEL_MAPPING_LABEL]).toBeUndefined()
    expect(labels[COOLDOWN_RULES_LABEL]).toBeUndefined()
  })

  it('preserves unrelated labels and never duplicates the managed ones', () => {
    const stored = {
      'region': 'cn',
      [MODEL_MAPPING_LABEL]: JSON.stringify({ a: 'b' }),
      [COOLDOWN_RULES_LABEL]: JSON.stringify([{ status: 429, cooldown_seconds: 10 }]),
    }
    const text = otherLabelsText(stored)
    expect(text).toBe('region=cn')

    const labels = labelsFromForm({
      labels: text,
      model_mapping: modelMappingRowsFromLabel(stored[MODEL_MAPPING_LABEL]),
      cooldown_rules: cooldownRowsFromLabel(stored[COOLDOWN_RULES_LABEL]),
    })
    expect(labels.region).toBe('cn')
    expect(JSON.parse(labels[MODEL_MAPPING_LABEL])).toEqual({ a: 'b' })
    expect(JSON.parse(labels[COOLDOWN_RULES_LABEL])).toEqual([
      { status: 429, keywords: [], cooldown_seconds: 10 },
    ])
  })

  it('tolerates malformed stored labels instead of crashing the form', () => {
    expect(modelMappingRowsFromLabel('not json')).toEqual([])
    expect(modelMappingRowsFromLabel('[1,2]')).toEqual([])
    expect(cooldownRowsFromLabel('not json')).toEqual([])
    expect(cooldownRowsFromLabel('{"a":1}')).toEqual([])
    expect(modelMappingRowsFromLabel(undefined)).toEqual([])
  })
})
