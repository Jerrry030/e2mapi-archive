import type { ReactNode } from 'react'
import { createBrowserRouter, Navigate } from 'react-router'
import type { UserRole } from './api/auth'
import { RequireAuth, RequireRole } from './components/RequireAuth'
import { consoleFeatureFlags } from './config/featureFlags'
import ConsoleLayout from './layouts/ConsoleLayout'
import Audits from './pages/Audits'
import Connectors from './pages/Connectors'
import InstanceAccounts from './pages/InstanceAccounts'
import Instances from './pages/Instances'
import Login from './pages/Login'
import Notifications from './pages/Notifications'
import Overview from './pages/Overview'
import PaymentOrders from './pages/PaymentOrders'
import PaymentResult from './pages/PaymentResult'
import PlatformDistribution from './pages/PlatformDistribution'
import PoolHealth from './pages/PoolHealth'
import Recharge from './pages/Recharge'
import Register from './pages/Register'
import SystemSettings from './pages/SystemSettings'
import Users from './pages/Users'

const platform: UserRole[] = ['admin']
const owners: UserRole[] = ['admin', 'client']

function rolePage(roles: UserRole[], element: ReactNode) {
  return <RequireRole roles={roles}>{element}</RequireRole>
}

export const router = createBrowserRouter([
  { path: '/login', element: <Login /> },
  { path: '/register', element: <Register /> },
  {
    path: '/',
    element: (
      <RequireAuth>
        <ConsoleLayout />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <Overview /> },
      { path: 'platform-distribution', element: rolePage(owners, <PlatformDistribution />) },
      { path: 'instances', element: rolePage(owners, <Instances />) },
      { path: 'instances/:id/accounts', element: rolePage(owners, <InstanceAccounts />) },
      { path: 'connectors', element: rolePage(owners, <Connectors />) },
      { path: 'pool-health', element: rolePage(owners, <PoolHealth />) },
      { path: 'notifications', element: rolePage(owners, <Notifications />) },
      { path: 'audits', element: rolePage(owners, <Audits />) },
      { path: 'users', element: rolePage(platform, <Users />) },
      { path: 'system-settings', element: rolePage(platform, <SystemSettings />) },
      ...(consoleFeatureFlags.payments
        ? [
            { path: 'recharge', element: rolePage(owners, <Recharge />) },
            { path: 'payment/success', element: rolePage(owners, <PaymentResult outcome="success" />) },
            {
              path: 'payment/cancelled',
              element: rolePage(owners, <PaymentResult outcome="cancelled" />),
            },
            { path: 'payment-orders', element: rolePage(platform, <PaymentOrders />) },
          ]
        : []),
      { path: '*', element: <Navigate to="/" replace /> },
    ],
  },
])
