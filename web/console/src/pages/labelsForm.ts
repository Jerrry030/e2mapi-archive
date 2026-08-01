export function parseLabels(
  value: string | undefined,
  invalidMessage = '标签必须是字符串键值 JSON 对象',
): Record<string, string> | undefined {
  const text = value?.trim()
  if (!text) return undefined

  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error(invalidMessage)
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(invalidMessage)
  }
  const labels = parsed as Record<string, unknown>
  if (Object.values(labels).some((item) => typeof item !== 'string')) {
    throw new Error(invalidMessage)
  }
  return labels as Record<string, string>
}

export function formatLabels(labels?: Record<string, string>): string | undefined {
  return labels && Object.keys(labels).length > 0 ? JSON.stringify(labels, null, 2) : undefined
}

export function labelsFieldValidator(_: unknown, value?: string) {
  try {
    parseLabels(value)
    return Promise.resolve()
  } catch (error) {
    return Promise.reject(error)
  }
}
