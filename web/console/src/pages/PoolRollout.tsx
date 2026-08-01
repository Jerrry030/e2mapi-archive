import { useMemo, useState } from 'react'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { useInstances, useUpstreamPools, useUsers } from '../api/hooks'
import {
  useDeletePoolRolloutTarget,
  usePoolRolloutPreview,
  useUpsertPoolRolloutTarget,
} from '../api/poolRolloutHooks'
import type {
  PoolRolloutMode,
  PoolRolloutOperation,
  PoolRolloutScope,
  PoolRolloutTarget,
  PoolRolloutTargetInput,
} from '../api/poolRollout'

const rolloutOptions = [
  { value: 'immediate', label: '立即全量' },
  { value: 'canary', label: '金丝雀灰度' },
  { value: 'batched', label: '分批灰度' },
]

const operationStatusView = {
  pending: { label: '等待执行', color: 'default' },
  running: { label: '执行中', color: 'processing' },
  succeeded: { label: '已完成', color: 'success' },
  failed: { label: '执行失败', color: 'error' },
  superseded: { label: '已被新规则替代', color: 'default' },
} as const

type FormValues = PoolRolloutTargetInput

export default function PoolRollout() {
  const pools = useUpstreamPools()
  const users = useUsers()
  const instances = useInstances()
  const [poolId, setPoolId] = useState<string>()
  const [editing, setEditing] = useState<PoolRolloutTarget | null>(null)
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm<FormValues>()
  const preview = usePoolRolloutPreview(poolId)
  const upsert = useUpsertPoolRolloutTarget()
  const remove = useDeletePoolRolloutTarget()
  const scope = Form.useWatch('scope', form)
  const userId = Form.useWatch('user_id', form)
  const rollout = Form.useWatch('rollout', form)

  const clientUsers = useMemo(
    () => (users.data ?? []).filter((user) => user.enabled && user.roles.includes('client')),
    [users.data],
  )
  const userById = useMemo(
    () => new Map((users.data ?? []).map((user) => [user.id, user])),
    [users.data],
  )
  const instanceById = useMemo(
    () => new Map((instances.data ?? []).map((instance) => [instance.id, instance])),
    [instances.data],
  )
  const eligibleInstances = (instances.data ?? []).filter(
    (instance) => !userId || instance.user_id === userId,
  )

  const openCreate = () => {
    setEditing(null)
    form.setFieldsValue({ scope: 'user', enabled: true, rollout: 'immediate' })
    setOpen(true)
  }

  const openEdit = (target: PoolRolloutTarget) => {
    setEditing(target)
    form.setFieldsValue({
      scope: target.scope,
      user_id: target.user_id,
      instance_id: target.instance_id,
      enabled: target.enabled,
      rollout: target.rollout,
      rollout_batch_size: target.rollout_batch_size,
      rollout_canary_count: target.rollout_canary_count,
      note: target.note,
    })
    setOpen(true)
  }

  return (
    <PageContainer
      title="客户投放范围"
      subTitle="明确决定哪个客户或实例可以接入号池；没有规则时默认不投放"
      extra={
        <Button icon={<ReloadOutlined />} onClick={() => preview.refetch()} disabled={!poolId}>
          刷新
        </Button>
      }
    >
      <Alert
        type="info"
        showIcon
        message="实例规则优先于客户规则"
        description="先给客户设置整体开关，再用实例规则做例外或小范围灰度。停用会触发已托管线路撤流；重新启用按所选灰度方式恢复。"
        style={{ marginBottom: 16 }}
      />
      <ProCard>
        <Space wrap style={{ marginBottom: 16 }}>
          <Typography.Text strong>号池</Typography.Text>
          <Select
            style={{ width: 320 }}
            placeholder="选择要管理的号池"
            value={poolId}
            onChange={setPoolId}
            options={(pools.data ?? []).map((pool) => ({
              value: pool.id,
              label: `${pool.name} · ${pool.status}`,
            }))}
          />
          <Button type="primary" icon={<PlusOutlined />} disabled={!poolId} onClick={openCreate}>
            添加投放规则
          </Button>
        </Space>

        <Table<PoolRolloutTarget>
          rowKey="id"
          loading={preview.isLoading}
          dataSource={preview.data?.targets ?? []}
          pagination={false}
          locale={{ emptyText: poolId ? '当前号池尚未授权任何客户' : '请先选择号池' }}
          columns={[
            {
              title: '范围',
              dataIndex: 'scope',
              render: (value: PoolRolloutScope) => (
                <Tag color={value === 'instance' ? 'purple' : 'blue'}>
                  {value === 'instance' ? '实例例外' : '客户规则'}
                </Tag>
              ),
            },
            {
              title: '客户',
              dataIndex: 'user_id',
              render: (value: number) =>
                userById.get(value)?.display_name || userById.get(value)?.email || value,
            },
            {
              title: '实例',
              dataIndex: 'instance_id',
              render: (value?: string) =>
                value ? instanceById.get(value)?.name || value : '全部实例',
            },
            {
              title: '服务状态',
              dataIndex: 'enabled',
              render: (enabled: boolean) => (
                <Tag color={enabled ? 'green' : 'default'}>{enabled ? '已启用' : '已停用'}</Tag>
              ),
            },
            {
              title: '接入方式',
              dataIndex: 'rollout',
              render: (value: PoolRolloutMode, row) => {
                const label = rolloutOptions.find((item) => item.value === value)?.label ?? value
                const count = value === 'canary' ? row.rollout_canary_count : row.rollout_batch_size
                return `${label}${count ? `（每批 ${count}）` : ''}`
              },
            },
            { title: '备注', dataIndex: 'note', ellipsis: true },
            {
              title: '操作',
              render: (_, target) => (
                <Space>
                  <Button type="link" onClick={() => openEdit(target)}>
                    编辑
                  </Button>
                  <Popconfirm
                    title="删除这条规则？"
                    description="删除后将恢复客户规则或默认拒绝。"
                    onConfirm={() =>
                      poolId &&
                      remove.mutate({
                        poolId,
                        input: {
                          scope: target.scope,
                          user_id: target.user_id,
                          instance_id: target.instance_id,
                        },
                      })
                    }
                  >
                    <Button type="link" danger>
                      删除
                    </Button>
                  </Popconfirm>
                </Space>
              ),
            },
          ]}
        />
      </ProCard>

      <ProCard title="网关执行状态" style={{ marginTop: 16 }}>
        <Alert
          type="warning"
          showIcon
          message="配置保存与网关执行是两个阶段"
          description="停用规则只有在“撤流 · 已完成”后才代表线路已从网关调度中移除；失败任务会由后台自动重试。"
          style={{ marginBottom: 16 }}
        />
        <Table<PoolRolloutOperation>
          rowKey="id"
          size="small"
          pagination={{ pageSize: 10, hideOnSinglePage: true }}
          loading={preview.isFetching}
          dataSource={preview.data?.operations ?? []}
          locale={{ emptyText: poolId ? '暂无待执行或历史任务' : '请先选择号池' }}
          columns={[
            {
              title: '动作',
              dataIndex: 'action',
              render: (action) => (
                <Tag color={action === 'drain' ? 'orange' : 'blue'}>
                  {action === 'drain' ? '撤流' : '接入/恢复'}
                </Tag>
              ),
            },
            {
              title: '客户',
              dataIndex: 'user_id',
              render: (value: number) =>
                userById.get(value)?.display_name || userById.get(value)?.email || value,
            },
            {
              title: '实例',
              dataIndex: 'instance_id',
              render: (value: string) => instanceById.get(value)?.name || value,
            },
            {
              title: '状态',
              dataIndex: 'status',
              render: (status: PoolRolloutOperation['status']) => {
                const view = operationStatusView[status]
                return <Tag color={view.color}>{view.label}</Tag>
              },
            },
            { title: '尝试次数', dataIndex: 'attempts', width: 100 },
            {
              title: '失败原因',
              dataIndex: 'last_error',
              ellipsis: true,
              render: (value?: string) => value || '—',
            },
            {
              title: '最近更新',
              dataIndex: 'updated_at',
              render: (value: string) => new Date(value).toLocaleString(),
            },
          ]}
        />
      </ProCard>

      <Modal
        title={editing ? '编辑投放规则' : '添加投放规则'}
        open={open}
        confirmLoading={upsert.isPending}
        onCancel={() => {
          setOpen(false)
          form.resetFields()
        }}
        onOk={() => form.submit()}
        destroyOnClose
      >
        <Form<FormValues>
          form={form}
          layout="vertical"
          onFinish={(values) => {
            if (!poolId) return
            upsert.mutate(
              { poolId, input: values },
              {
                onSuccess: () => {
                  setOpen(false)
                  form.resetFields()
                },
              },
            )
          }}
        >
          <Form.Item name="scope" label="规则范围" rules={[{ required: true }]}>
            <Select
              disabled={Boolean(editing)}
              options={[
                { value: 'user', label: '整个客户（包含当前及未来实例）' },
                { value: 'instance', label: '单个实例（覆盖客户规则）' },
              ]}
            />
          </Form.Item>
          <Form.Item name="user_id" label="客户" rules={[{ required: true }]}>
            <Select
              showSearch
              disabled={Boolean(editing)}
              optionFilterProp="label"
              options={clientUsers.map((user) => ({
                value: user.id,
                label: user.display_name || user.email,
              }))}
            />
          </Form.Item>
          {scope === 'instance' && (
            <Form.Item name="instance_id" label="实例" rules={[{ required: true }]}>
              <Select
                disabled={Boolean(editing)}
                options={eligibleInstances.map((instance) => ({
                  value: instance.id,
                  label: instance.name,
                }))}
              />
            </Form.Item>
          )}
          <Form.Item name="enabled" label="是否提供服务" valuePropName="checked">
            <Switch checkedChildren="启用" unCheckedChildren="停用" />
          </Form.Item>
          <Form.Item name="rollout" label="首次接入/恢复方式" rules={[{ required: true }]}>
            <Select options={rolloutOptions} />
          </Form.Item>
          {rollout === 'canary' && (
            <Form.Item name="rollout_canary_count" label="首批启用线路数" initialValue={1}>
              <InputNumber min={1} precision={0} style={{ width: '100%' }} />
            </Form.Item>
          )}
          {rollout === 'batched' && (
            <Form.Item name="rollout_batch_size" label="每批启用线路数" initialValue={1}>
              <InputNumber min={1} precision={0} style={{ width: '100%' }} />
            </Form.Item>
          )}
          <Form.Item name="note" label="运营备注">
            <Input.TextArea
              maxLength={300}
              showCount
              placeholder="例如：首批试运营客户，观察 24 小时"
            />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
