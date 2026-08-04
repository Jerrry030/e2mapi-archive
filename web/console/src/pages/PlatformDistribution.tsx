import { useMemo, useState } from 'react'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
} from 'antd'
import {
  CopyOutlined,
  EditOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { getActiveRole } from '../api/auth'
import { useActiveRoleUser, useUsers } from '../api/hooks'
import {
  useAdjustPlatformWallet,
  useCreatePlatformGroup,
  useCreatePlatformKey,
  useCreatePlatformUpstream,
  useDeletePlatformGroup,
  useDeletePlatformUpstream,
  usePlatformGroups,
  usePlatformKeys,
  usePlatformKeyValue,
  usePlatformUsage,
  usePlatformUpstreams,
  usePlatformWallet,
  useTestPlatformUpstream,
  useUpdatePlatformGroup,
  useUpdatePlatformUpstream,
} from '../api/platformDistributionHooks'
import type { PlatformKeyInput } from '../api/endpoints'
import type { PlatformApiKey, PlatformGroup, PlatformUsage, PlatformUpstream } from '../api/types'

const yuan = (micros: number) => `¥${(micros / 1_000_000).toFixed(2)}`
const parseLabels = (value: unknown) => {
  const labels: Record<string, string> = {}
  for (const entry of String(value ?? '').split(',')) {
    const [rawKey, ...rawValue] = entry.split('=')
    const key = rawKey?.trim()
    const labelValue = rawValue.join('=').trim()
    if (key && labelValue) labels[key] = labelValue
  }
  return labels
}

export default function PlatformDistribution() {
  const { message } = App.useApp()
  const user = useActiveRoleUser()
  const role = getActiveRole(user)
  const admin = role === 'admin'
  const users = useUsers(admin)
  const clients = (users.data ?? []).filter((item) => item.roles.includes('client') && item.enabled)
  const [selectedUserId, setSelectedUserId] = useState<number>()
  const targetUserId = admin ? (selectedUserId ?? clients[0]?.id) : user?.id
  const groups = usePlatformGroups(admin || role === 'client')
  const upstreams = usePlatformUpstreams(admin)
  const wallet = usePlatformWallet(targetUserId, Boolean(targetUserId))
  const keys = usePlatformKeys(targetUserId, Boolean(targetUserId))
  const usage = usePlatformUsage(targetUserId, Boolean(targetUserId))
  const createGroup = useCreatePlatformGroup()
  const updateGroup = useUpdatePlatformGroup()
  const deleteGroup = useDeletePlatformGroup()
  const createUpstream = useCreatePlatformUpstream()
  const updateUpstream = useUpdatePlatformUpstream()
  const deleteUpstream = useDeletePlatformUpstream()
  const testUpstream = useTestPlatformUpstream()
  const createKey = useCreatePlatformKey()
  const keyValue = usePlatformKeyValue()
  const adjustWallet = useAdjustPlatformWallet()
  const [groupOpen, setGroupOpen] = useState(false)
  const [editingGroupId, setEditingGroupId] = useState<string>()
  const [upstreamOpen, setUpstreamOpen] = useState(false)
  const [editingUpstreamId, setEditingUpstreamId] = useState<string>()
  const [keyOpen, setKeyOpen] = useState(false)
  const [adjustOpen, setAdjustOpen] = useState(false)
  const [visibleKeyValues, setVisibleKeyValues] = useState<Record<string, string>>({})
  const [groupForm] = Form.useForm()
  const [upstreamForm] = Form.useForm()
  const [keyForm] = Form.useForm()
  const [adjustForm] = Form.useForm()

  const openNewGroup = () => {
    setEditingGroupId(undefined)
    groupForm.resetFields()
    groupForm.setFieldsValue({ resource_class: 'economy', status: 'active' })
    setGroupOpen(true)
  }
  const openEditGroup = (group: PlatformGroup) => {
    setEditingGroupId(group.id)
    groupForm.resetFields()
    const multiplierBps = Number(group.labels?.['e2m.rate_multiplier_bps'] ?? '')
    groupForm.setFieldsValue({
      ...group,
      models: (group.models ?? []).join(', '),
      labels: Object.entries(group.labels ?? {})
        .filter(([key]) => key !== 'e2m.rate_multiplier_bps')
        .map(([key, value]) => `${key}=${value}`)
        .join(', '),
      rate_multiplier: Number.isFinite(multiplierBps) && multiplierBps > 0 ? multiplierBps / 10000 : undefined,
    })
    setGroupOpen(true)
  }
  const openNewUpstream = () => {
    setEditingUpstreamId(undefined)
    upstreamForm.resetFields()
    upstreamForm.setFieldsValue({
      group_id: (groups.data ?? []).find((group) => group.status === 'active')?.id,
      priority: 0,
      weight: 1,
      max_concurrency: 0,
      capacity_percent: 100,
      max_request_micros: 1,
      status: 'active',
    })
    setUpstreamOpen(true)
  }
  const openEditUpstream = (upstream: PlatformUpstream) => {
    const firstPrice = upstream.models.map((model) => upstream.prices?.[model]).find(Boolean)
    setEditingUpstreamId(upstream.id)
    upstreamForm.resetFields()
    upstreamForm.setFieldsValue({
      group_id: upstream.group_id,
      name: upstream.name,
      base_url: upstream.base_url,
      api_key: undefined,
      models: (upstream.models ?? []).join(', '),
      input_price: firstPrice ? firstPrice.input_micros_per_million / 1_000_000 : 0,
      output_price: firstPrice ? firstPrice.output_micros_per_million / 1_000_000 : 0,
      priority: upstream.priority,
      weight: upstream.weight,
      max_concurrency: upstream.capacity?.max_concurrency,
      capacity_percent: upstream.capacity?.capacity_percent,
      max_request_micros: upstream.capacity?.max_request_micros
        ? upstream.capacity.max_request_micros / 1_000_000
        : undefined,
      status: upstream.status,
      labels: Object.entries(upstream.labels ?? {})
        .map(([key, value]) => `${key}=${value}`)
        .join(', '),
    })
    setUpstreamOpen(true)
  }
  const testAndLoadModels = async () => {
    if (!editingUpstreamId) return
    const result = await testUpstream.mutateAsync(editingUpstreamId)
    if (result.ok && result.models?.length) {
      upstreamForm.setFieldValue('models', result.models.join(', '))
      message.info(`已从上游发现 ${result.models.length} 个模型，请确认后保存`)
    }
  }
  const closeGroupModal = () => {
    setGroupOpen(false)
    setEditingGroupId(undefined)
    groupForm.resetFields()
  }
  const closeUpstreamModal = () => {
    setUpstreamOpen(false)
    setEditingUpstreamId(undefined)
    upstreamForm.resetFields()
  }

  const groupMap = useMemo(
    () => new Map((groups.data ?? []).map((group) => [group.id, group])),
    [groups.data],
  )
  const selectTargetUser = (userId?: number) => {
    setSelectedUserId(userId)
    setVisibleKeyValues({})
  }
  const reload = () => {
    void groups.refetch()
    void upstreams.refetch()
    void wallet.refetch()
    void keys.refetch()
    void usage.refetch()
  }
  const copy = async (value: string) => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value)
      } else {
        const input = document.createElement('textarea')
        input.value = value
        input.style.position = 'fixed'
        input.style.opacity = '0'
        document.body.appendChild(input)
        input.select()
        if (!document.execCommand('copy')) throw new Error('copy failed')
        input.remove()
      }
      message.success('已复制')
    } catch {
      message.error('复制失败，请手动复制')
    }
  }
  const loadKeyValue = async (key: PlatformApiKey) => {
    const cached = visibleKeyValues[key.id]
    if (cached) return cached
    const result = await keyValue.mutateAsync({
      id: key.id,
      userId: admin ? targetUserId : undefined,
    })
    setVisibleKeyValues((current) => ({ ...current, [key.id]: result.value }))
    return result.value
  }
  const toggleKeyValue = async (key: PlatformApiKey) => {
    if (visibleKeyValues[key.id]) {
      setVisibleKeyValues((current) => {
        const next = { ...current }
        delete next[key.id]
        return next
      })
      return
    }
    await loadKeyValue(key)
  }
  const copyKeyValue = async (key: PlatformApiKey) => {
    const value = await loadKeyValue(key)
    await copy(value)
  }

  if (!admin && !user) return null
  return (
    <PageContainer
      title="平台分发"
      subTitle="E2M 原生管理平台分组、上游供给、客户余额和下游 API Key；Connector 自有号池不在此计费。"
      extra={
        <Button icon={<ReloadOutlined />} onClick={reload}>
          刷新
        </Button>
      }
    >
      {admin ? (
        <Card size="small" style={{ marginBottom: 16 }}>
          <Space wrap>
            <Typography.Text strong>客户视角</Typography.Text>
            <Select
              style={{ minWidth: 280 }}
              placeholder="选择客户查看钱包、Key 与用量"
              value={targetUserId}
              onChange={selectTargetUser}
              options={clients.map((item) => ({
                value: item.id,
                label: `${item.display_name || item.email} (#${item.id})`,
              }))}
            />
            <Button onClick={() => selectTargetUser(clients[0]?.id)} disabled={!clients.length}>
              选择首个客户
            </Button>
          </Space>
        </Card>
      ) : null}

      {!targetUserId && admin ? (
        <Alert
          type="info"
          showIcon
          message="请先创建或启用一个客户账号，再管理钱包、Key 和最近用量"
          style={{ marginBottom: 16 }}
        />
      ) : null}
      <StatisticCard.Group direction="row">
        <StatisticCard
          statistic={{ title: '平台分组', value: groups.data?.length ?? 0 }}
          loading={groups.isLoading}
        />
        <StatisticCard.Divider />
        <StatisticCard
          statistic={{ title: '平台上游', value: upstreams.data?.length ?? 0 }}
          loading={upstreams.isLoading}
        />
        <StatisticCard.Divider />
        <StatisticCard
          statistic={{
            title: '客户余额',
            value: wallet.data ? yuan(wallet.data.available_micros) : '—',
          }}
          loading={wallet.isLoading}
        />
        <StatisticCard.Divider />
        <StatisticCard
          statistic={{ title: '最近用量', value: usage.data?.count ?? 0 }}
          loading={usage.isLoading}
        />
      </StatisticCard.Group>

      {admin ? (
        <>
          <ProCard
            title="平台分组"
            bordered
            style={{ marginTop: 16 }}
            extra={
              <Button type="primary" icon={<PlusOutlined />} onClick={openNewGroup}>
                新建分组
              </Button>
            }
          >
            <Table
              rowKey="id"
              size="small"
              loading={groups.isLoading}
              dataSource={groups.data ?? []}
              pagination={false}
              columns={[
                { title: '名称', dataIndex: 'name' },
                {
                  title: '类型',
                  dataIndex: 'resource_class',
                  render: (v: string) => (
                    <Tag color={v === 'stable' ? 'blue' : 'gold'}>
                      {v === 'stable' ? '稳定池' : '低价池'}
                    </Tag>
                  ),
                },
                {
                  title: '模型',
                  dataIndex: 'models',
                  render: (v: string[]) => (v ?? []).join(', ') || '未限定',
                },
                {
                  title: '状态',
                  dataIndex: 'status',
                  render: (v: string) => (
                    <Tag color={v === 'active' ? 'green' : v === 'retired' ? 'default' : 'orange'}>
                      {v === 'active' ? '启用' : v === 'retired' ? '已退休' : '维护中'}
                    </Tag>
                  ),
                },
                {
                  title: '操作',
                  key: 'actions',
                  render: (_: unknown, row: PlatformGroup) => (
                    <Space size="small">
                      <Button
                        size="small"
                        icon={<EditOutlined />}
                        onClick={() => openEditGroup(row)}
                      >
                        编辑
                      </Button>
                      {row.status !== 'retired' ? (
                        <Button
                          size="small"
                          loading={updateGroup.isPending}
                          onClick={() =>
                            updateGroup.mutate({
                              id: row.id,
                              input: { status: row.status === 'active' ? 'maintenance' : 'active' },
                            })
                          }
                        >
                          {row.status === 'active' ? '停用' : '启用'}
                        </Button>
                      ) : null}
                      {row.status !== 'retired' ? (
                        <Popconfirm
                          title="确认退休此分组？"
                          description="退休后不会再接受新的请求，历史账务仍会保留。"
                          okText="确认退休"
                          cancelText="取消"
                          onConfirm={() => deleteGroup.mutate(row.id)}
                        >
                          <Button size="small" danger loading={deleteGroup.isPending}>
                            退休
                          </Button>
                        </Popconfirm>
                      ) : null}
                    </Space>
                  ),
                },
              ]}
            />
          </ProCard>
          <ProCard
            title="平台上游"
            bordered
            style={{ marginTop: 16 }}
            extra={
              <Button
                icon={<PlusOutlined />}
                onClick={openNewUpstream}
                disabled={!(groups.data ?? []).some((group) => group.status === 'active')}
              >
                接入上游
              </Button>
            }
          >
            <Table<PlatformUpstream>
              rowKey="id"
              size="small"
              loading={upstreams.isLoading}
              dataSource={upstreams.data ?? []}
              pagination={{ pageSize: 8 }}
              columns={[
                { title: '名称', dataIndex: 'name' },
                {
                  title: '分组',
                  dataIndex: 'group_id',
                  render: (v: string) => groupMap.get(v)?.name ?? v,
                },
                { title: '地址', dataIndex: 'base_url', ellipsis: true },
                {
                  title: '模型',
                  dataIndex: 'models',
                  render: (v: string[]) => (v ?? []).join(', '),
                },
                {
                  title: 'API Key',
                  dataIndex: 'api_key_masked',
                  render: (v: string) => v || '已配置',
                },
                {
                  title: '状态',
                  dataIndex: 'status',
                  render: (v: string) => (
                    <Tag color={v === 'active' ? 'green' : v === 'retired' ? 'default' : 'orange'}>
                      {v === 'active' ? '启用' : v === 'retired' ? '已退休' : '维护中'}
                    </Tag>
                  ),
                },
                { title: '优先级', dataIndex: 'priority' },
                { title: '权重', dataIndex: 'weight' },
                {
                  title: '容量',
                  render: (_: unknown, row: PlatformUpstream) =>
                    `${row.capacity?.capacity_percent ?? 100}% / ${row.capacity?.max_concurrency || '不限'}`,
                },
                {
                  title: '操作',
                  key: 'actions',
                  render: (_: unknown, row: PlatformUpstream) => (
                    <Space size="small" wrap>
                      <Button
                        size="small"
                        icon={<EditOutlined />}
                        onClick={() => openEditUpstream(row)}
                      >
                        编辑
                      </Button>
                      <Button
                        size="small"
                        icon={<ThunderboltOutlined />}
                        loading={testUpstream.isPending && testUpstream.variables === row.id}
                        onClick={() => testUpstream.mutate(row.id)}
                      >
                        测试
                      </Button>
                      {row.status !== 'retired' ? (
                        <Button
                          size="small"
                          loading={updateUpstream.isPending}
                          onClick={() =>
                            updateUpstream.mutate({
                              id: row.id,
                              input: { status: row.status === 'active' ? 'maintenance' : 'active' },
                            })
                          }
                        >
                          {row.status === 'active' ? '停用' : '启用'}
                        </Button>
                      ) : null}
                      {row.status !== 'retired' ? (
                        <Popconfirm
                          title="确认下线此上游？"
                          description="上游将停止接收新请求，凭证和审计记录会保留。"
                          okText="确认下线"
                          cancelText="取消"
                          onConfirm={() => deleteUpstream.mutate(row.id)}
                        >
                          <Button size="small" danger loading={deleteUpstream.isPending}>
                            下线
                          </Button>
                        </Popconfirm>
                      ) : null}
                    </Space>
                  ),
                },
              ]}
            />
          </ProCard>
        </>
      ) : null}

      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        <Col xs={24} lg={8}>
          <Card
            title="客户钱包"
            extra={
              admin ? (
                <Button size="small" onClick={() => setAdjustOpen(true)} disabled={!targetUserId}>
                  调整余额
                </Button>
              ) : null
            }
            loading={wallet.isLoading}
          >
            {wallet.data ? (
              <Space direction="vertical">
                <Statistic title="可用余额" value={yuan(wallet.data.available_micros)} />
                <Typography.Text type="secondary">
                  预占：{yuan(wallet.data.reserved_micros)} · {wallet.data.currency}
                </Typography.Text>
              </Space>
            ) : (
              <Typography.Text type="secondary">暂无钱包数据</Typography.Text>
            )}
          </Card>
        </Col>
        <Col xs={24} lg={16}>
          <Card
            title="下游 API Key"
            extra={
              <Button
                size="small"
                type="primary"
                icon={<PlusOutlined />}
                onClick={() => setKeyOpen(true)}
                disabled={!targetUserId || !groups.data?.length}
              >
                创建 Key
              </Button>
            }
            loading={keys.isLoading}
          >
            <Table<PlatformApiKey>
              rowKey="id"
              size="small"
              dataSource={keys.data ?? []}
              pagination={false}
              columns={[
                { title: '名称', dataIndex: 'name' },
                {
                  title: 'API Key',
                  dataIndex: 'prefix',
                  width: 340,
                  render: (prefix: string, row) => {
                    const value = visibleKeyValues[row.id]
                    return (
                      <Space.Compact style={{ width: '100%' }}>
                        <Input
                          value={value || `${prefix}••••••••••••••••`}
                          readOnly
                          style={{ fontFamily: 'monospace' }}
                        />
                        <Button
                          aria-label={value ? '隐藏 API Key' : '显示 API Key'}
                          icon={value ? <EyeInvisibleOutlined /> : <EyeOutlined />}
                          loading={keyValue.isPending && keyValue.variables?.id === row.id}
                          onClick={() => void toggleKeyValue(row)}
                        />
                        <Button
                          aria-label="复制 API Key"
                          icon={<CopyOutlined />}
                          loading={keyValue.isPending && keyValue.variables?.id === row.id}
                          onClick={() => void copyKeyValue(row)}
                        />
                      </Space.Compact>
                    )
                  },
                },
                {
                  title: '分组',
                  dataIndex: 'group_id',
                  render: (v: string) => groupMap.get(v)?.name ?? v,
                },
                {
                  title: '限额',
                  dataIndex: 'daily_limit_micros',
                  render: (v: number) => (v ? yuan(v) : '不限'),
                },
                {
                  title: '状态',
                  dataIndex: 'enabled',
                  render: (v: boolean) => (
                    <Tag color={v ? 'green' : 'default'}>{v ? '启用' : '停用'}</Tag>
                  ),
                },
              ]}
              locale={{ emptyText: '暂无平台 Key' }}
            />
          </Card>
        </Col>
      </Row>
      <ProCard title="最近用量" bordered style={{ marginTop: 16 }}>
        <Table<PlatformUsage>
          rowKey="id"
          size="small"
          loading={usage.isLoading}
          dataSource={usage.data?.items ?? []}
          pagination={{ pageSize: 10 }}
          columns={[
            {
              title: '时间',
              dataIndex: 'created_at',
              render: (v: string) => new Date(v).toLocaleString(),
            },
            { title: '模型', dataIndex: 'model' },
            { title: '类型', dataIndex: 'resource_class' },
            { title: 'Token', render: (_, row) => `${row.prompt_tokens + row.completion_tokens}` },
            { title: '扣费', dataIndex: 'settled_micros', render: (v: number) => yuan(v) },
            { title: '状态', dataIndex: 'status' },
          ]}
          locale={{ emptyText: '暂无用量记录' }}
        />
      </ProCard>

      <Modal
        title={editingGroupId ? '编辑平台分组' : '新建平台分组'}
        open={groupOpen}
        onCancel={closeGroupModal}
        confirmLoading={createGroup.isPending || updateGroup.isPending}
        onOk={() => groupForm.submit()}
      >
        <Form
          form={groupForm}
          layout="vertical"
          onFinish={async (values) => {
            const input = {
              ...values,
              labels: parseLabels(values.labels),
              models: String(values.models ?? '')
                .split(',')
                .map((model) => model.trim())
                .filter(Boolean),
              rate_multiplier:
                values.rate_multiplier != null && values.rate_multiplier !== ''
                  ? String(values.rate_multiplier)
                  : undefined,
            }
            if (editingGroupId) {
              await updateGroup.mutateAsync({ id: editingGroupId, input })
            } else {
              await createGroup.mutateAsync(input)
            }
            closeGroupModal()
          }}
          initialValues={{ resource_class: 'economy', status: 'active' }}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input placeholder="低价池 / 稳定池" />
          </Form.Item>
          <Form.Item name="resource_class" label="资源类型" rules={[{ required: true }]}>
            <Select
              options={[
                { value: 'economy', label: '低价池' },
                { value: 'stable', label: '稳定池' },
              ]}
            />
          </Form.Item>
          <Form.Item name="provider" label="供应商说明">
            <Input />
          </Form.Item>
          <Form.Item
            name="rate_multiplier"
            label="售价倍率"
            extra="基于基准价目表的售价倍数（如 1.25）；留空保持 1。创建上游不填价格时按 基准价×汇率×倍率 自动定价。"
          >
            <InputNumber min={0.0001} max={100} step={0.05} style={{ width: '100%' }} placeholder="1" />
          </Form.Item>
          <Form.Item name="models" label="模型（逗号分隔）">
            <Input placeholder="gpt-4o-mini, claude-3-5-sonnet" />
          </Form.Item>
          <Form.Item name="region" label="区域">
            <Input placeholder="可选，例如 cn 或 us" />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="labels" label="标签（key=value，逗号分隔）">
            <Input placeholder="tier=premium, owner=ops" />
          </Form.Item>
          {editingGroupId ? (
            <Form.Item name="status" label="状态">
              <Select
                options={[
                  { value: 'active', label: '启用' },
                  { value: 'maintenance', label: '维护中' },
                ]}
              />
            </Form.Item>
          ) : null}
        </Form>
      </Modal>
      <Modal
        title={editingUpstreamId ? '编辑平台上游' : '接入平台上游'}
        open={upstreamOpen}
        onCancel={closeUpstreamModal}
        confirmLoading={createUpstream.isPending || updateUpstream.isPending}
        onOk={() => upstreamForm.submit()}
      >
        <Form
          form={upstreamForm}
          layout="vertical"
          onFinish={async (values) => {
            const models = String(values.models ?? values.model ?? '')
              .split(',')
              .map((model) => model.trim())
              .filter(Boolean)
            const prices = Object.fromEntries(
              models.map((model) => [
                model,
                {
                  input_micros_per_million: Math.round((values.input_price || 0) * 1_000_000),
                  output_micros_per_million: Math.round((values.output_price || 0) * 1_000_000),
                },
              ]),
            )
            const input = {
              group_id: values.group_id,
              name: values.name,
              base_url: values.base_url,
              ...(values.api_key ? { api_key: values.api_key } : {}),
              models,
              prices,
              priority: values.priority,
              weight: values.weight,
              status: values.status,
              labels: parseLabels(values.labels),
              capacity: {
                max_concurrency: values.max_concurrency || 0,
                capacity_percent: values.capacity_percent ?? 100,
                max_request_micros: Math.round((values.max_request_micros || 1) * 1_000_000),
              },
            }
            if (editingUpstreamId) {
              await updateUpstream.mutateAsync({ id: editingUpstreamId, input })
            } else {
              await createUpstream.mutateAsync({ ...input, api_key: values.api_key as string })
            }
            closeUpstreamModal()
          }}
          initialValues={{
            priority: 0,
            weight: 1,
            capacity_percent: 100,
            max_request_micros: 1,
            status: 'active',
          }}
        >
          <Alert
            type="info"
            showIcon
            message="当前数据面支持 OpenAI-compatible API（Bearer 凭证与 /v1/chat/completions）；其他协议需先接入适配器。"
            style={{ marginBottom: 16 }}
          />
          <Form.Item name="group_id" label="平台分组" rules={[{ required: true }]}>
            <Select
              disabled={Boolean(editingUpstreamId)}
              options={(groups.data ?? []).map((g) => ({
                value: g.id,
                label: g.name,
                disabled: g.status !== 'active',
              }))}
            />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="base_url" label="Base URL" rules={[{ required: true }]}>
            <Input placeholder="https://api.example.com/v1" />
          </Form.Item>
          <Form.Item
            name="api_key"
            label={editingUpstreamId ? '上游 API Key（留空表示不更换）' : '上游 API Key'}
            rules={editingUpstreamId ? [] : [{ required: true }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item label="模型（逗号分隔）" required>
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="models" noStyle rules={[{ required: true }]}>
                <Input placeholder="gpt-4o-mini, gpt-4.1-mini" />
              </Form.Item>
              {editingUpstreamId ? (
                <Button
                  icon={<ThunderboltOutlined />}
                  loading={testUpstream.isPending}
                  onClick={() => void testAndLoadModels()}
                >
                  测试并拉取
                </Button>
              ) : null}
            </Space.Compact>
          </Form.Item>
          <Space>
            <Form.Item
              name="input_price"
              label="输入价（元/百万 Token）"
              rules={[{ required: true }]}
            >
              <InputNumber min={0} />
            </Form.Item>
            <Form.Item
              name="output_price"
              label="输出价（元/百万 Token）"
              rules={[{ required: true }]}
            >
              <InputNumber min={0} />
            </Form.Item>
          </Space>
          <Space wrap>
            <Form.Item name="priority" label="优先级">
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="weight" label="权重">
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="max_concurrency" label="最大并发">
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="capacity_percent" label="容量百分比">
              <InputNumber min={0} max={100} precision={0} />
            </Form.Item>
            <Form.Item name="max_request_micros" label="单请求上限（元）">
              <InputNumber min={0.000001} precision={6} />
            </Form.Item>
          </Space>
          {editingUpstreamId ? (
            <Form.Item name="status" label="状态">
              <Select
                options={[
                  { value: 'active', label: '启用' },
                  { value: 'maintenance', label: '维护中' },
                ]}
              />
            </Form.Item>
          ) : null}
          <Form.Item name="labels" label="标签（key=value，逗号分隔）">
            <Input placeholder="region=cn, lane=primary" />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        title="创建下游 API Key"
        open={keyOpen}
        onCancel={() => setKeyOpen(false)}
        confirmLoading={createKey.isPending}
        onOk={() => keyForm.submit()}
      >
        <Form<PlatformKeyInput>
          form={keyForm}
          layout="vertical"
          onFinish={async (values) => {
            const result = await createKey.mutateAsync({
              ...values,
              user_id: targetUserId,
              daily_limit_micros: Math.round((values.daily_limit_micros ?? 0) * 1_000_000),
            })
            setVisibleKeyValues((current) => ({
              ...current,
              [result.key.id]: result.plaintext_key,
            }))
            setKeyOpen(false)
            keyForm.resetFields()
          }}
        >
          <Form.Item name="group_id" label="平台分组" rules={[{ required: true }]}>
            <Select
              options={(groups.data ?? []).map((g) => ({
                value: g.id,
                label: `${g.name}（${g.resource_class === 'stable' ? '稳定' : '低价'}）`,
              }))}
            />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="daily_limit_micros" label="每日额度（元）">
            <InputNumber min={0} style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        title="调整客户余额"
        open={adjustOpen}
        onCancel={() => setAdjustOpen(false)}
        confirmLoading={adjustWallet.isPending}
        onOk={() => adjustForm.submit()}
      >
        <Form
          form={adjustForm}
          layout="vertical"
          onFinish={async (values) => {
            await adjustWallet.mutateAsync({
              user_id: targetUserId!,
              amount_micros: Math.round(values.amount * 1_000_000),
              currency: 'CNY',
              reason: values.reason,
            })
            setAdjustOpen(false)
            adjustForm.resetFields()
          }}
        >
          <Form.Item name="amount" label="金额（元，负数为扣减）" rules={[{ required: true }]}>
            <InputNumber style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="reason" label="原因" rules={[{ required: true }]}>
            <Input.TextArea />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
