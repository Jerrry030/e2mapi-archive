// The data plane reads two upstream behaviours from channel labels. The
// console edits them as structured rows and serializes here, so operators
// never hand write JSON; any other label stays free-form text.
export const MODEL_MAPPING_LABEL = 'e2m.model_mapping'
export const COOLDOWN_RULES_LABEL = 'e2m.error_cooldown_rules'

export interface ModelMappingRow {
  from?: string
  to?: string
}

export interface CooldownRuleRow {
  status?: number
  keywords?: string
  cooldown_seconds?: number
}

export function parseLabels(value: unknown): Record<string, string> {
  const labels: Record<string, string> = {}
  for (const entry of String(value ?? '').split(',')) {
    const [rawKey, ...rawValue] = entry.split('=')
    const key = rawKey?.trim()
    const labelValue = rawValue.join('=').trim()
    if (key && labelValue) labels[key] = labelValue
  }
  return labels
}

export function modelMappingRowsFromLabel(raw?: string): ModelMappingRow[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as Record<string, string>
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return []
    return Object.entries(parsed).map(([from, to]) => ({ from, to: String(to) }))
  } catch {
    return []
  }
}

export function cooldownRowsFromLabel(raw?: string): CooldownRuleRow[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as {
      status?: number
      keywords?: string[]
      cooldown_seconds?: number
    }[]
    if (!Array.isArray(parsed)) return []
    return parsed.map((rule) => ({
      status: rule?.status,
      keywords: (rule?.keywords ?? []).join(', '),
      cooldown_seconds: rule?.cooldown_seconds,
    }))
  } catch {
    return []
  }
}

// preservedLabels carries every label the form has no editor for. The console
// only edits the model mapping, so anything else an operator or an earlier
// build stored (cooldown rules, routing hints) must survive a save untouched
// rather than be silently dropped.
export function preservedLabels(labels?: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(labels ?? {})) {
    if (key !== MODEL_MAPPING_LABEL) out[key] = value
  }
  return out
}

export function labelsFromForm(values: {
  preserved_labels?: Record<string, string>
  model_mapping?: ModelMappingRow[]
}): Record<string, string> {
  const labels = { ...(values.preserved_labels ?? {}) }
  const mapping: Record<string, string> = {}
  for (const row of values.model_mapping ?? []) {
    const from = row?.from?.trim()
    const to = row?.to?.trim()
    if (from && to) mapping[from] = to
  }
  if (Object.keys(mapping).length) labels[MODEL_MAPPING_LABEL] = JSON.stringify(mapping)
  else delete labels[MODEL_MAPPING_LABEL]
  return labels
}
