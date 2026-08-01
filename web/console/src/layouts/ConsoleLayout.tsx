import { useMemo, useState, useSyncExternalStore } from 'react'
import { Link, Outlet, useLocation } from 'react-router'
import { ProLayout } from '@ant-design/pro-components'
import { Badge, Dropdown, Tag, Tooltip } from 'antd'
import {
  AppstoreOutlined,
  AuditOutlined,
  BellOutlined,
  ClusterOutlined,
  DashboardOutlined,
  HeartOutlined,
  LogoutOutlined,
  SettingOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { useCurrentUser, useHealth } from '../api/hooks'
import {
  clearSession,
  getActiveRole,
  getStoredUser,
  onActiveRoleChange,
  setActiveRole,
  type AuthUser,
  type UserRole,
} from '../api/auth'
import { endpoints } from '../api/endpoints'
import { LocaleSwitcher } from '../components/LocaleSwitcher'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { menuForRole, type MenuIcon, type MenuNode } from './consoleMenu'

type LayoutMenuNode = Omit<MenuNode, 'icon' | 'routes'> & {
  icon?: React.ReactNode
  routes?: LayoutMenuNode[]
}

const menuIcons: Record<MenuIcon, React.ReactNode> = {
  audit: <AuditOutlined />,
  connector: <ClusterOutlined />,
  dashboard: <DashboardOutlined />,
  health: <HeartOutlined />,
  instances: <AppstoreOutlined />,
  notifications: <BellOutlined />,
  platform: <SettingOutlined />,
  users: <UserOutlined />,
}

function withIcons(nodes: MenuNode[]): LayoutMenuNode[] {
  return nodes.map((node) => ({
    ...node,
    name: t(node.i18nKey, node.name),
    icon: node.icon ? menuIcons[node.icon] : undefined,
    routes: node.routes ? withIcons(node.routes) : undefined,
  }))
}

const roleTag: Record<UserRole, { i18nKey: string; fallback: string; color: string }> = {
  admin: { i18nKey: 'consoleShell.roles.admin', fallback: '管理员', color: 'gold' },
  client: { i18nKey: 'consoleShell.roles.client', fallback: '实例托管', color: 'blue' },
  supplier: { i18nKey: 'consoleShell.roles.supplier', fallback: '资源供给', color: 'purple' },
}

function HealthDot() {
  const { data, isError } = useHealth()
  const ok = !isError && data?.status === 'ok'
  const status = ok
    ? t('consoleShell.coreHealthy', 'Core 正常')
    : t('consoleShell.coreError', 'Core 异常')
  return (
    <Tooltip
      title={
        ok
          ? `${status} · ${data?.serverTime ?? ''}`
          : t('consoleShell.coreUnreachable', 'Core 不可达')
      }
    >
      <span className="e2m-header-health" role="status" aria-label={status}>
        <Badge status={ok ? 'success' : 'error'} text={status} />
      </span>
    </Tooltip>
  )
}

function UserMenu({
  user,
  activeRole,
  onRoleChange,
}: {
  user: AuthUser | null
  activeRole?: UserRole
  onRoleChange: (role: UserRole) => void
}) {
  if (!user) return null
  const logout = async () => {
    try {
      await endpoints.logout()
    } finally {
      clearSession()
      window.location.assign('/login')
    }
  }
  const items = [
    ...(user.roles.length > 1
      ? user.roles.map((role) => ({
          key: `role:${role}`,
          label: roleTag[role] ? t(roleTag[role].i18nKey, roleTag[role].fallback) : role,
          onClick: () => onRoleChange(role),
        }))
      : []),
    {
      type: 'divider' as const,
    },
    {
      key: 'logout',
      icon: <LogoutOutlined />,
      label: t('consoleShell.logout', '退出登录'),
      onClick: logout,
    },
  ]
  const active = activeRole ? roleTag[activeRole] : undefined
  const activeText = active ? t(active.i18nKey, active.fallback) : ''
  const label = `${user.display_name || user.email}${activeText ? ` · ${activeText}` : ''}`
  return (
    <Dropdown menu={{ items }}>
      <button type="button" className="e2m-header-user" aria-label={label}>
        <UserOutlined />{' '}
        <span className="e2m-header-user-name">{user.display_name || user.email}</span>{' '}
        {active ? (
          <Tag className="e2m-header-user-role" color={active.color}>
            {activeText}
          </Tag>
        ) : null}
      </button>
    </Dropdown>
  )
}

export default function ConsoleLayout() {
  const location = useLocation()
  const [collapsed, setCollapsed] = useState(false)
  const localeVersion = useLocaleVersion()
  const { data: freshUser } = useCurrentUser()
  const user = freshUser ?? getStoredUser()
  const activeRole = useSyncExternalStore(
    onActiveRoleChange,
    () => getActiveRole(user),
    () => getActiveRole(user),
  )

  const visibleMenu = useMemo(() => {
    // t() reads the current global locale; the version makes that implicit
    // input explicit so menu names and ProLayout's page title update live.
    void localeVersion
    return withIcons(menuForRole(activeRole))
  }, [activeRole, localeVersion])

  const changeRole = (role: UserRole) => {
    setActiveRole(role)
  }

  return (
    <ProLayout
      title="E2M Ops"
      logo={false}
      layout="mix"
      fixedHeader
      fixSiderbar
      collapsed={collapsed}
      onCollapse={setCollapsed}
      location={{ pathname: location.pathname }}
      route={{ path: '/', routes: visibleMenu }}
      menuItemRender={(item, dom) => {
        const node = item as { routes?: unknown[] }
        if (node.routes?.length) return dom
        return (
          <Link className="e2m-menu-item-link" to={item.path ?? '/'}>
            {dom}
          </Link>
        )
      }}
      actionsRender={() => [
        <HealthDot key="health" />,
        <LocaleSwitcher key="locale" />,
        <UserMenu key="user" user={user} activeRole={activeRole} onRoleChange={changeRole} />,
      ]}
    >
      <Outlet />
    </ProLayout>
  )
}
