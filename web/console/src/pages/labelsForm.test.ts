import { describe, expect, it } from 'vitest'
import { formatLabels, labelsFieldValidator, parseLabels } from './labelsForm'

describe('labels administration form', () => {
  it('round-trips string labels', () => {
    const labels = { environment: 'production', remote_id: 'account-1' }
    expect(parseLabels(formatLabels(labels))).toEqual(labels)
  })

  it('rejects arrays and non-string values', () => {
    expect(() => parseLabels('["production"]')).toThrow('标签')
    expect(() => parseLabels('{"priority":1}')).toThrow('标签')
    expect(() => parseLabels('{broken')).toThrow('标签')
  })

  it('treats blank input as no labels', () => {
    expect(parseLabels('  ')).toBeUndefined()
    expect(formatLabels({})).toBeUndefined()
  })

  it('exposes a form validator for the shared labels field', async () => {
    await expect(labelsFieldValidator(undefined, '{"region":"hk"}')).resolves.toBeUndefined()
    await expect(labelsFieldValidator(undefined, '{"region":7}')).rejects.toThrow('标签')
  })
})
