import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ProColumns } from '@ant-design/pro-components'
import type { IntelligenceRate } from '../../api/upstreamIntelligence'
import { EffectiveRateLeaderboard } from './EffectiveRateLeaderboard'
import { setLocale } from '../../i18n'

const { table } = vi.hoisted(() => ({ table: vi.fn((_props: unknown) => null) }))

vi.mock('../LocalizedProTable', () => ({ LocalizedProTable: table }))

afterEach(() => {
  cleanup()
  table.mockClear()
  setLocale('zh')
})

describe('EffectiveRateLeaderboard', () => {
  setLocale('zh')
  it('does not offer a lossy Number-based client sort for decimal multipliers', () => {
    render(<EffectiveRateLeaderboard rates={[]} onEvidence={vi.fn()} />)

    const props = table.mock.calls[0]?.[0] as { columns: ProColumns<IntelligenceRate>[] }
    const multiplier = props.columns.find((column) => column.title === '有效倍率')
    expect(multiplier).toBeDefined()
    expect(multiplier).not.toHaveProperty('sorter')
  })

  it('makes the horizontally scrollable table region keyboard reachable', () => {
    render(<EffectiveRateLeaderboard rates={[]} onEvidence={vi.fn()} />)

    const region = screen.getByRole('region', { name: '有效倍率与价格明细，可横向滚动' })
    expect(region.tabIndex).toBe(0)
  })

  it('recomputes columns and the accessible region name after switching to English', () => {
    setLocale('en')
    render(<EffectiveRateLeaderboard rates={[]} onEvidence={vi.fn()} />)

    const props = table.mock.calls[0]?.[0] as { columns: ProColumns<IntelligenceRate>[] }
    expect(props.columns.some((column) => column.title === 'Effective multiplier')).toBe(true)
    expect(
      screen.getByRole('region', {
        name: 'Effective rate and price details, horizontally scrollable',
      }).tabIndex,
    ).toBe(0)
  })
})
