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

// otherLabelsText renders every label except the two managed by dedicated
// form sections, so round-tripping never duplicates or drops them.
export function otherLabelsText(labels?: Record<string, string>): string {
  return Object.entries(labels ?? {})
    .filter(([key]) => key !== MODEL_MAPPING_LABEL && key !== COOLDOWN_RULES_LABEL)
    .map(([key, value]) => `${key}=${value}`)
    .join(', ')
}

export function labelsFromForm(values: {
  labels?: string
  model_mapping?: ModelMappingRow[]
  cooldown_rules?: CooldownRuleRow[]
}): Record<string, string> {
  const labels = parseLabels(values.labels)
  const mapping: Record<string, string> = {}
  for (const row of values.model_mapping ?? []) {
    const from = row?.from?.trim()
    const to = row?.to?.trim()
    if (from && to) mapping[from] = to
  }
  if (Object.keys(mapping).length) labels[MODEL_MAPPING_LABEL] = JSON.stringify(mapping)

  const rules = (values.cooldown_rules ?? [])
    .filter((row) => row?.status && row?.cooldown_seconds)
    .map((row) => ({
      status: Number(row.status),
      keywords: String(row.keywords ?? '')
        .split(',')
        .map((keyword) => keyword.trim())
        .filter(Boolean),
      cooldown_seconds: Number(row.cooldown_seconds),
    }))
  if (rules.length) labels[COOLDOWN_RULES_LABEL] = JSON.stringify(rules)
  return labels
}
