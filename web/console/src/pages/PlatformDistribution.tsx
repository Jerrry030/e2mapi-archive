import { useMemo, useState } from 'react'
import { PageContainer, ProCard, StatisticCard } from '@ant-design/pro-components'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Modal,
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
  EyeInvisibleOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { getActiveRole } from '../api/auth'
import { useActiveRoleUser, useUsers } from '../api/hooks'
import {
  useAdjustPlatformWallet,
  useCreatePlatformKey,
  usePlatformGroups,
  usePlatformKeys,
  usePlatformKeyValue,
  usePlatformUsage,
  usePlatformWallet,
} from '../api/platformDistributionHooks'
import type { PlatformKeyInput } from '../api/endpoints'
import type { PlatformApiKey, PlatformUsage } from '../api/types'

const yuan = (micros: number) => `¥${(micros / 1_000_000).toFixed(2)}`

// PlatformDistribution is the distribution-operations page: it serves the
// customer-facing wallet, downstream keys, and usage. Group and upstream
// management live on their own dedicated admin pages.
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
  const wallet = usePlatformWallet(targetUserId, Boolean(targetUserId))
  const keys = usePlatformKeys(targetUserId, Boolean(targetUserId))
  const usage = usePlatformUsage(targetUserId, Boolean(targetUserId))
  const createKey = useCreatePlatformKey()
  const keyValue = usePlatformKeyValue()
  const adjustWallet = useAdjustPlatformWallet()
  const [keyOpen, setKeyOpen] = useState(false)
  const [adjustOpen, setAdjustOpen] = useState(false)
  const [visibleKeyValues, setVisibleKeyValues] = useState<Record<string, string>>({})
  const [keyForm] = Form.useForm()
  const [adjustForm] = Form.useForm()

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
      subTitle="客户余额、下游 API Key 与用量。分组与上游账号在「分组管理」「上游账号」页维护。"
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
            <Button type="link" href="/platform-groups">
              分组管理 →
            </Button>
            <Button type="link" href="/platform-upstreams">
              上游账号 →
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
            { title: 'Token', render: (_, row) => `${row.prompt_tokens + row.completion_tokens}` },
            { title: '扣费', dataIndex: 'settled_micros', render: (v: number) => yuan(v) },
            { title: '状态', dataIndex: 'status' },
          ]}
          locale={{ emptyText: '暂无用量记录' }}
        />
      </ProCard>

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
            const expiresAt = (values as { expires_at?: { toDate?: () => Date } }).expires_at
            const result = await createKey.mutateAsync({
              ...values,
              user_id: targetUserId,
              daily_limit_micros: Math.round((values.daily_limit_micros ?? 0) * 1_000_000),
              expires_at: expiresAt?.toDate ? expiresAt.toDate().toISOString() : undefined,
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
                label: g.name,
              }))}
            />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="daily_limit_micros" label="每日额度（元）">
            <InputNumber min={0} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="expires_at" label="有效期至" extra="留空表示长期有效；到期后 Key 自动失效。">
            <DatePicker showTime style={{ width: '100%' }} />
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
