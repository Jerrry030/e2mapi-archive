import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  App,
  Alert,
  Button,
  Input,
  InputNumber,
  Modal,
  Progress,
  Select,
  Space,
  Tag,
  Typography,
} from 'antd'
import { PageContainer } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { RelativeTime } from '../components/common'
import { friendlyErrorMessage } from '../api/errors'
import {
  upstreamLifecycleApi,
  type InventoryImportEntry,
  type InventoryItem,
  type InventoryState,
  type KeyRotation,
  type PoolRetirementJob,
} from '../api/upstreamLifecycle'
import { endpoints } from '../api/endpoints'

const inventoryLabels: Record<InventoryState, string> = {
  draft: '草稿',
  testing: '质检中',
  ready: '可分配',
  quarantined: '已隔离',
  retired: '已报废',
}

export default function UpstreamInventory() {
  const { message } = App.useApp()
  const qc = useQueryClient()
  const [poolId, setPoolId] = useState<string>()
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState('')
  const [rotation, setRotation] = useState<KeyRotation>()
  const [rotationValue, setRotationValue] = useState('')
  const pools = useQuery({ queryKey: ['upstream-pools'], queryFn: endpoints.listUpstreamPools })
  const inventory = useQuery({
    queryKey: ['upstream-inventory', poolId ?? 'all'],
    queryFn: () => upstreamLifecycleApi.inventory(poolId),
    refetchInterval: 20_000,
  })
  const retirements = useQuery({
    queryKey: ['pool-retirement-jobs', poolId ?? 'all'],
    queryFn: () => upstreamLifecycleApi.retirementJobs(poolId),
  })
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['upstream-inventory'] })
    qc.invalidateQueries({ queryKey: ['upstream-channels'] })
    qc.invalidateQueries({ queryKey: ['pool-retirement-jobs'] })
  }
  const mutation = useMutation({
    mutationFn: async (action: () => Promise<unknown>) => action(),
    onSuccess: () => {
      refresh()
      message.success('操作已提交')
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })

  const summary = useMemo(
    () => inventory.data?.pools.find((item) => item.pool_id === poolId),
    [inventory.data, poolId],
  )
  const columns: ProColumns<InventoryItem>[] = [
    { title: 'Key 资源', render: (_, item) => item.channel.display_name },
    { title: '来源', render: (_, item) => item.channel.source_id || '-' },
    {
      title: '库存状态',
      width: 145,
      render: (_, item) => (
        <Select
          size="small"
          value={item.inventory_state}
          style={{ width: 120 }}
          options={(Object.keys(inventoryLabels) as InventoryState[]).map((value) => ({
            value,
            label: inventoryLabels[value],
          }))}
          disabled={mutation.isPending || (item.allocated && item.inventory_state !== 'retired')}
          onChange={(state) =>
            mutation.mutate(() => upstreamLifecycleApi.setInventoryState(item.channel.id, state))
          }
        />
      ),
    },
    {
      title: '归属',
      render: (_, item) =>
        item.allocated ? <Tag color="blue">用户 {item.allocated_user_id}</Tag> : <Tag>未分配</Tag>,
    },
    {
      title: '证明 / 部署',
      width: 180,
      render: (_, item) =>
        `${item.proof_verified}/${item.target_instances} · ${item.deployments_deployed}/${item.target_instances}`,
    },
    {
      title: 'Key 版本',
      width: 100,
      render: (_, item) =>
        item.delivery ? `v${item.delivery.key_version}` : <Tag color="red">未写入</Tag>,
    },
    {
      title: '操作',
      valueType: 'option',
      render: (_, item) =>
        item.delivery ? (
          <a
            onClick={async () => {
              try {
                setRotation(await upstreamLifecycleApi.rotation(item.channel.id))
              } catch (error) {
                message.error(friendlyErrorMessage(error))
              }
            }}
          >
            轮换
          </a>
        ) : null,
    },
  ]
  const retirementColumns: ProColumns<PoolRetirementJob>[] = [
    { title: '任务', dataIndex: 'id', ellipsis: true },
    { title: '状态', dataIndex: 'status', render: (value) => <Tag>{String(value)}</Tag> },
    {
      title: '撤流进度',
      render: (_, job) => (
        <Progress
          percent={
            job.total_plans ? Math.round((job.completed_plans / job.total_plans) * 100) : 100
          }
          size="small"
        />
      ),
    },
    {
      title: '清理进度',
      render: (_, job) => (
        <Progress
          percent={
            job.total_plans
              ? Math.round((job.cleanup_completed_plans / job.total_plans) * 100)
              : 100
          }
          status={job.cleanup_failed_plans ? 'exception' : undefined}
          size="small"
        />
      ),
    },
    { title: '失败', dataIndex: 'failed_plans' },
    { title: '更新时间', render: (_, job) => <RelativeTime value={job.updated_at} /> },
    {
      title: '操作',
      valueType: 'option',
      render: (_, job) =>
        job.status !== 'completed' ? (
          <a onClick={() => mutation.mutate(() => upstreamLifecycleApi.runRetirementJob(job.id))}>
            继续执行
          </a>
        ) : null,
    },
  ]

  const parseImport = (): InventoryImportEntry[] => {
    const rows = importText
      .split('\n')
      .map((line) => line.trim())
      .filter(Boolean)
    return rows.map((line, index) => {
      const [source_id, display_name, value, provider] = line.split(',').map((part) => part.trim())
      if (!source_id || !display_name || !value)
        throw new Error(`第 ${index + 1} 行缺少来源、名称或 Key`)
      return { source_id, display_name, value, provider }
    })
  }

  return (
    <PageContainer title="号池库存运营">
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="新导入的 Key 默认为草稿且不参与分配；质检完成后逐条改为“可分配”。已归属的 Key 不会重新转让。"
      />
      {(inventory.data?.alerts ?? []).map((alert) => (
        <Alert
          key={alert.pool_id}
          type="warning"
          showIcon
          style={{ marginBottom: 8 }}
          message={`安全库存不足：可用 ${alert.available}，阈值 ${alert.threshold}`}
        />
      ))}
      <Space style={{ marginBottom: 16 }} wrap>
        <Select
          value={poolId}
          placeholder="选择号池"
          style={{ width: 240 }}
          options={(pools.data ?? []).map((pool) => ({ value: pool.id, label: pool.name }))}
          onChange={setPoolId}
        />
        {poolId && (
          <InputNumber
            min={0}
            value={summary?.safety_stock_threshold ?? 0}
            addonBefore="安全库存"
            onChange={(value) =>
              mutation.mutate(() => upstreamLifecycleApi.setSafetyStock(poolId, value ?? 0))
            }
          />
        )}
        <Button type="primary" disabled={!poolId} onClick={() => setImportOpen(true)}>
          批量导入
        </Button>
        <Button
          danger
          disabled={!poolId}
          onClick={() =>
            poolId && mutation.mutate(() => upstreamLifecycleApi.createRetirementJob(poolId))
          }
        >
          创建退出任务
        </Button>
      </Space>
      <ProTable<InventoryItem>
        rowKey={(item) => item.channel.id}
        search={false}
        loading={inventory.isLoading}
        dataSource={inventory.data?.items ?? []}
        columns={columns}
        scroll={{ x: 'max-content' }}
      />
      <Typography.Title level={4}>号池退出任务</Typography.Title>
      <ProTable<PoolRetirementJob>
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={retirements.data ?? []}
        columns={retirementColumns}
      />

      <Modal
        title="批量导入 Key"
        open={importOpen}
        confirmLoading={mutation.isPending}
        okText="导入为草稿"
        onCancel={() => setImportOpen(false)}
        onOk={() => {
          if (!poolId) return
          try {
            const entries = parseImport()
            mutation.mutate(() => upstreamLifecycleApi.importInventory(poolId, entries), {
              onSuccess: () => {
                setImportOpen(false)
                setImportText('')
              },
            })
          } catch (error) {
            message.error(friendlyErrorMessage(error))
          }
        }}
      >
        <Alert
          type="warning"
          showIcon
          message="每行格式：来源ID,显示名称,API Key,服务商（可选）。API Key 只在本次安全写入时发送。"
          style={{ marginBottom: 12 }}
        />
        <Input.TextArea
          rows={8}
          value={importText}
          onChange={(event) => setImportText(event.target.value)}
          placeholder="source-a,A-001,sk-...,OpenAI"
        />
      </Modal>

      <Modal
        title="Key 双版本轮换"
        open={!!rotation}
        onCancel={() => {
          setRotation(undefined)
          setRotationValue('')
        }}
        footer={
          <Space>
            <Button
              disabled={!rotation?.can_rollback}
              onClick={() =>
                rotation &&
                mutation.mutate(() => upstreamLifecycleApi.rollbackRotation(rotation.channel_id), {
                  onSuccess: (data) => setRotation(data as KeyRotation),
                })
              }
            >
              回滚上一版
            </Button>
            <Button
              disabled={!rotation?.can_finalize}
              danger
              onClick={() =>
                rotation &&
                mutation.mutate(() => upstreamLifecycleApi.finalizeRotation(rotation.channel_id), {
                  onSuccess: (data) => setRotation(data as KeyRotation),
                })
              }
            >
              确认销毁旧版
            </Button>
            <Button
              type="primary"
              disabled={!rotationValue.trim()}
              onClick={() =>
                rotation &&
                mutation.mutate(
                  () => upstreamLifecycleApi.startRotation(rotation.channel_id, rotationValue),
                  {
                    onSuccess: (data) => {
                      setRotation(data as KeyRotation)
                      setRotationValue('')
                    },
                  },
                )
              }
            >
              开始轮换
            </Button>
          </Space>
        }
      >
        {rotation && (
          <Alert
            type={rotation.can_finalize ? 'success' : 'warning'}
            showIcon
            style={{ marginBottom: 12 }}
            message={`当前 v${rotation.current_key_version}，目标实例确认 ${rotation.confirmed_instances}/${rotation.target_instances}`}
          />
        )}
        <Input.Password
          value={rotationValue}
          onChange={(event) => setRotationValue(event.target.value)}
          placeholder="输入新 API Key"
        />
      </Modal>
    </PageContainer>
  )
}
