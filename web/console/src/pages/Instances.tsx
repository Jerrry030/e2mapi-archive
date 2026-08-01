import { useCallback, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import {
  DrawerForm,
  ModalForm,
  PageContainer,
  ProFormSelect,
  ProFormText,
} from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Popconfirm } from 'antd'
import {
  ApiOutlined,
  DisconnectOutlined,
  EditOutlined,
  LinkOutlined,
  PlusOutlined,
  SettingOutlined,
  SwapOutlined,
} from '@ant-design/icons'
import {
  useActiveRoleUser,
  useBindInstanceConnector,
  useCreateInstanceConnectorInstall,
  useCreateInstance,
  useConnectors,
  useInstances,
  useUpdateInstance,
  useUsers,
} from '../api/hooks'
import { EmptyTeach, RelativeTime } from '../components/common'
import { KindTag, StatusTag } from '../components/tags'
import { UserSelect } from '../components/fields'
import { ConnectorInstallModal } from '../components/ConnectorInstallGuide'
import { InstanceMonitorPolicyDrawer } from '../components/InstanceMonitorPolicyDrawer'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { canWriteOwner, currentUserId, isPlatformAdmin } from '../api/auth'
import type { CreateConnectorEnrollmentResult, Instance, InstanceKind } from '../api/types'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { eligibleInstanceOwners } from './resourceFormAccess'
import { instancesForLocation } from './instanceLocation'

interface InstanceFormValues {
  user_id?: number
  name: string
  kind: InstanceKind
}

function trim(v?: string) {
  return (v ?? '').trim()
}

export default function Instances() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const [searchParams, setSearchParams] = useSearchParams()
  const platform = isPlatformAdmin(user)
  const scopedUserId = !platform ? currentUserId(user) : undefined
  const [userId, setUserId] = useState<number | undefined>(scopedUserId)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Instance | null>(null)
  const [binding, setBinding] = useState<Instance | null>(null)
  const [monitoring, setMonitoring] = useState<Instance | null>(null)
  const [install, setInstall] = useState<CreateConnectorEnrollmentResult | null>(null)
  const { data, isLoading, refetch } = useInstances(userId)
  const connectors = useConnectors(userId)
  const users = useUsers(platform)
  const createInstance = useCreateInstance()
  const updateInstance = useUpdateInstance()
  const createInstall = useCreateInstanceConnectorInstall()
  const bindConnector = useBindInstanceConnector()
  const writable = canWriteOwner(user)
  const located = instancesForLocation(data ?? [], searchParams.get('instance_id'))
  const clearLocation = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('instance_id')
    setSearchParams(next, { replace: true })
  }
  const submitting = createInstance.isPending || updateInstance.isPending
  const refreshInstallState = useCallback(async () => {
    await Promise.all([refetch(), connectors.refetch()])
  }, [connectors, refetch])

  const columns: ProColumns<Instance>[] = [
    { title: t('instances.columns.name'), dataIndex: 'name', width: 180, ellipsis: true },
    {
      title: t('instances.columns.kind'),
      dataIndex: 'kind',
      width: 120,
      render: (_, r) => <KindTag kind={r.kind} />,
    },
    {
      title: t('instances.columns.status'),
      dataIndex: 'status',
      width: 96,
      render: (_, r) => <StatusTag status={r.status} />,
    },
    ...(platform
      ? [
          {
            title: t('instances.columns.connector'),
            dataIndex: 'connector_id',
            width: 180,
            ellipsis: true,
            copyable: true,
            render: (_, record) => record.connector_id || t('common.none'),
          } as ProColumns<Instance>,
        ]
      : []),
    {
      title: t('instances.columns.createdAt'),
      dataIndex: 'created_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
    {
      title: t('instances.columns.actions'),
      valueType: 'option',
      key: 'option',
      width: 520,
      render: (_, r) => [
        <Link key="accounts" to={`/instances/${r.id}/accounts`}>
          <Button type="link" size="small" icon={<SwapOutlined />}>
            {t('instances.actions.accounts')}
          </Button>
        </Link>,
        <Button
          key="monitor"
          type="link"
          size="small"
          icon={<SettingOutlined />}
          onClick={() => setMonitoring(r)}
        >
          {t('instances.actions.monitorPolicy')}
        </Button>,
        writable ? (
          <Button
            key="edit"
            type="link"
            size="small"
            icon={<EditOutlined />}
            onClick={() => {
              setEditing(r)
              setOpen(true)
            }}
          >
            {t('common.edit')}
          </Button>
        ) : null,
        writable ? (
          <Button
            key="install"
            type="link"
            size="small"
            icon={<ApiOutlined />}
            loading={createInstall.isPending}
            onClick={async () => setInstall(await createInstall.mutateAsync(r.id))}
          >
            {r.connector_id
              ? t('instances.actions.reinstallConnector')
              : t('instances.actions.installConnector')}
          </Button>
        ) : null,
        platform ? (
          <Button
            key="bind"
            type="link"
            size="small"
            icon={<LinkOutlined />}
            onClick={() => setBinding(r)}
          >
            {r.connector_id
              ? t('instances.actions.changeConnector')
              : t('instances.actions.bindConnector')}
          </Button>
        ) : null,
        platform && r.connector_id ? (
          <Popconfirm
            key="unbind"
            title={t('instances.actions.unbindConnectorConfirm')}
            okText={t('instances.actions.unbindConnector')}
            cancelText={t('common.cancel')}
            okButtonProps={{ danger: true, loading: bindConnector.isPending }}
            onConfirm={() => bindConnector.mutate({ instanceId: r.id, connectorId: '' })}
          >
            <Button type="link" size="small" danger icon={<DisconnectOutlined />}>
              {t('instances.actions.unbindConnector')}
            </Button>
          </Popconfirm>
        ) : null,
      ],
    },
  ]

  return (
    <PageContainer title={t('instances.title')}>
      {located.requested ? (
        <Alert
          type={located.found ? 'info' : 'warning'}
          showIcon
          style={{ marginBottom: 16 }}
          message={
            located.found
              ? `正在定位实例：${located.items[0].name}`
              : '未找到要定位的实例，已显示当前可见的全部实例'
          }
          action={<Button onClick={clearLocation}>清除定位</Button>}
        />
      ) : null}
      <ProTable<Instance>
        rowKey="id"
        loading={isLoading}
        dataSource={located.items}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
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
              onClick={() => {
                setEditing(null)
                setOpen(true)
              }}
            >
              {t('instances.actions.create')}
            </Button>
          ) : null,
        ]}
        locale={{
          emptyText: <EmptyTeach title={t('instances.empty')} />,
        }}
      />

      <DrawerForm<InstanceFormValues>
        title={editing ? t('instances.form.editTitle') : t('instances.form.createTitle')}
        open={open}
        onOpenChange={(next) => {
          setOpen(next)
          if (!next) setEditing(null)
        }}
        drawerProps={{ destroyOnHidden: true }}
        initialValues={
          editing
            ? {
                user_id: editing.user_id,
                name: editing.name,
                kind: editing.kind,
              }
            : undefined
        }
        submitter={{
          submitButtonProps: { loading: submitting },
        }}
        onFinish={async (values) => {
          const body = {
            user_id: platform ? values.user_id : undefined,
            name: trim(values.name),
            kind: values.kind,
          }
          if (editing) {
            await updateInstance.mutateAsync({ id: editing.id, body })
          } else {
            const created = await createInstance.mutateAsync(body)
            if (created.connector_install) setInstall(created.connector_install)
          }
          return true
        }}
      >
        {platform && !editing ? (
          <ProFormSelect
            name="user_id"
            label={t('instances.form.account')}
            rules={[{ required: true, message: t('instances.form.accountRequired') }]}
            options={eligibleInstanceOwners(users.data ?? []).map((item) => ({
              value: item.id,
              label: item.display_name || item.email,
            }))}
          />
        ) : null}
        <ProFormText
          name="name"
          label={t('instances.form.name')}
          rules={[{ required: true, message: t('instances.form.nameRequired') }]}
          placeholder={t('instances.form.namePlaceholder')}
        />
        <ProFormSelect
          name="kind"
          label={t('instances.form.kind')}
          rules={[{ required: true, message: t('instances.form.kindRequired') }]}
          options={[
            { value: 'sub2api', label: 'sub2api' },
            { value: 'newapi', label: 'new-api' },
            { value: 'cpa', label: 'CPA / CLIProxyAPI' },
          ]}
        />
      </DrawerForm>
      <ModalForm<{ connector_id: string }>
        key={binding?.id ?? 'bind-connector'}
        title={t('instances.connectorForm.title')}
        open={!!binding}
        onOpenChange={(next) => {
          if (!next) setBinding(null)
        }}
        modalProps={{ destroyOnHidden: true }}
        initialValues={{ connector_id: binding?.connector_id }}
        submitter={{ submitButtonProps: { loading: bindConnector.isPending } }}
        onFinish={async (values) => {
          if (!binding) return false
          await bindConnector.mutateAsync({
            instanceId: binding.id,
            connectorId: values.connector_id,
          })
          return true
        }}
      >
        <ProFormSelect
          name="connector_id"
          label={t('instances.connectorForm.connector')}
          rules={[{ required: true, message: t('instances.connectorForm.connectorRequired') }]}
          options={(connectors.data ?? [])
            .filter(
              (connector) =>
                connector.instance_id === binding?.id && connector.status !== 'revoked',
            )
            .map((connector) => ({
              value: connector.connector_id,
              label: `${connector.name || connector.connector_id} (${t(
                `connectors.status.${connector.status}`,
                connector.status,
              )})`,
            }))}
          fieldProps={{ showSearch: true, optionFilterProp: 'label' }}
        />
      </ModalForm>
      <ConnectorInstallModal
        result={install}
        connectors={connectors.data}
        instances={data}
        onRefresh={refreshInstallState}
        onClose={() => setInstall(null)}
      />
      <InstanceMonitorPolicyDrawer instance={monitoring} onClose={() => setMonitoring(null)} />
    </PageContainer>
  )
}
