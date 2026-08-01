import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, useSearchParams } from 'react-router'
import { EvidenceDrawer } from '../components/intelligence/EvidenceDrawer'
import { setLocale } from '../i18n'
import { EvidenceButton } from './UpstreamIntelligence'

const { evidenceQuery } = vi.hoisted(() => ({ evidenceQuery: vi.fn() }))

vi.mock('../api/upstreamIntelligenceHooks', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/upstreamIntelligenceHooks')>()
  return { ...actual, useIntelligenceEvidence: evidenceQuery }
})

beforeEach(() => {
  setLocale('zh')
  evidenceQuery.mockReturnValue({ isLoading: false, error: null, data: undefined })
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  )
})

afterEach(() => {
  cleanup()
  evidenceQuery.mockReset()
  vi.unstubAllGlobals()
  setLocale('zh')
})

function EvidenceKeyboardFlow() {
  const [search, setSearch] = useSearchParams()
  const evidenceId = search.get('evidence_id') || undefined
  const updateEvidence = (nextEvidenceId?: string) => {
    const next = new URLSearchParams(search)
    if (nextEvidenceId) next.set('evidence_id', nextEvidenceId)
    else next.delete('evidence_id')
    setSearch(next)
  }

  return (
    <>
      <EvidenceButton evidenceId="change-evidence-3" onOpen={updateEvidence} />
      <output aria-label="current query">{search.toString()}</output>
      <EvidenceDrawer userId={2} evidenceId={evidenceId} onClose={() => updateEvidence()} />
    </>
  )
}

describe('EvidenceButton', () => {
  it('opens the selected evidence exactly once when focused and activated with Enter', () => {
    const onOpen = vi.fn()
    render(<EvidenceButton evidenceId="change-evidence-1" onOpen={onOpen} />)
    const button = screen.getByRole('button', { name: '查看证据' })

    button.focus()
    const dispatched = fireEvent.keyDown(button, { key: 'Enter', code: 'Enter', charCode: 13 })
    fireEvent.keyUp(button, { key: 'Enter', code: 'Enter', charCode: 13 })

    expect(document.activeElement).toBe(button)
    expect(dispatched).toBe(false)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith('change-evidence-1')
  })

  it('preserves pointer activation and ignores unrelated keys', () => {
    const onOpen = vi.fn()
    render(<EvidenceButton evidenceId="change-evidence-2" onOpen={onOpen} />)
    const button = screen.getByRole('button', { name: '查看证据' })

    fireEvent.keyDown(button, { key: 'ArrowDown', code: 'ArrowDown' })
    expect(onOpen).not.toHaveBeenCalled()

    fireEvent.click(button)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen).toHaveBeenCalledWith('change-evidence-2')
  })

  it('opens the real Drawer through URL state with Enter and restores focus after Escape', async () => {
    render(
      <MemoryRouter initialEntries={['/upstream-intelligence?tab=changes&user_id=2&window=24h']}>
        <EvidenceKeyboardFlow />
      </MemoryRouter>,
    )
    const button = screen.getByRole('button', { name: '查看证据' })
    const query = screen.getByRole('status', { name: 'current query' })

    button.focus()
    fireEvent.keyDown(button, { key: 'Enter', code: 'Enter', keyCode: 13, which: 13 })
    fireEvent.keyUp(button, { key: 'Enter', code: 'Enter', keyCode: 13, which: 13 })

    await waitFor(() => {
      expect(new URLSearchParams(query.textContent ?? '').get('evidence_id')).toBe(
        'change-evidence-3',
      )
      expect(screen.getByRole('dialog', { name: '规范化证据' })).toBeTruthy()
    })

    const drawer = screen.getByRole('dialog', { name: '规范化证据' })
    const drawerRoot = drawer.closest('.ant-drawer') as HTMLElement | null
    expect(drawerRoot).toBeTruthy()
    const closeButton = screen.getByRole('button', { name: 'Close' })
    closeButton.focus()
    expect(drawerRoot?.contains(document.activeElement)).toBe(true)

    fireEvent.keyDown(closeButton, {
      key: 'Escape',
      code: 'Escape',
      keyCode: 27,
      which: 27,
    })

    await waitFor(
      () => {
        expect(new URLSearchParams(query.textContent ?? '').has('evidence_id')).toBe(false)
        expect(screen.queryByRole('dialog', { name: '规范化证据' })).toBeNull()
        expect(document.activeElement).toBe(button)
      },
      { timeout: 2000 },
    )
  })
})
