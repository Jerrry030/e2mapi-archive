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

export interface MenuNode {
  path: string
  name: string
  i18nKey: string
  icon?: MenuIcon
  roles?: UserRole[]
  routes?: MenuNode[]
}

export const consoleMenu: MenuNode[] = [
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
    path: '/instances',
    name: '客户实例',
    i18nKey: 'consoleMenu.customerInstances',
    icon: 'instances',
    roles: ['admin'],
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
]

export function menuForRole(activeRole?: UserRole): MenuNode[] {
  if (!activeRole) return []
  return consoleMenu.filter((node) => !node.roles || node.roles.includes(activeRole))
}
