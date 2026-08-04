import { useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import { EditOutlined, KeyOutlined, PlusOutlined } from '@ant-design/icons'
import { PageContainer } from '@ant-design/pro-components'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '../api/client'
import { endpoints, arr, type CreateUserInput, type UpdateUserInput } from '../api/endpoints'
import { friendlyErrorMessage } from '../api/errors'
import {
  clearSession,
  getStoredUser,
  isPlatformAdmin,
  setStoredUser,
  type AuthUser,
  type UserRole,
} from '../api/auth'
import { AbsoluteTime } from '../components/common'

const roleLabel: Record<UserRole, { text: string; color: string }> = {
  admin: { text: '管理员', color: 'gold' },
  client: { text: '托管能力', color: 'blue' },
  supplier: { text: '供给能力', color: 'purple' },
}

const roleOptions = [
  { value: 'admin', label: '管理员（全局管理）' },
  { value: 'client', label: '托管能力（接入实例 / 连接器）' },
  { value: 'supplier', label: '供给能力（登记供给）' },
]

interface ResetPasswordValues {
  password: string
  confirm_password: string
}

function AccountRoleSelect({
  value,
  onChange,
  disabled,
}: {
  value?: UserRole[]
  onChange: (roles: UserRole[]) => void
  disabled?: boolean
}) {
  return (
    <Select
      mode="multiple"
      value={value}
      disabled={disabled}
      options={roleOptions}
      onChange={(next: UserRole[]) => {
        if (next.includes('admin') && next.length > 1) {
          onChange(['admin'])
          return
        }
        onChange(next)
      }}
    />
  )
}

export default function Users() {
  const me = getStoredUser()
  const meId = me?.id
  const qc = useQueryClient()
  const { message } = App.useApp()
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<AuthUser | null>(null)
  const [resetTarget, setResetTarget] = useState<AuthUser | null>(null)
  const [createForm] = Form.useForm<CreateUserInput>()
  const [editForm] = Form.useForm<UpdateUserInput>()
  const [passwordForm] = Form.useForm<ResetPasswordValues>()

  const users = useQuery({
    queryKey: ['users'],
    queryFn: () => endpoints.listUsers().then(arr),
    enabled: isPlatformAdmin(me),
  })

  const enabledAdminCount = useMemo(
    () => (users.data ?? []).filter((user) => user.enabled && user.roles.includes('admin')).length,
    [users.data],
  )
  const editingSelf = editing?.id === meId
  const editingLastAdmin = Boolean(
    editing?.enabled && editing.roles.includes('admin') && enabledAdminCount <= 1,
  )
  const accountAccessLocked = editingSelf || editingLastAdmin

  const create = useMutation({
    mutationFn: endpoints.createUser,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      message.success('用户已创建')
      setCreateOpen(false)
      createForm.resetFields()
    },
    onError: (e) => message.error(friendlyErrorMessage(e)),
  })

  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: UpdateUserInput }) =>
      endpoints.updateUser(id, body),
    onSuccess: (updated) => {
      qc.invalidateQueries({ queryKey: ['users'] })
      qc.invalidateQueries({ queryKey: ['audits'] })
      if (updated.id === meId) setStoredUser(updated)
      if (updated.id === meId) qc.setQueryData(['auth', 'me'], updated)
      message.success('用户资料已更新')
      setEditing(null)
      editForm.resetFields()
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'stale_user') {
        qc.invalidateQueries({ queryKey: ['users'] })
        setEditing(null)
        message.error('用户资料已被其他管理员更新，列表已刷新，请重新编辑。')
        return
      }
      message.error(friendlyErrorMessage(e))
    },
  })

  const resetPassword = useMutation({
    mutationFn: ({ id, password }: { id: number; password: string }) =>
      endpoints.resetUserPassword(id, password),
    onSuccess: (_data, variables) => {
      qc.invalidateQueries({ queryKey: ['audits'] })
      if (variables.id === meId) {
        clearSession()
        window.location.assign('/login')
        return
      }
      message.success('密码已重置，目标用户的旧会话已失效')
      setResetTarget(null)
      passwordForm.resetFields()
    },
    onError: (e) => message.error(friendlyErrorMessage(e)),
  })

  const openEditor = (user: AuthUser) => {
    if (user.deactivation_status === 'draining') {
      message.warning('该客户正在安全撤流，完成前不能重复编辑。')
      return
    }
    setEditing(user)
    editForm.setFieldsValue({
      email: user.email,
      display_name: user.display_name,
      roles: user.roles,
      enabled: user.enabled,
      expected_updated_at: user.updated_at ?? '',
      platform_concurrency: user.platform_concurrency ?? 0,
      platform_rpm: user.platform_rpm ?? 0,
    })
  }

  if (!isPlatformAdmin(me)) {
    return (
      <PageContainer title="用户与权限">
        <Typography.Text type="secondary">仅管理员可管理用户。</Typography.Text>
      </PageContainer>
    )
  }

  return (
    <PageContainer
      title="用户与权限"
      subTitle="维护登录资料、账号能力、启停状态与密码"
      extra={[
        <Button
          key="new"
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCreateOpen(true)}
        >
          新建用户
        </Button>,
      ]}
    >
      <Table<AuthUser>
        rowKey="id"
        loading={users.isLoading}
        dataSource={users.data ?? []}
        pagination={{ defaultPageSize: 20, showSizeChanger: false }}
        scroll={{ x: 'max-content' }}
        columns={[
          {
            title: '用户',
            dataIndex: 'email',
            width: 240,
            ellipsis: true,
            render: (_, user) => (
              <Space size={6}>
                <Typography.Text>{user.email}</Typography.Text>
                {user.id === meId ? <Tag color="blue">当前账号</Tag> : null}
              </Space>
            ),
          },
          {
            title: '用户名 / 昵称',
            dataIndex: 'display_name',
            width: 180,
            ellipsis: true,
            render: (value) => value || '-',
          },
          {
            title: '账号能力',
            dataIndex: 'roles',
            width: 230,
            render: (value: UserRole[]) =>
              (value ?? []).map((role) => {
                const item = roleLabel[role]
                return item ? (
                  <Tag key={role} color={item.color}>
                    {item.text}
                  </Tag>
                ) : null
              }),
          },
          {
            title: '状态',
            dataIndex: 'enabled',
            width: 130,
            render: (value: boolean, user) => {
              switch (user.deactivation_status) {
                case 'draining':
                  return <Tag color="processing">托管停用处理中</Tag>
                case 'failed':
                  return <Tag color="error">托管撤流失败</Tag>
                case 'completed':
                  return <Tag>{value ? '托管已停用' : '已停用'}</Tag>
                default:
                  return value ? <Tag color="green">启用</Tag> : <Tag color="red">停用</Tag>
              }
            },
          },
          {
            title: '更新时间',
            dataIndex: 'updated_at',
            width: 170,
            render: (value) => <AbsoluteTime value={value} />,
          },
          {
            title: '编号',
            dataIndex: 'id',
            width: 80,
            render: (value) => <Typography.Text code>#{value}</Typography.Text>,
          },
          {
            title: '操作',
            key: 'actions',
            fixed: 'right',
            width: 190,
            render: (_, user) => (
              <Space size={0} className="e2m-table-actions">
                <Button
                  type="link"
                  size="small"
                  icon={<EditOutlined />}
                  disabled={user.deactivation_status === 'draining'}
                  onClick={() => openEditor(user)}
                >
                  编辑
                </Button>
                <Button
                  type="link"
                  size="small"
                  icon={<KeyOutlined />}
                  onClick={() => setResetTarget(user)}
                >
                  重置密码
                </Button>
              </Space>
            ),
          },
        ]}
      />

      <Modal
        title="新建用户"
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => createForm.submit()}
        confirmLoading={create.isPending}
        destroyOnHidden
        afterClose={() => createForm.resetFields()}
      >
        <Form<CreateUserInput>
          form={createForm}
          layout="vertical"
          initialValues={{ roles: ['client'] }}
          onFinish={(values) => create.mutate(values)}
        >
          <Form.Item name="email" label="邮箱" rules={[{ required: true, type: 'email' }]}>
            <Input placeholder="user@example.com" autoComplete="off" />
          </Form.Item>
          <Form.Item
            name="password"
            label="初始密码"
            rules={[{ required: true, min: 8, message: '至少 8 位' }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="display_name" label="用户名 / 昵称">
            <Input />
          </Form.Item>
          <Form.Item
            name="roles"
            label="账号能力"
            rules={[{ required: true, message: '至少选择一项账号能力' }]}
          >
            <AccountRoleSelect onChange={(roles) => createForm.setFieldValue('roles', roles)} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`编辑用户${editing ? ` - ${editing.email}` : ''}`}
        open={Boolean(editing)}
        onCancel={() => setEditing(null)}
        onOk={() => editForm.submit()}
        confirmLoading={update.isPending}
        destroyOnHidden
        afterClose={() => editForm.resetFields()}
      >
        {editing?.deactivation_status === 'draining' ? (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="客户正在安全撤流。所有线路确认撤销后，Connector 和会话才会被最终清理。"
          />
        ) : editing?.deactivation_status === 'failed' ? (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 16 }}
            message="上次撤流失败。点击保存可触发安全重试；Connector 会保留到所有线路确认撤销。"
          />
        ) : accountAccessLocked ? (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message={
              editingSelf
                ? '当前账号可以修改邮箱和昵称，但不能在自己的会话中停用或移除管理员权限。'
                : '必须保留至少一个启用的管理员账号。'
            }
          />
        ) : (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 16 }}
            message="修改账号能力或停用账号后，该用户的全部旧会话会立即失效。"
          />
        )}
        <Form<UpdateUserInput>
          form={editForm}
          layout="vertical"
          onFinish={(values) => {
            if (editing) update.mutate({ id: editing.id, body: values })
          }}
        >
          <Form.Item name="expected_updated_at" hidden>
            <Input />
          </Form.Item>
          <Form.Item name="email" label="邮箱" rules={[{ required: true, type: 'email' }]}>
            <Input disabled={editing?.deactivation_status === 'draining'} />
          </Form.Item>
          <Form.Item name="display_name" label="用户名 / 昵称">
            <Input disabled={editing?.deactivation_status === 'draining'} />
          </Form.Item>
          <Form.Item
            name="roles"
            label="账号能力"
            rules={[{ required: true, message: '至少选择一项账号能力' }]}
          >
            <AccountRoleSelect
              disabled={accountAccessLocked || editing?.deactivation_status === 'draining'}
              onChange={(roles) => editForm.setFieldValue('roles', roles)}
            />
          </Form.Item>
          <Form.Item name="enabled" label="登录状态" valuePropName="checked">
            <Switch
              disabled={accountAccessLocked || editing?.deactivation_status === 'draining'}
              checkedChildren="启用"
              unCheckedChildren="停用"
            />
          </Form.Item>
          <Form.Item
            name="platform_concurrency"
            label="平台并发上限"
            extra="同时进行中的平台请求数上限；0 表示不限制。"
          >
            <InputNumber min={0} max={1000000} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item
            name="platform_rpm"
            label="平台每分钟请求上限"
            extra="每分钟平台请求数上限（RPM）；0 表示不限制。"
          >
            <InputNumber min={0} max={1000000} style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`重置密码${resetTarget ? ` - ${resetTarget.email}` : ''}`}
        open={Boolean(resetTarget)}
        onCancel={() => setResetTarget(null)}
        onOk={() => passwordForm.submit()}
        confirmLoading={resetPassword.isPending}
        destroyOnHidden
        afterClose={() => passwordForm.resetFields()}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="保存后该用户的全部旧会话会立即失效，需要使用新密码重新登录。"
        />
        <Form<ResetPasswordValues>
          form={passwordForm}
          layout="vertical"
          onFinish={({ password }) => {
            if (resetTarget) resetPassword.mutate({ id: resetTarget.id, password })
          }}
        >
          <Form.Item
            name="password"
            label="新密码"
            rules={[{ required: true, min: 8, message: '至少 8 位' }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm_password"
            label="确认新密码"
            dependencies={['password']}
            rules={[
              { required: true, message: '请再次输入新密码' },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  return !value || getFieldValue('password') === value
                    ? Promise.resolve()
                    : Promise.reject(new Error('两次输入的密码不一致'))
                },
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
