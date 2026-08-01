import { useCallback, useMemo, useState } from 'react'
import { DrawerForm, PageContainer, ProFormDigit, ProFormSelect } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Modal, Popconfirm, Space, Tag, Tooltip, Typography } from 'antd'
import { CopyOutlined, DisconnectOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import {
  useActiveRoleUser,
  useConnectorTasks,
  useConnectors,
  useCreateConnectorEnrollment,
  useInstances,
  useRevokeConnector,
  useResolveConnectorTaskExecution,
  useRotateConnectorToken,
  useUsers,
} from '../api/hooks'
import { canWriteOwner, currentUserId, isPlatformAdmin } from '../api/auth'
import { AbsoluteTime, RelativeTime } from '../components/common'
import { UserSelect } from '../components/fields'
import { ConnectorInstallModal } from '../components/ConnectorInstallGuide'
import { ConnectorExecutionResolutionModal } from '../components/ConnectorExecutionResolutionModal'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { RiskLevelTag } from '../components/tags'
import type {
  Connector,
  ConnectorTask,
  CreateConnectorEnrollmentInput,
  CreateConnectorEnrollmentResult,
  RotateConnectorTokenResult,
} from '../api/types'
import { connectorErrorLabel, connectorTaskStatusLabel, connectorTaskTypeLabel, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

function ConnectorStatusTag({ status }: { status: Connector['status'] }) {
  const color = status === 'online' ? 'green' : status === 'revoked' ? 'red' : 'default'
  return <Tag color={color}>{t(`connectors.status.${status}`, status)}</Tag>
}

function TaskStatusTag({ status }: { status: ConnectorTask['status'] }) {
  const color =
    status === 'succeeded'
      ? 'green'
      : status === 'failed' || status === 'expired'
        ? 'red'
        : status === 'leased' || status === 'executing'
          ? 'blue'
          : 'default'
  return <Tag color={color}>{connectorTaskStatusLabel(status)}</Tag>
}

function GatewayStatus({ connector }: { connector: Connector }) {
  const gateway = connector.gateway
  let color = 'default'
  let label = t('connectors.gateway.unknown')
  if (!gateway?.gateway_configured) {
    color = 'orange'
    label = t('connectors.gateway.missing')
  } else if (gateway.gateway_status === 'ok') {
    color = 'green'
    label = t('connectors.gateway.ok')
  } else if (gateway.gateway_status === 'error') {
    color = 'red'
    label = t('connectors.gateway.error')
  } else if (gateway.gateway_status === 'configured') {
    color = 'blue'
    label = t('connectors.gateway.configured')
  }

  const details = [
    gateway?.error_code
      ? t('connectors.gateway.errorCode', undefined, {
          code: connectorErrorLabel(gateway.error_code),
        })
      : '',
  ].filter(Boolean)

  const tag = <Tag color={color}>{label}</Tag>
  return details.length ? <Tooltip title={details.join('\n')}>{tag}</Tooltip> : tag
}

function TokenModal({
  result,
  onClose,
}: {
  result: RotateConnectorTokenResult | null
  onClose: () => void
}) {
  return (
    <Modal
      title={t('connectors.rotateSuccessTitle')}
      open={!!result}
      onCancel={onClose}
      footer={null}
      width={720}
    >
      <Alert
        type="warning"
        showIcon
        message={t('connectors.tokenWarning')}
        style={{ marginBottom: 16 }}
      />
      {result?.connector_token ? (
        <Typography.Paragraph copyable={{ text: result.connector_token, icon: <CopyOutlined /> }}>
          <Typography.Text code>{result.connector_token}</Typography.Text>
        </Typography.Paragraph>
      ) : null}
    </Modal>
  )
}

export default function Connectors() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const scopedUserId = !platform ? currentUserId(user) : undefined
  const [userId, setUserId] = useState<number | undefined>(scopedUserId)
  const [open, setOpen] = useState(false)
  const [install, setInstall] = useState<CreateConnectorEnrollmentResult | null>(null)
  const [rotated, setRotated] = useState<RotateConnectorTokenResult | null>(null)
  const [resolvingTask, setResolvingTask] = useState<ConnectorTask | null>(null)
  const writable = canWriteOwner(user)
  const users = useUsers(platform)
  const connectors = useConnectors(userId)
  const instances = useInstances(userId)
  const tasks = useConnectorTasks(platform ? { user_id: userId, limit: 100 } : { limit: 100 })
  const createEnrollment = useCreateConnectorEnrollment()
  const rotateToken = useRotateConnectorToken()
  const revokeConnector = useRevokeConnector()
  const resolveExecution = useResolveConnectorTaskExecution()
  const refreshInstallState = useCallback(async () => {
    await Promise.all([connectors.refetch(), instances.refetch()])
  }, [connectors, instances])

  const userName = useMemo(() => {
    const map = new Map<number, string>()
    for (const item of users.data ?? []) map.set(item.id, item.display_name || item.email)
    return (id?: number) => (id ? (map.get(id) ?? String(id)) : t('common.notAvailable'))
  }, [users.data])

  const instancesByID = useMemo(
    () => new Map((instances.data ?? []).map((instance) => [instance.id, instance])),
    [instances.data],
  )

  const instanceOptions = (instances.data ?? [])
    .filter((instance) => !instance.connector_id)
    .map((instance) => ({
      value: instance.id,
      label: `${instance.name} (${instance.kind})`,
    }))

  const connectorColumns: ProColumns<Connector>[] = [
    {
      title: t('connectors.columns.name'),
      dataIndex: 'name',
      width: 180,
      ellipsis: true,
      render: (value, record) => value || record.connector_id,
    },
    {
      title: t('connectors.columns.instance'),
      dataIndex: 'instance_id',
      width: 180,
      ellipsis: true,
      render: (_, record) => {
        const instance = instancesByID.get(record.instance_id)
        return instance?.name || t('connectors.noInstance', undefined, { id: record.instance_id })
      },
    },
    ...(platform
      ? [
          {
            title: t('connectors.columns.connectorId'),
            dataIndex: 'connector_id',
            width: 180,
            ellipsis: true,
            copyable: true,
          } as ProColumns<Connector>,
          {
            title: t('connectors.columns.user'),
            dataIndex: 'user_id',
            width: 140,
            ellipsis: true,
            render: (_: unknown, record: Connector) => userName(record.user_id),
          } as ProColumns<Connector>,
        ]
      : []),
    {
      title: t('connectors.columns.status'),
      dataIndex: 'status',
      width: 96,
      render: (_, record) => <ConnectorStatusTag status={record.status} />,
    },
    {
      title: t('connectors.columns.gateway'),
      dataIndex: 'gateway',
      width: 160,
      render: (_, record) => <GatewayStatus connector={record} />,
    },
    {
      title: t('connectors.columns.version'),
      dataIndex: 'version',
      width: 96,
      render: (value) => value || t('common.notAvailable'),
    },
    {
      title: t('connectors.columns.lastSeen'),
      dataIndex: 'last_seen_at',
      width: 120,
      render: (_, record) => <RelativeTime value={record.last_seen_at} />,
    },
    {
      title: t('connectors.columns.actions'),
      valueType: 'option',
      width: 220,
      render: (_, record) =>
        writable
          ? [
              <Popconfirm
                key="rotate"
                title={t('connectors.rotateConfirm')}
                description={t('connectors.rotateDescription')}
                okText={t('connectors.rotate')}
                cancelText={t('common.cancel')}
                okButtonProps={{ danger: true, loading: rotateToken.isPending }}
                onConfirm={async () =>
                  setRotated(await rotateToken.mutateAsync(record.connector_id))
                }
                disabled={record.status === 'revoked'}
              >
                <Button
                  size="small"
                  icon={<ReloadOutlined />}
                  disabled={record.status === 'revoked' || rotateToken.isPending}
                >
                  {t('connectors.rotate')}
                </Button>
              </Popconfirm>,
              <Popconfirm
                key="revoke"
                title={t('connectors.revokeConfirm')}
                onConfirm={() => revokeConnector.mutate(record.connector_id)}
                disabled={record.status === 'revoked'}
              >
                <Button
                  size="small"
                  danger
                  icon={<DisconnectOutlined />}
                  disabled={record.status === 'revoked'}
                >
                  {t('connectors.revoke')}
                </Button>
              </Popconfirm>,
            ]
          : [],
    },
  ]

  const taskColumns: ProColumns<ConnectorTask>[] = [
    {
      title: t('connectors.columns.taskId'),
      dataIndex: 'id',
      width: 180,
      ellipsis: true,
      copyable: true,
    },
    {
      title: t('connectors.columns.taskType'),
      dataIndex: 'type',
      width: 190,
      render: (_, record) => connectorTaskTypeLabel(record.type),
    },
    {
      title: t('connectors.columns.risk'),
      dataIndex: 'risk_level',
      width: 96,
      render: (_, record) => <RiskLevelTag level={record.risk_level} />,
    },
    {
      title: t('connectors.columns.status'),
      dataIndex: 'status',
      width: 110,
      render: (_, record) => <TaskStatusTag status={record.status} />,
    },
    {
      title: t('connectors.columns.instance'),
      dataIndex: 'instance_id',
      width: 160,
      ellipsis: true,
      render: (_, record) => instancesByID.get(record.instance_id)?.name || record.instance_id,
    },
    {
      title: t('connectors.columns.connectorId'),
      dataIndex: 'connector_id',
      width: 160,
      ellipsis: true,
      copyable: true,
    },
    {
      title: t('connectors.columns.error'),
      dataIndex: 'error',
      width: 260,
      ellipsis: true,
      render: (_, record) => {
        if (!record.error?.code) return t('connectors.noTaskError')
        const retryable = record.error.retryable ? ` (${t('connectors.retryable')})` : ''
        return `${connectorErrorLabel(record.error.code)}${retryable}`
      },
    },
    {
      title: t('connectors.columns.createdAt'),
      dataIndex: 'created_at',
      width: 180,
      render: (_, record) => <AbsoluteTime value={record.created_at} />,
    },
    ...(platform
      ? [
          {
            title: t('connectors.columns.actions'),
            valueType: 'option',
            width: 150,
            fixed: 'right',
            render: (_: unknown, record: ConnectorTask) =>
              record.status === 'executing'
                ? [
                    <Button
                      key="resolve-execution"
                      size="small"
                      danger
                      onClick={() => setResolvingTask(record)}
                    >
                      {t('connectors.resolution.action')}
                    </Button>,
                  ]
                : [],
          } as ProColumns<ConnectorTask>,
        ]
      : []),
  ]

  return (
    <PageContainer title={t('connectors.title')} subTitle={t('connectors.subtitle')}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <ProTable<Connector>
          headerTitle={t('connectors.connectorTable')}
          rowKey="connector_id"
          search={false}
          loading={connectors.isLoading}
          dataSource={connectors.data ?? []}
          columns={connectorColumns}
          scroll={{ x: 'max-content' }}
          options={{ reload: () => connectors.refetch() }}
          toolBarRender={() => [
            <UserSelect
              key="user"
              value={userId}
              onChange={setUserId}
              placeholder={t('common.allAccounts')}
            />,
            writable ? (
              <Button
                key="new"
                type="primary"
                icon={<PlusOutlined />}
                onClick={() => setOpen(true)}
                disabled={instanceOptions.length === 0}
              >
                {t('connectors.createInstall')}
              </Button>
            ) : null,
          ]}
        />

        <ProTable<ConnectorTask>
          headerTitle={t('connectors.taskTable')}
          rowKey="id"
          search={false}
          loading={tasks.isLoading}
          dataSource={tasks.data ?? []}
          columns={taskColumns}
          pagination={{ pageSize: 10 }}
          scroll={{ x: 'max-content' }}
          options={{ reload: () => tasks.refetch() }}
        />
      </Space>

      <DrawerForm<CreateConnectorEnrollmentInput>
        title={t('connectors.form.createTitle')}
        open={open}
        onOpenChange={setOpen}
        drawerProps={{ destroyOnHidden: true }}
        initialValues={{ local_config_port: 18081 }}
        submitter={{
          searchConfig: {
            submitText: t('connectors.form.generate'),
            resetText: t('common.reset'),
          },
          submitButtonProps: { loading: createEnrollment.isPending },
        }}
        onFinish={async (values) => {
          const result = await createEnrollment.mutateAsync({
            instance_id: values.instance_id,
            user_id: scopedUserId,
            local_config_port: values.local_config_port,
          })
          setInstall(result)
          return true
        }}
      >
        <Alert
          type="info"
          showIcon
          message={t('connectors.form.notice')}
          style={{ marginBottom: 16 }}
        />
        <ProFormSelect
          name="instance_id"
          label={t('connectors.form.instance')}
          rules={[{ required: true, message: t('connectors.form.instanceRequired') }]}
          options={instanceOptions}
          showSearch
          fieldProps={{ optionFilterProp: 'label' }}
        />
        <ProFormDigit
          name="local_config_port"
          label={t('connectors.form.localConfigPort')}
          min={1024}
          max={65535}
          fieldProps={{ precision: 0 }}
          extra={t('connectors.form.localConfigPortExtra')}
        />
      </DrawerForm>

      <ConnectorInstallModal
        result={install}
        connectors={connectors.data}
        instances={instances.data}
        onRefresh={refreshInstallState}
        onClose={() => setInstall(null)}
      />
      <TokenModal result={rotated} onClose={() => setRotated(null)} />
      <ConnectorExecutionResolutionModal
        task={resolvingTask}
        connectorStatus={
          connectors.data?.find((item) => item.connector_id === resolvingTask?.connector_id)?.status
        }
        submitting={resolveExecution.isPending}
        onClose={() => setResolvingTask(null)}
        onSubmit={async (taskId, body) => {
          await resolveExecution.mutateAsync({ taskId, body })
          setResolvingTask(null)
        }}
      />
    </PageContainer>
  )
}
