import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

describe('upstream margin page integration', () => {
  it('uses owner plus global window and renders a dedicated guarded tab', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/pages/UpstreamIntelligence.tsx'),
      'utf8',
    )
    const zh = readFileSync(resolve(process.cwd(), 'src/i18n/locales/zh.ts'), 'utf8')
    const en = readFileSync(resolve(process.cwd(), 'src/i18n/locales/en.ts'), 'utf8')
    expect(source).toContain("useUpstreamMarginSummary(userId, location.window ?? '24h')")
    expect(source).toContain("key: 'margin'")
    expect(source).toContain("label: t('upstreamIntelligence.page.tabMargin')")
    expect(source).toContain('<UpstreamMarginSummary view={margin.data} />')
    expect(source).toContain("t('upstreamIntelligence.page.unknownDataNotice')")
    expect(zh).toContain("tabMargin: '成本与毛利护栏'")
    expect(zh).toContain("unknownDataNotice: '页面不会用 0 代替未知数据。'")
    expect(en).toContain("tabMargin: 'Cost & margin guardrails'")
    expect(en).toContain("unknownDataNotice: 'Unknown values are never shown as zero.'")
  })
})
