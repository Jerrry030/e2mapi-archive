import { useMemo, useState } from 'react'
import {
  ModalForm,
  PageContainer,
  ProForm,
  ProFormDependency,
  ProFormSelect,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Popconfirm, Tag, Typography } from 'antd'
import { CopyOutlined, DeleteOutlined, KeyOutlined, PlusOutlined } from '@ant-design/icons'
import {
  useActiveRoleUser,
  useDeleteSecret,
  useSecrets,
  useUsers,
  useUpsertSecret,
} from '../api/hooks'
import { currentUserId, getActiveRole, isPlatformAdmin } from '../api/auth'
import { UserSelect } from '../components/fields'
import { EmptyTeach } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type { SecretKind, SecretRef } from '../api/types'
import {
  allowedSecretKinds,
  eligibleSecretOwners,
  visibleSecretsForRole,
} from './resourceFormAccess'
import { isManagedPersonalNotificationRef } from './notificationCredentials'

const kindOptions: { value: SecretKind; label: string }[] = [
  { value: 'notification', label: '通知 Webhook 地址' },
  { value: 'upstream', label: '上游业务/API 提供方凭证' },
  { value: 'proxy', label: '代理凭证' },
]

const kindColor: Record<SecretKind, string> = {
  notification: 'green',
  upstream: 'purple',
  proxy: 'orange',
}

interface SecretFormValues {
  user_id: number
  kind: SecretKind
  name: string
  value: string
}

export default function Secrets() {
  const user = useActiveRoleUser()
  const activeRole = getActiveRole(user)
  const platform = isPlatformAdmin(user)
  const scopedUserId = !platform ? currentUserId(user) : undefined
  const [userId, setUserId] = useState<number | undefined>(scopedUserId)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<SecretRef | null>(null)
  const [form] = ProForm.useForm<SecretFormValues>()

  const secrets = useSecrets(userId)
  const users = useUsers(platform)
  const upsert = useUpsertSecret()
  const remove = useDeleteSecret()
  const allowedKinds = allowedSecretKinds(activeRole)
  const availableKindOptions = kindOptions.filter((option) => allowedKinds.includes(option.value))
  const defaultKind: SecretKind = activeRole === 'client' ? 'notification' : 'upstream'
  const visibleSecrets = visibleSecretsForRole(secrets.data ?? [], activeRole).filter(
    (secret) => !isManagedPersonalNotificationRef(secret.ref),
  )

  const userName = useMemo(() => {
    const map = new Map(
      (users.data ?? []).map((item) => [item.id, item.display_name || item.email]),
    )
    return (id: number) => map.get(id) ?? String(id)
  }, [users.data])

  const openCreate = () => {
    setEditing(null)
    setOpen(true)
  }

  const openRotate = (item: SecretRef) => {
    setEditing(item)
    setOpen(true)
  }

  const columns: ProColumns<SecretRef>[] = [
    { title: '名称', dataIndex: 'name', width: 180, ellipsis: true },
    {
      title: '类型',
      dataIndex: 'kind',
      width: 140,
      render: (_, r) => (
        <Tag color={kindColor[r.kind]}>
          {kindOptions.find((k) => k.value === r.kind)?.label ?? r.kind}
        </Tag>
      ),
    },
    ...(platform
      ? [
          {
            title: '账号',
            dataIndex: 'user_id',
            width: 140,
            ellipsis: true,
            render: (_: unknown, r: SecretRef) => userName(r.user_id),
          },
          {
            title: '内部引用',
            dataIndex: 'ref',
            width: 240,
            ellipsis: true,
            render: (_: unknown, r: SecretRef) => (
              <Typography.Text copyable={{ text: r.ref, icon: <CopyOutlined /> }} code>
                {r.ref}
              </Typography.Text>
            ),
          },
        ]
      : []),
    {
      title: '操作',
      valueType: 'option',
      width: 180,
      render: (_, r) => [
        <a key="rotate" onClick={() => openRotate(r)}>
          更新明文
        </a>,
        <Popconfirm
          key="delete"
          title="删除这个凭证？"
          description="删除后，使用它的通知、上游业务或代理配置会在下次执行时失败。"
          onConfirm={() => remove.mutate({ userId: r.user_id, ref: r.ref })}
        >
          <a style={{ color: '#ff4d4f' }}>
            <DeleteOutlined /> 删除
          </a>
        </Popconfirm>,
      ],
    },
  ]

  const initialValues: Partial<SecretFormValues> = editing
    ? { user_id: editing.user_id, kind: editing.kind, name: editing.name }
    : { user_id: scopedUserId, kind: defaultKind }

  const pageCopy =
    activeRole === 'client'
      ? {
          subtitle: '安全保存通知使用的 Webhook 地址，页面不会回显明文',
          notice:
            '这里只保存消息通知使用的 HTTPS Webhook 地址。平台交付 Key 由系统自动管理，网关管理密钥只保存在连接器本地。',
          empty: '还没有 Webhook 地址。先保存一个 HTTPS Webhook。',
        }
      : activeRole === 'supplier'
        ? {
            subtitle: '保存供给资源使用的上游业务/API 提供方与代理凭证，页面不会回显明文',
            notice:
              '这里只保存供给资源使用的上游业务/API 提供方凭证和代理凭证。网关管理密钥仍只保存在连接器本地。',
            empty: '还没有供给凭证。先保存上游业务/API 提供方或代理凭证。',
          }
        : {
            subtitle: '仅保存通知、上游业务账号/API 提供方或代理凭证，页面不会回显明文',
            notice:
              'Core Vault 仅保存通知 Webhook、上游业务账号/API 提供方凭证和代理凭证。网关管理密钥只保存在对应连接器的本地配置中，不会上传到 Core。后续表单会直接选择这些凭证，不需要手动复制内部引用。',
            empty: '还没有凭证。先保存通知、上游业务账号/API 提供方或代理凭证。',
          }

  return (
    <PageContainer title="凭证管理" subTitle={pageCopy.subtitle}>
      <Alert type="info" showIcon style={{ marginBottom: 16 }} message={pageCopy.notice} />
      <ProTable<SecretRef>
        rowKey="ref"
        search={false}
        loading={secrets.isLoading}
        dataSource={visibleSecrets}
        columns={columns}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => secrets.refetch() }}
        toolBarRender={() => [
          <UserSelect key="user" value={userId} onChange={setUserId} placeholder="全部账号" />,
          <Button key="new" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            写入凭证
          </Button>,
        ]}
        locale={{
          emptyText: (
            <EmptyTeach
              title={pageCopy.empty}
              action={
                <Button type="primary" icon={<KeyOutlined />} onClick={openCreate}>
                  写入第一个凭证
                </Button>
              }
            />
          ),
        }}
      />

      <ModalForm<SecretFormValues>
        key={editing?.ref ?? `new-${userId ?? scopedUserId ?? 'all'}`}
        title={editing ? `更新凭证明文 · ${editing.name}` : '写入凭证'}
        open={open}
        onOpenChange={setOpen}
        form={form}
        modalProps={{ destroyOnHidden: true }}
        initialValues={initialValues}
        submitter={{
          searchConfig: { submitText: editing ? '更新明文' : '保存到 vault', resetText: '重置' },
          submitButtonProps: { loading: upsert.isPending },
        }}
        onFinish={async (values) => {
          await upsert.mutateAsync({
            ...values,
            user_id: values.user_id ?? editing?.user_id ?? userId ?? scopedUserId ?? 0,
          })
          return true
        }}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="保存后页面不会回显明文。更新已有引用时，只会覆盖 vault 中的明文值。"
        />
        <ProFormSelect
          name="kind"
          label="用途"
          rules={[{ required: true }]}
          options={availableKindOptions}
          fieldProps={{
            onChange: (kind: SecretKind) => {
              if (!platform || editing) return
              const selectedUserID = form.getFieldValue('user_id')
              const stillEligible = eligibleSecretOwners(users.data ?? [], kind).some(
                (candidate) => candidate.id === selectedUserID,
              )
              if (!stillEligible) form.setFieldValue('user_id', undefined)
            },
          }}
        />
        {platform ? (
          <ProFormDependency name={['kind']}>
            {({ kind }: { kind?: SecretKind }) => (
              <ProFormSelect
                name="user_id"
                label="账号"
                rules={[{ required: true }]}
                disabled={!!editing}
                options={eligibleSecretOwners(users.data ?? [], kind ?? defaultKind).map(
                  (item) => ({
                    value: item.id,
                    label: item.display_name || item.email,
                  }),
                )}
              />
            )}
          </ProFormDependency>
        ) : null}
        <ProFormText
          name="name"
          label="名称"
          rules={[{ required: true }]}
          placeholder="例如：OpenAI API 业务凭证"
        />
        <ProFormTextArea
          name="value"
          label="内容"
          rules={[{ required: true }]}
          fieldProps={{ rows: 5 }}
          placeholder="粘贴 webhook URL、上游业务账号 token、API 提供方密钥、代理地址或 JSON 凭证"
        />
      </ModalForm>
    </PageContainer>
  )
}
