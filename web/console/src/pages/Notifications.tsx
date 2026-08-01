import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import {
  ModalForm,
  PageContainer,
  ProCard,
  ProFormDependency,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProFormInstance, ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Popconfirm, Space, Switch, Tag, Typography } from 'antd'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import {
  useCreateNotificationRoute,
  useDeleteNotificationRoute,
  useActiveRoleUser,
  useNotificationChannelStatuses,
  useNotificationDeliveries,
  useNotificationRoutes,
  useNotificationTargets,
  useRetryNotificationDelivery,
  useSecrets,
  useTestNotificationRoute,
  useUsers,
  useUpdateNotificationRoute,
} from '../api/hooks'
import type { NotificationRouteInput } from '../api/endpoints'
import { ChannelTag } from '../components/tags'
import { UserSelect } from '../components/fields'
import { EmptyTeach, RelativeTime } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { currentUserId, getActiveRole } from '../api/auth'
import type {
  NotificationChannelStatus,
  NotificationDelivery,
  NotificationRoute,
  NotificationSystemChannel,
} from '../api/types'
import { friendlyErrorMessage } from '../api/errors'
import {
  notificationCredentialOptions,
  safeNotificationCredentialRef,
} from './notificationCredentials'
import {
  findSystemChannelStatus,
  latestDeliveriesByRoute,
  notificationChannelStatusView,
  notificationDeliveryStatusView,
  notificationFormUserOptions,
} from './notificationDeliveryView'
import PersonalNotificationTargets from './PersonalNotificationTargets'
import {
  canTestNotificationRoute,
  channelForDeliveryMethod,
  deliveryMethodForRoute,
  notificationDestination,
  personalTargetForChannel,
  targetRefForDeliveryMethod,
} from './notificationTargets'
import type { NotificationDeliveryMethod } from './notificationTargets'

const { Text } = Typography

const channelOptions = [
  { value: 'platform_feishu', label: '平台统一飞书群' },
  { value: 'personal_feishu', label: '我的飞书机器人' },
  { value: 'platform_qq', label: '平台统一 QQ 群' },
  { value: 'personal_qq', label: '我的 QQ 群' },
  { value: 'webhook', label: 'Webhook（高级）' },
]

const eventLevelOptions = [
  { value: 'L0', label: '全部消息（含日常记录）' },
  { value: 'L1', label: '重要动态和异常' },
  { value: 'L2', label: '仅异常和紧急问题（推荐）' },
  { value: 'L3', label: '仅紧急问题' },
]

const eventLevelLabels = Object.fromEntries(
  eventLevelOptions.map((option) => [option.value, option.label.replace('（推荐）', '')]),
) as Record<string, string>

function toInput(r: NotificationRoute): NotificationRouteInput {
  return {
    user_id: r.user_id,
    name: r.name,
    channel: r.channel,
    target_ref: r.target_ref,
    min_risk_level: r.min_risk_level,
    min_event_level: r.min_event_level ?? r.min_risk_level,
    enabled: r.enabled,
    template: r.template,
    quiet_window: r.quiet_window,
    escalation_after: r.escalation_after,
  }
}

function toFormInput(r: NotificationRoute) {
  const input = toInput(r)
  return {
    ...input,
    delivery_method: deliveryMethodForRoute(r),
    target_ref:
      r.channel === 'webhook' ? safeNotificationCredentialRef(r.target_ref, r.user_id) : undefined,
  }
}

function NotificationCredentialSelect({
  userId,
  currentRef,
}: {
  userId: number
  currentRef?: string
}) {
  const secrets = useSecrets(userId)
  const availableOptions = notificationCredentialOptions(secrets.data, userId)
  const availableRefs = new Set(availableOptions.map((option) => option.value))
  const options =
    currentRef && !availableRefs.has(currentRef)
      ? [
          {
            value: currentRef,
            label: '原 Webhook 地址已不可用',
            disabled: true,
          },
          ...availableOptions,
        ]
      : availableOptions

  return (
    <>
      <ProFormSelect
        name="target_ref"
        label="Webhook 地址"
        rules={[
          { required: true, message: '请选择 Webhook 地址' },
          {
            validator: (_, value) =>
              !value || availableRefs.has(value)
                ? Promise.resolve()
                : Promise.reject(new Error('请选择当前账号可用的 Webhook 地址')),
          },
        ]}
        options={options}
        placeholder="选择已安全保存的 Webhook 地址"
        fieldProps={{
          loading: secrets.isFetching,
          showSearch: true,
          optionFilterProp: 'label',
        }}
      />
      {!secrets.isFetching && availableOptions.length === 0 ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="该账号还没有可用的 Webhook 地址"
          description="请先在凭证管理中安全保存 Webhook 地址，再回来选择。"
          action={<Link to="/secrets">前往凭证管理</Link>}
        />
      ) : null}
    </>
  )
}

function EmptyNotificationCredentialSelect() {
  return (
    <ProFormSelect
      name="target_ref"
      label="Webhook 地址"
      rules={[{ required: true, message: '请先选择账号' }]}
      disabled
      placeholder="请先选择账号"
      extra={<Link to="/secrets">前往凭证管理</Link>}
    />
  )
}

function SystemChannelCard({
  channel,
  status,
}: {
  channel: NotificationSystemChannel
  status?: NotificationChannelStatus
}) {
  const view = notificationChannelStatusView(status)
  const name = channel === 'feishu' ? '平台飞书群' : '平台 QQ 群'
  const showingFailure = status?.state === 'failing' || !status?.last_success_at
  const activityAt = showingFailure
    ? status?.last_failure_at
    : status?.last_success_at || status?.last_failure_at

  return (
    <ProCard bordered title={name} extra={<Tag color={view.color}>{view.label}</Tag>}>
      {!status ? (
        <Text type="secondary">尚未取得渠道状态。</Text>
      ) : !status.configured ? (
        <Text type="secondary">平台尚未配置，暂时无法发送消息。</Text>
      ) : activityAt ? (
        <Space direction="vertical" size={4}>
          <Text type="secondary">
            {showingFailure ? '最近失败：' : '最近成功：'}
            <RelativeTime value={activityAt} />
          </Text>
          {showingFailure && status.last_error_code ? (
            <Text type="danger">错误代码：{status.last_error_code}</Text>
          ) : null}
        </Space>
      ) : (
        <Text type="secondary">渠道已配置，发送测试消息后可确认是否可用。</Text>
      )}
    </ProCard>
  )
}

export default function Notifications() {
  const user = useActiveRoleUser()
  const platform = getActiveRole(user) === 'admin'
  const scopedUserId = !platform ? currentUserId(user) : undefined
  const [userId, setUserId] = useState<number | undefined>(scopedUserId)
  const effectiveUserId = platform ? userId : scopedUserId
  const [editing, setEditing] = useState<NotificationRoute | null>(null)
  const [formUserId, setFormUserId] = useState<number | undefined>(scopedUserId)
  const [formOpen, setFormOpen] = useState(false)
  const formRef = useRef<ProFormInstance | undefined>(undefined)
  const previousPlatform = useRef(platform)

  useEffect(() => {
    if (previousPlatform.current && !platform) {
      setUserId(scopedUserId)
      setFormUserId(scopedUserId)
      setEditing(null)
      setFormOpen(false)
    }
    previousPlatform.current = platform
  }, [platform, scopedUserId])

  const { data, isLoading, isError, error, refetch } = useNotificationRoutes(effectiveUserId)
  const channelStatuses = useNotificationChannelStatuses()
  const deliveries = useNotificationDeliveries({ user_id: effectiveUserId, limit: 50 })
  const users = useUsers(platform)
  const createRoute = useCreateNotificationRoute()
  const updateRoute = useUpdateNotificationRoute()
  const deleteRoute = useDeleteNotificationRoute()
  const testRoute = useTestNotificationRoute()
  const retryDelivery = useRetryNotificationDelivery()
  const latestByRoute = latestDeliveriesByRoute(deliveries.data)

  const openCreate = () => {
    setEditing(null)
    setFormUserId(platform ? userId : scopedUserId)
    setFormOpen(true)
  }
  const openEdit = (r: NotificationRoute) => {
    setEditing(r)
    setFormUserId(r.user_id)
    setFormOpen(true)
  }

  const handleFormOpenChange = (nextOpen: boolean) => {
    setFormOpen(nextOpen)
    if (!nextOpen) {
      setEditing(null)
      setFormUserId(platform ? userId : scopedUserId)
    }
  }

  const credentialUserId = platform ? (editing?.user_id ?? formUserId) : scopedUserId
  const personalTargets = useNotificationTargets(credentialUserId, Boolean(credentialUserId))
  const currentWebhookRef =
    editing?.channel === 'webhook'
      ? safeNotificationCredentialRef(editing.target_ref, editing.user_id)
      : undefined
  const formUserOptions = notificationFormUserOptions(users.data, platform ? user : null)

  const columns: ProColumns<NotificationRoute>[] = [
    { title: '通知名称', dataIndex: 'name', width: 160, ellipsis: true },
    {
      title: '接收方式',
      dataIndex: 'channel',
      width: 100,
      render: (_, r) => <ChannelTag channel={r.channel} />,
    },
    {
      title: '发送到',
      dataIndex: 'target_ref',
      width: 240,
      ellipsis: true,
      render: (_, r) => {
        return notificationDestination(r)
      },
    },
    {
      title: '接收哪些消息',
      dataIndex: 'min_event_level',
      width: 190,
      render: (_, r) => {
        const level = r.min_event_level ?? r.min_risk_level
        return eventLevelLabels[level] ?? level
      },
    },
    {
      title: '接收通知',
      dataIndex: 'enabled',
      width: 80,
      render: (_, r) => (
        <Switch
          size="small"
          checked={r.enabled}
          loading={updateRoute.isPending}
          aria-label={`通知「${r.name}」接收开关`}
          onChange={(v) => updateRoute.mutate({ id: r.id, body: { ...toInput(r), enabled: v } })}
        />
      ),
    },
    {
      title: '最近发送',
      key: 'last_delivery',
      width: 120,
      render: (_, r) => {
        const delivery = latestByRoute.get(r.id)
        if (!delivery) return <Text type="secondary">尚未发送</Text>
        const view = notificationDeliveryStatusView(delivery.status)
        return <Tag color={view.color}>{view.label}</Tag>
      },
    },
    {
      title: '操作',
      valueType: 'option',
      width: 190,
      render: (_, r) => (
        <Space>
          <Button
            type="link"
            size="small"
            style={{ padding: 0 }}
            disabled={!canTestNotificationRoute(r, channelStatuses.data, personalTargets.data)}
            loading={testRoute.isPending && testRoute.variables === r.id}
            onClick={() => testRoute.mutate(r.id)}
          >
            发送测试
          </Button>
          <Button type="link" size="small" style={{ padding: 0 }} onClick={() => openEdit(r)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除通知「${r.name}」？`}
            description="删除后将不再通过这种方式接收通知。"
            onConfirm={() => deleteRoute.mutate(r.id)}
          >
            <Button type="link" size="small" danger style={{ padding: 0 }}>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const deliveryColumns: ProColumns<NotificationDelivery>[] = [
    {
      title: '加入时间',
      dataIndex: 'created_at',
      width: 130,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
    { title: '通知名称', dataIndex: 'route_name', width: 160, ellipsis: true },
    {
      title: '消息类型',
      dataIndex: 'kind',
      width: 100,
      render: (_, r) => (r.kind === 'test' ? '测试消息' : '业务消息'),
    },
    {
      title: '接收方式',
      dataIndex: 'channel',
      width: 100,
      render: (_, r) => <ChannelTag channel={r.channel} />,
    },
    {
      title: '发送状态',
      dataIndex: 'status',
      width: 110,
      render: (_, r) => {
        const view = notificationDeliveryStatusView(r.status)
        return <Tag color={view.color}>{view.label}</Tag>
      },
    },
    {
      title: '尝试次数',
      dataIndex: 'attempts',
      width: 100,
      render: (_, r) => `${r.attempts}/${r.max_attempts}`,
    },
    {
      title: '结果时间',
      key: 'result_at',
      width: 130,
      render: (_, r) => <RelativeTime value={r.sent_at ?? r.updated_at} />,
    },
    {
      title: '失败原因',
      dataIndex: 'last_error_message',
      width: 220,
      ellipsis: true,
      render: (_, r) => r.last_error_message || '—',
    },
    {
      title: '操作',
      valueType: 'option',
      width: 100,
      render: (_, r) =>
        r.status === 'failed' ? (
          <Popconfirm
            title="重新发送这条消息？"
            description="确认后，消息将重新加入发送队列。"
            onConfirm={() => retryDelivery.mutate(r.id)}
          >
            <Button
              type="link"
              size="small"
              style={{ padding: 0 }}
              loading={retryDelivery.isPending && retryDelivery.variables === r.id}
            >
              重新发送
            </Button>
          </Popconfirm>
        ) : null,
    },
  ]

  return (
    <PageContainer title="通知设置" subTitle="选择接收方式和需要接收的消息">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message="通知设置保存后，系统会按你选择的重要程度把消息加入发送队列。最终是否发送成功，请以“最近发送记录”为准。"
        description="你可以继续使用平台统一渠道，也可以在下方安全配置自己的飞书机器人或 QQ 群。"
      />
      <PersonalNotificationTargets userId={effectiveUserId} />
      {channelStatuses.isError ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="接收渠道状态暂不可用"
          description={friendlyErrorMessage(channelStatuses.error)}
          action={<Button onClick={() => channelStatuses.refetch()}>重试</Button>}
        />
      ) : (
        <ProCard
          title="平台统一渠道"
          subTitle="由平台维护，可作为个人渠道之外的接收方式"
          gutter={16}
          wrap
          style={{ marginBottom: 16 }}
          loading={channelStatuses.isLoading}
        >
          <SystemChannelCard
            channel="feishu"
            status={findSystemChannelStatus(channelStatuses.data, 'feishu')}
          />
          <SystemChannelCard
            channel="qq"
            status={findSystemChannelStatus(channelStatuses.data, 'qq')}
          />
        </ProCard>
      )}
      {isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message="通知设置加载失败"
          description={friendlyErrorMessage(error)}
          action={<Button onClick={() => refetch()}>重试</Button>}
        />
      ) : null}
      <ProTable<NotificationRoute>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          ...(platform
            ? [<UserSelect key="u" value={userId} onChange={setUserId} placeholder="全部账号" />]
            : []),
          <Button key="new" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            添加通知
          </Button>,
        ]}
        locale={{
          emptyText: (
            <EmptyTeach
              title="还没有添加通知。添加后，系统可在服务异常、自动切换或接入失败时提醒你。"
              action={
                <Button type="primary" onClick={openCreate}>
                  添加通知
                </Button>
              }
            />
          ),
        }}
      />

      {deliveries.isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message="最近发送记录加载失败"
          description={friendlyErrorMessage(deliveries.error)}
          action={<Button onClick={() => deliveries.refetch()}>重试</Button>}
        />
      ) : null}
      <ProTable<NotificationDelivery>
        headerTitle="最近发送记录"
        rowKey="id"
        loading={deliveries.isLoading || deliveries.isFetching}
        dataSource={deliveries.isError ? [] : (deliveries.data ?? [])}
        columns={deliveryColumns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={false}
        toolBarRender={() => [
          <Button
            key="refresh"
            icon={<ReloadOutlined />}
            loading={deliveries.isFetching}
            onClick={() => deliveries.refetch()}
          >
            刷新
          </Button>,
        ]}
        locale={{
          emptyText: (
            <EmptyTeach title="还没有发送记录。可以先在上方选择一条通知，发送测试消息。" />
          ),
        }}
      />

      <ModalForm
        key={editing?.id ?? 'new'}
        title={editing ? `编辑通知 · ${editing.name}` : '添加通知'}
        open={formOpen}
        onOpenChange={handleFormOpenChange}
        formRef={formRef}
        modalProps={{ destroyOnHidden: true }}
        initialValues={
          editing
            ? toFormInput(editing)
            : {
                user_id: platform ? userId : scopedUserId,
                delivery_method: 'platform_feishu',
                min_risk_level: 'L0',
                min_event_level: 'L2',
                enabled: true,
              }
        }
        onValuesChange={(changedValues) => {
          if (Object.prototype.hasOwnProperty.call(changedValues, 'delivery_method')) {
            formRef.current?.setFieldsValue({ target_ref: undefined })
          }
          if (
            platform &&
            !editing &&
            Object.prototype.hasOwnProperty.call(changedValues, 'user_id')
          ) {
            formRef.current?.setFieldsValue({ target_ref: undefined })
            setFormUserId(changedValues.user_id)
          }
        }}
        onFinish={async (values) => {
          const formValues = values as NotificationRouteInput & {
            delivery_method: NotificationDeliveryMethod
          }
          const channel =
            formValues.delivery_method === 'webhook'
              ? 'webhook'
              : channelForDeliveryMethod(formValues.delivery_method)
          const personalTarget =
            channel === 'feishu' || channel === 'qq'
              ? personalTargetForChannel(personalTargets.data, channel)
              : undefined
          const preservedPersonalRef =
            editing && deliveryMethodForRoute(editing) === formValues.delivery_method
              ? editing.target_ref
              : undefined
          const body: NotificationRouteInput = {
            user_id: platform
              ? (formValues.user_id ?? editing?.user_id ?? effectiveUserId ?? 0)
              : (scopedUserId ?? 0),
            name: formValues.name,
            min_risk_level: formValues.min_risk_level,
            min_event_level: formValues.min_event_level,
            enabled: formValues.enabled,
            template: formValues.template,
            ...(editing
              ? {
                  quiet_window: editing.quiet_window,
                  escalation_after: editing.escalation_after,
                }
              : {}),
            channel,
            target_ref: targetRefForDeliveryMethod(
              formValues.delivery_method,
              personalTarget,
              formValues.delivery_method === 'webhook'
                ? formValues.target_ref
                : preservedPersonalRef,
            ),
          }
          if (editing) {
            await updateRoute.mutateAsync({ id: editing.id, body })
          } else {
            await createRoute.mutateAsync(body)
          }
          return true
        }}
      >
        {platform ? (
          <ProFormSelect
            name="user_id"
            label="账号"
            rules={[{ required: true }]}
            disabled={!!editing}
            options={formUserOptions}
          />
        ) : null}
        <ProFormText
          name="name"
          label="通知名称"
          placeholder="例如：生产异常提醒"
          rules={[{ required: true }]}
        />
        <ProFormSelect
          name="delivery_method"
          label="接收方式"
          rules={[{ required: true }]}
          options={channelOptions.map((option) => ({
            ...option,
            disabled:
              option.value === 'personal_feishu'
                ? !personalTargetForChannel(personalTargets.data, 'feishu')?.configured
                : option.value === 'personal_qq'
                  ? !personalTargetForChannel(personalTargets.data, 'qq')?.configured
                  : false,
          }))}
          extra="“我的”渠道需先在页面上方完成安全配置。"
        />
        <ProFormDependency name={['delivery_method']}>
          {({ delivery_method: method }: { delivery_method?: NotificationDeliveryMethod }) =>
            method === 'webhook' ? (
              credentialUserId ? (
                <NotificationCredentialSelect
                  key={credentialUserId}
                  userId={credentialUserId}
                  currentRef={currentWebhookRef}
                />
              ) : (
                <EmptyNotificationCredentialSelect />
              )
            ) : method === 'personal_feishu' || method === 'personal_qq' ? (
              (() => {
                const channel = channelForDeliveryMethod(method)
                const target = personalTargetForChannel(personalTargets.data, channel)
                const name = channel === 'feishu' ? '我的飞书机器人' : '我的 QQ 群'
                return (
                  <Alert
                    type={target?.configured ? 'success' : 'warning'}
                    showIcon
                    style={{ marginBottom: 16 }}
                    message={
                      target?.configured
                        ? `消息将发送到${name}`
                        : `${name}尚未配置，请先在页面上方完成配置`
                    }
                    description="机器人地址、Token、密钥和完整群号不会写入这条通知设置。"
                  />
                )
              })()
            ) : method === 'platform_feishu' || method === 'platform_qq' ? (
              (() => {
                const channel = channelForDeliveryMethod(method)
                const status = findSystemChannelStatus(channelStatuses.data, channel)
                const name = channel === 'feishu' ? '平台飞书群' : '平台 QQ 群'
                return (
                  <Alert
                    type={status?.configured ? 'info' : 'warning'}
                    showIcon
                    style={{ marginBottom: 16 }}
                    message={
                      status?.configured
                        ? `消息将发送到平台统一配置的${name}`
                        : `平台尚未配置${name}，保存后暂时无法发送消息`
                    }
                  />
                )
              })()
            ) : null
          }
        </ProFormDependency>
        <ProFormSelect
          name="min_event_level"
          label="接收哪些消息"
          rules={[{ required: true }]}
          options={eventLevelOptions}
          extra="默认只接收需要处理的异常；如希望看到接入成功等日常进展，可选择“全部消息”。"
        />
        <ProFormSwitch name="enabled" label="接收通知" />
        <ProFormTextArea
          name="template"
          label="自定义消息格式（高级，可选）"
          placeholder={'[{eventLevel}] {title}\n{text}'}
          extra="占位符：{title} {text} {eventLevel} {riskLevel} {result} {userId} {instanceId} 及事件字段（{instanceName} {accountName} {balance}…）；留空使用默认格式"
          fieldProps={{ rows: 3 }}
        />
      </ModalForm>
    </PageContainer>
  )
}
