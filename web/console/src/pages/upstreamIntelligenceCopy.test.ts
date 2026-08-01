import { describe, expect, it } from 'vitest'
import { upstreamIntelligenceOpportunityDescription } from './upstreamIntelligenceCopy'

describe('upstream intelligence opportunity copy', () => {
  it('keeps Pareto candidates read-only until the ledger and strategy lab are ready', () => {
    expect(upstreamIntelligenceOpportunityDescription).toBe(
      '毛利账本和策略实验室完成前，不生成可执行建议；合格的 Pareto 候选仍只用于只读比较，不依据最低价直接触发切换。',
    )
    expect(upstreamIntelligenceOpportunityDescription).not.toContain(
      '质量连接、毛利账本和 shadow/dry-run 尚未完成前',
    )
  })
})
