import type { UserRole } from '../api/auth'
import { consoleFeatureFlags } from '../config/featureFlags'

export type MenuIcon =
  | 'audit'
  | 'connector'
  | 'dashboard'
  | 'health'
  | 'instances'
  | 'notifications'
  | 'platform'
  | 'users'

// MenuSection splits the sidebar the way sub2api does: the admin section
// holds operator-only surfaces, the common section holds modules every role
// uses. Administrators see both (admin on top); other roles see only the
// common modules as a flat list.
export type MenuSection = 'admin' | 'common'

export interface MenuNode {
  path: string
  name: string
  i18nKey: string
  icon?: MenuIcon
  roles?: UserRole[]
  section?: MenuSection
  routes?: MenuNode[]
}

const adminSectionMenu: MenuNode[] = [
  {
    path: '/instances',
    name: '客户实例',
    i18nKey: 'consoleMenu.customerInstances',
    icon: 'instances',
    roles: ['admin'],
  },
  ...(consoleFeatureFlags.payments
    ? ([
        {
          path: '/redeem-codes',
          name: '兑换码管理',
          i18nKey: 'consoleMenu.redeemCodes',
          icon: 'audit',
          roles: ['admin'],
        },
        {
          path: '/payment-orders',
          name: '收款订单（试验）',
          i18nKey: 'consoleMenu.experimentalPaymentOrders',
          icon: 'audit',
          roles: ['admin'],
        },
      ] satisfies MenuNode[])
    : []),
  {
    path: '/users',
    name: '用户与权限',
    i18nKey: 'consoleMenu.usersPermissions',
    icon: 'users',
    roles: ['admin'],
  },
  {
    path: '/system-settings',
    name: '系统设置',
    i18nKey: 'consoleMenu.systemSettings',
    icon: 'platform',
    roles: ['admin'],
  },
]

const commonSectionMenu: MenuNode[] = [
  {
    path: '/',
    name: '平台总览',
    i18nKey: 'consoleMenu.platformOverview',
    icon: 'dashboard',
    roles: ['admin'],
  },
  {
    path: '/',
    name: '服务总览',
    i18nKey: 'consoleMenu.serviceOverview',
    icon: 'dashboard',
    roles: ['client'],
  },
  {
    path: '/platform-distribution',
    name: '平台分发',
    i18nKey: 'consoleMenu.platformDistribution',
    icon: 'platform',
    roles: ['admin', 'client'],
  },
  {
    path: '/model-market',
    name: '模型市场',
    i18nKey: 'consoleMenu.platformModelMarket',
    icon: 'platform',
    roles: ['admin', 'client'],
  },
  ...(consoleFeatureFlags.payments
    ? ([
        {
          path: '/recharge',
          name: '余额充值',
          i18nKey: 'consoleMenu.recharge',
          icon: 'platform',
          roles: ['admin', 'client'],
        },
        {
          path: '/redeem',
          name: '兑换码',
          i18nKey: 'consoleMenu.redeem',
          icon: 'platform',
          roles: ['admin', 'client'],
        },
      ] satisfies MenuNode[])
    : []),
  {
    path: '/instances',
    name: '托管实例',
    i18nKey: 'consoleMenu.managedInstances',
    icon: 'instances',
    roles: ['client'],
  },
  {
    path: '/connectors',
    name: '连接器与接入状态',
    i18nKey: 'consoleMenu.connectorsAccessStatus',
    icon: 'connector',
    roles: ['admin', 'client'],
  },
  {
    path: '/pool-health',
    name: '自有号池健康',
    i18nKey: 'consoleMenu.ownedPoolHealth',
    icon: 'health',
    roles: ['admin', 'client'],
  },
  {
    path: '/notifications',
    name: '通知设置',
    i18nKey: 'consoleMenu.notificationSettings',
    icon: 'notifications',
    roles: ['admin', 'client'],
  },
  {
    path: '/audits',
    name: '审计日志',
    i18nKey: 'consoleMenu.auditLog',
    icon: 'audit',
    roles: ['admin', 'client'],
  },
]

function forRole(nodes: MenuNode[], activeRole: UserRole): MenuNode[] {
  return nodes.filter((node) => !node.roles || node.roles.includes(activeRole))
}

// menuForRole returns the sidebar tree. Administrators get the two labelled
// sections (admin operations on top, common modules below); every other role
// gets the flat common list.
export function menuForRole(activeRole?: UserRole): MenuNode[] {
  if (!activeRole) return []
  const common = forRole(commonSectionMenu, activeRole)
  if (activeRole !== 'admin') return common
  return [
    {
      path: '/section/admin',
      name: '平台管理',
      i18nKey: 'consoleMenu.sections.admin',
      section: 'admin',
      routes: forRole(adminSectionMenu, activeRole),
    },
    {
      path: '/section/common',
      name: '通用功能',
      i18nKey: 'consoleMenu.sections.common',
      section: 'common',
      routes: common,
    },
  ]
}

// flatMenuPaths lists every leaf path a role can reach, section-independent.
export function flatMenuPaths(activeRole?: UserRole): string[] {
  const collect = (nodes: MenuNode[]): string[] =>
    nodes.flatMap((node) => (node.routes?.length ? collect(node.routes) : [node.path]))
  return collect(menuForRole(activeRole))
}
