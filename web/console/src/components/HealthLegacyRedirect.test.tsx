import { afterEach, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { RequireRole } from './RequireAuth'
import { setSession, type AuthUser } from '../api/auth'
import { HealthLegacyRedirect } from './HealthLegacyRedirect'

const user: AuthUser = {
  id: 7,
  email: 'user@example.com',
  roles: ['client'],
  enabled: true,
}

function HealthLocation() {
  const location = useLocation()
  return <div>{`health:${new URLSearchParams(location.search).get('view')}`}</div>
}

function renderLegacyRoute() {
  return render(
    <MemoryRouter initialEntries={['/health-check']}>
      <Routes>
        <Route path="/" element={<div>home</div>} />
        <Route
          path="/health-check"
          element={
            <RequireRole roles={['admin', 'client']}>
              <HealthLegacyRedirect />
            </RequireRole>
          }
        />
        <Route path="/pool-health" element={<HealthLocation />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(() => {
  localStorage.clear()
})

describe('legacy health route', () => {
  it('redirects client users to the health overview', async () => {
    setSession('token', user)

    renderLegacyRoute()

    expect(await screen.findByText('health:summary')).toBeTruthy()
  })

  it('keeps supplier users out of the owner health surface', async () => {
    setSession('token', { ...user, roles: ['supplier'] })

    renderLegacyRoute()

    expect(await screen.findByText('home')).toBeTruthy()
  })
})
