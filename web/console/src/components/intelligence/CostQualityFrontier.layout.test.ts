import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const stylesheet = readFileSync(resolve(process.cwd(), 'src/index.css'), 'utf8')
const component = readFileSync(
  resolve(process.cwd(), 'src/components/intelligence/CostQualityFrontier.tsx'),
  'utf8',
)

function declarationsFor(selector: string) {
  const declarations = new Map<string, string>()
  const rulePattern = /([^{}]+)\{([^{}]*)\}/g

  for (const match of stylesheet.matchAll(rulePattern)) {
    const selectors = match[1]
      .split(',')
      .map((candidate) => candidate.trim())
      .filter(Boolean)
    if (!selectors.includes(selector)) continue

    for (const declaration of match[2].split(';')) {
      const separator = declaration.indexOf(':')
      if (separator < 0) continue
      declarations.set(
        declaration.slice(0, separator).trim(),
        declaration.slice(separator + 1).trim(),
      )
    }
  }

  return declarations
}

function compact(value: string | undefined) {
  return value?.replace(/\s+/g, '')
}

describe('CostQualityFrontier mobile layout contract', () => {
  it('keeps the chart grid and its intrinsic-width children shrinkable', () => {
    const visual = declarationsFor('.intelligence-frontier-visual')
    const header = declarationsFor('.intelligence-frontier-chart-header')
    const legend = declarationsFor('.intelligence-frontier-chart-legend')

    expect(visual.get('display')).toBe('grid')
    expect(compact(visual.get('grid-template-columns'))).toBe('minmax(0,1fr)')
    expect(visual.get('min-width')).toBe('0')
    expect(header.get('min-width')).toBe('0')
    expect(legend.get('min-width')).toBe('0')

    for (const className of [
      'intelligence-frontier-visual',
      'intelligence-frontier-chart-header',
      'intelligence-frontier-chart-legend',
    ]) {
      expect(component).toContain(`className="${className}"`)
    }
  })

  it('keeps wide chart and table content inside local horizontal scrollers', () => {
    const chartRegion = declarationsFor('.intelligence-frontier-chart-region')
    const tableRegion = declarationsFor('.intelligence-frontier-table-region')

    expect(chartRegion.get('max-width')).toBe('100%')
    expect(chartRegion.get('overflow-x')).toBe('auto')
    expect(tableRegion.get('max-width')).toBe('100%')
    expect(tableRegion.get('overflow-x')).toBe('auto')
    expect(declarationsFor('.intelligence-frontier-chart').get('min-width')).toBe('600px')
    expect(declarationsFor('.intelligence-frontier-table').get('min-width')).toBe('980px')

    expect(component).toContain('className="intelligence-frontier-chart-region"')
    expect(component).toContain('className="intelligence-frontier-table-region"')
  })
})
