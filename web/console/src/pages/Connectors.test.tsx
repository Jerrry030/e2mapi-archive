import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp } from 'antd'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setSession } from '../api/auth'
import type { Connector, ConnectorTask } from '../api/types'
import { setLocale } from '../i18n'
import Connectors from './Connectors'

const connector: Connector = {
  connector_id: 'connector-1',
  user_id: 7,
  instance_id: 'instance-1',
  name: 'Customer connector',
  status: 'online',
  version: '0.3.0',
  protocol_version: 3,
  created_at: '2026-07-27T01:00:00Z',
  updated_at: '2026-07-27T01:00:00Z',
}

const executingTask: ConnectorTask = {
  id: 'task-executing',
  instance_id: 'instance-1',
  connector_id: connector.connector_id,
  type: 'gateway.account.traffic_share.set',
  schema_version: 1,
  risk_level: 'L1',
  status: 'executing',
  attempts: 1,
  max_attempts: 3,
  target_account_id: 'account-a',
  target_traffic_share: 25,
  scheduling_fence: {
    scope: 'auto-switch/plan/plan-a',
    version: 7,
    sequence: 2,
  },
  available_at: '2026-07-27T01:00:00Z',
  expires_at: '2026-07-27T02:00:00Z',
  created_at: '2026-07-27T01:00:00Z',
  updated_at: '2026-07-27T01:01:00Z',
}

function response(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    text: async () => JSON.stringify(body),
  }
}

function mockMatchMedia() {
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
}

function mockAPI(task = executingTask) {
  let tasks = [task]
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/users')) {
      return response([{ id: 7, email: 'owner@example.com', roles: ['client'], enabled: true }])
    }
    if (url.startsWith('/api/v1/connectors?')) return response([connector])
    if (url.startsWith('/api/v1/instances?')) {
      return response([
        {
          id: 'instance-1',
          user_id: 7,
          name: 'Customer instance',
          kind: 'newapi',
          status: 'active',
          connector_id: connector.connector_id,
          created_at: '2026-07-27T01:00:00Z',
          updated_at: '2026-07-27T01:00:00Z',
        },
      ])
    }
    if (url.startsWith('/api/v1/connector-tasks?')) return response(tasks)
    if (
      url === '/api/v1/connector-tasks/task-executing/resolve-execution' &&
      init?.method === 'POST'
    ) {
      tasks = [{ ...task, status: 'failed' }]
      return response(tasks[0])
    }
    return response({ code: 'not_found', message: url }, 404)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderConnectors() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <AntdApp>
        <Connectors />
      </AntdApp>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  setLocale('en')
  mockMatchMedia()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  setLocale('zh')
})

describe('Connectors protocol-v3 execution resolution', () => {
  it('lets an owner see the executing status but exposes no resolution control', async () => {
    setSession('owner-token', {
      id: 7,
      email: 'owner@example.com',
      roles: ['client'],
      enabled: true,
    })
    const fetchMock = mockAPI()
    renderConnectors()

    expect(await screen.findByText('Outcome uncertain')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Resolve outcome' })).toBeNull()
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes('/resolve-execution')),
    ).toBe(false)
  })

  it('offers the platform admin a safe typed resolution and submits the exact request', async () => {
    setSession('admin-token', {
      id: 1,
      email: 'admin@example.com',
      roles: ['admin'],
      enabled: true,
    })
    const fetchMock = mockAPI()
    renderConnectors()

    fireEvent.click(await screen.findByRole('button', { name: 'Resolve outcome' }))
    const dialog = await screen.findByRole('dialog', { name: 'Resolve an uncertain execution' })
    expect(dialog.classList.contains('ant-modal')).toBe(true)
    expect(dialog.style.getPropertyValue('--ant-modal-xs-width')).toBe('calc(100vw - 16px)')
    expect(dialog.style.getPropertyValue('--ant-modal-sm-width')).toBe('640px')
    expect(within(dialog).getByText('This is an irreversible L3 terminal operation')).toBeTruthy()

    const nonceInput = within(dialog).getByLabelText('Execution nonce') as HTMLInputElement
    expect(nonceInput.type).toBe('password')
    expect(within(dialog).queryByRole('button', { name: /copy/i })).toBeNull()
    fireEvent.change(nonceInput, { target: { value: 'a'.repeat(43) } })
    fireEvent.change(within(dialog).getByLabelText('Verification note'), {
      target: { value: 'Independent gateway readback confirmed no mutation was applied.' },
    })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm and terminalize task' }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/connector-tasks/task-executing/resolve-execution',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            lease_nonce: 'a'.repeat(43),
            resolution: 'confirmed_not_applied',
            evidence_note: 'Independent gateway readback confirmed no mutation was applied.',
          }),
        }),
      ),
    )
    expect(await screen.findByText('The execution outcome was resolved and audited')).toBeTruthy()
  })

  it('keeps invalid sensitive evidence local and supports Escape dismissal', async () => {
    setSession('admin-token', {
      id: 1,
      email: 'admin@example.com',
      roles: ['admin'],
      enabled: true,
    })
    const fetchMock = mockAPI()
    renderConnectors()

    fireEvent.click(await screen.findByRole('button', { name: 'Resolve outcome' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Execution nonce'), {
      target: { value: 'a'.repeat(43) },
    })
    fireEvent.change(within(dialog).getByLabelText('Verification note'), {
      target: { value: 'Authorization: Bearer secret' },
    })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Confirm and terminalize task' }))

    expect(
      await within(dialog).findByText(
        'The note appears to contain a URL, header, token, or another secret; remove it first',
      ),
    ).toBeTruthy()
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes('/resolve-execution')),
    ).toBe(false)

    fireEvent.keyDown(document, { key: 'Escape', code: 'Escape' })
    await waitFor(() => expect(dialog.classList.contains('ant-zoom-leave')).toBe(true))
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes('/resolve-execution')),
    ).toBe(false)
  })
})
