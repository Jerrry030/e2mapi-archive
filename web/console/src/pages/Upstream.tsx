import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  ModalForm,
  PageContainer,
  ProCard,
  ProFormDependency,
  ProFormGroup,
  ProFormDigit,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import {
  Alert,
  App,
  Button,
  Descriptions,
  Popconfirm,
  Progress,
  Space,
  Tag,
  Typography,
} from 'antd'
import {
  DeleteOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
  RollbackOutlined,
  SendOutlined,
  SettingOutlined,
} from '@ant-design/icons'
import {
  useAutoSwitchSummary,
  useCreateRoutePlan,
  useCreateUpstreamChannel,
  useCreateUpstreamPool,
  useDeleteRouteStrategy,
  useEvaluateAutoSwitch,
  useInstances,
  useObserveAutoSwitchDecision,
  usePublishedBindings,
  useReconcileRoutePlan,
  useReconcileRuns,
  useRollbackRoutePlan,
  useRoutePlans,
  useRouteStrategies,
  useUpdateRoutePlan,
  useUpdateUpstreamChannel,
  useUpdateUpstreamPool,
  useUpsertRouteStrategy,
  useUpstreamChannels,
  useUpstreamPools,
  useUsers,
} from '../api/hooks'
import {
  useApproveAutoSwitchDecision,
  useExecuteAutoSwitchDecision,
  useRejectAutoSwitchDecision,
} from '../api/recoveryActionHooks'
import type { RoutePlanInput, UpstreamChannelInput, UpstreamPoolInput } from '../api/endpoints'
import { friendlyInlineError } from '../api/errors'
import { EmptyTeach, RelativeTime } from '../components/common'
import { UserSelect } from '../components/fields'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type {
  AutoSwitchChannelHealth,
  AutoSwitchDecision,
  AutoSwitchSummary,
  GatewayAccountOwnership,
  PublishedBinding,
  QualityProbeCapability,
  QualityProbeEndpointPath,
  ReconcileAction,
  ReconcilePlan,
  ReconcileRun,
  RolloutMode,
  RoutePlan,
  RoutePlanStatus,
  RouteStrategy,
  RouteStrategyType,
  StrategyScope,
  StrategyWeights,
  UpstreamChannel,
  UpstreamChannelStatus,
  UpstreamPool,
  UpstreamPoolStatus,
} from '../api/types'
import { formatLabels, labelsFieldValidator, parseLabels } from './labelsForm'
import { strategyFromForm, strategyValidationError } from './routeStrategyForm'
import {
  circuitReasonText,
  circuitStateView,
  decisionDetailText,
  decisionImpactText,
  failureScopeText,
  penaltyBreakdownText,
  recoveryProgressText,
  schedulingStateView,
} from './autoSwitchView'
import { reconcileDetailLabel } from '../i18n'
import { selectedPlanFromLocation, upstreamLocationFromSearch } from './upstreamLocation'

const poolStatusOptions = [
  { value: 'active', label: '生效' },
  { value: 'maintenance', label: '维护' },
  { value: 'retired', label: '退役' },
]

const channelStatusOptions = poolStatusOptions
const accountOwnershipOptions = [
  { value: 'platform_managed', label: '平台托管' },
  { value: 'owner_provided', label: '用户自有' },
]
const accountOwnershipLabels: Record<GatewayAccountOwnership, string> = {
  platform_managed: '平台托管',
  owner_provided: '用户自有',
}
const probeCapabilityOptions = [
  { value: 'disabled', label: '关闭' },
  { value: 'text_stream', label: '文本流探测' },
]
const probeEndpointOptions = [
  { value: '/v1/messages', label: '/v1/messages' },
  { value: '/v1/responses', label: '/v1/responses' },
  { value: '/v1/chat/completions', label: '/v1/chat/completions' },
]
const planStatusOptions = [
  { value: 'draft', label: '草稿' },
  { value: 'published', label: '已发布' },
  { value: 'suspended', label: '已挂起' },
]
const rolloutOptions = [
  { value: 'immediate', label: '立即' },
  { value: 'canary', label: '灰度' },
  { value: 'batched', label: '批次' },
]

const strategyOptions = [
  { value: 'stability_first', label: '稳定优先' },
  { value: 'cost_first', label: '成本优先' },
  { value: 'latency_first', label: '延迟优先' },
  { value: 'balanced', label: '均衡策略' },
]

const strategyLabels: Record<string, string> = Object.fromEntries(
  strategyOptions.map((o) => [o.value, o.label]),
)

const scopeLabels: Record<string, string> = {
  plan: '计划策略',
  pool: '上游池策略',
  user: '用户策略',
}

const runKindLabels: Record<string, string> = {
  dry_run: '预检',
  apply: '发布',
  rollback: '回滚',
}

const statusLabels: Record<string, string> = {
  active: '生效',
  disabled: '已停用',
  draft: '草稿',
  failed: '失败',
  maintenance: '维护',
  partial: '部分成功',
  pending: '等待执行',
  published: '已发布',
  retired: '已退役',
  revoked: '已回收',
  succeeded: '成功',
  suspended: '已挂起',
}

const healthStateLabels: Record<string, string> = {
  degraded: '降级',
  healthy: '健康',
  quarantined: '隔离中',
  recovering: '恢复中',
  unhealthy: '异常',
  unknown: '未知',
}

const actionLabels: Record<string, string> = {
  create: '创建',
  deprovision: '移除',
  disable: '停用',
  enable: '启用',
  hold: '等待处理',
  noop: '无需操作',
  revoke: '回收',
  update: '更新',
}

const rolloutLabels: Record<string, string> = {
  batched: '分批发布',
  canary: '灰度发布',
  immediate: '立即发布',
}

const triggerLabels: Record<string, string> = {
  manual: '人工',
  auto: '自动切换',
  system: '系统',
}

const decisionStatusLabels: Record<string, string> = {
  proposed: '待人工确认',
  approved: '已批准待执行',
  rejected: '已拒绝',
  skipped: '已跳过',
  applying: '执行中',
  observing: '观察中',
  completed: '已完成',
  rolled_back: '已回滚',
  failed: '失败',
}

function strategyText(type?: RouteStrategyType) {
  return strategyLabels[type ?? ''] ?? type ?? '-'
}

function percent(value?: number) {
  if (typeof value !== 'number') return '-'
  return `${(value * 100).toFixed(1)}%`
}

function ms(value?: number) {
  if (typeof value !== 'number' || value <= 0) return '-'
  return `${Math.round(value)} ms`
}

function listText(values?: string[]) {
  return values?.filter(Boolean).join(', ') || '-'
}

function listFormValue(values?: string[]) {
  return values?.filter(Boolean).join(', ') || undefined
}

function splitList(value?: string) {
  return (value ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

type UpstreamChannelFormValues = Record<string, string | number | undefined>

// eslint-disable-next-line react-refresh/only-export-components
export function channelInputFromForm(
  values: UpstreamChannelFormValues,
  lockedOwnership?: GatewayAccountOwnership,
): UpstreamChannelInput {
  const selectedOwnership: GatewayAccountOwnership =
    values.account_ownership === 'owner_provided' ? 'owner_provided' : 'platform_managed'
  const probeCapability: QualityProbeCapability =
    values.probe_capability === 'text_stream' ? 'text_stream' : ''
  const probeEndpointPath: QualityProbeEndpointPath =
    probeCapability === 'text_stream'
      ? (String(values.probe_endpoint_path ?? '').trim() as QualityProbeEndpointPath)
      : ''

  return {
    pool_id: String(values.pool_id),
    source_id: String(values.source_id ?? '').trim(),
    account_ownership: lockedOwnership ?? selectedOwnership,
    display_name: String(values.display_name ?? ''),
    provider: values.provider as string | undefined,
    models: splitList(values.models_text as string | undefined),
    probe_capability: probeCapability,
    probe_endpoint_path: probeEndpointPath,
    groups: splitList(values.groups_text as string | undefined),
    credential_binding_id: values.credential_binding_id as string | undefined,
    proxy_binding_id: values.proxy_binding_id as string | undefined,
    priority: values.priority as number | undefined,
    weight: values.weight as number | undefined,
    cost_hint: values.cost_hint as number | undefined,
    status: values.status as UpstreamChannelStatus,
    inventory_state: values.inventory_state as UpstreamChannelInput['inventory_state'],
    labels: parseLabels(values.labels_text as string | undefined),
  }
}

function probeScopeText(channel: UpstreamChannel) {
  if (channel.probe_capability !== 'text_stream' || !channel.probe_endpoint_path) return '关闭'
  return `文本流 · ${channel.probe_endpoint_path}`
}

const weightFields: (keyof StrategyWeights)[] = ['success', 'ttft', 'duration', 'stability', 'cost']

function strategyFormValues(strategy: Partial<RouteStrategy>) {
  const hasCustomWeights = weightFields.some((key) => (strategy.weights?.[key] ?? 0) !== 0)
  return {
    ...strategy,
    threshold_min_samples: strategy.thresholds?.min_samples,
    threshold_target_success_rate: strategy.thresholds?.target_success_rate,
    threshold_floor_success_rate: strategy.thresholds?.floor_success_rate,
    threshold_max_ttft_p95_ms: strategy.thresholds?.max_ttft_p95_ms,
    threshold_max_duration_p95_ms: strategy.thresholds?.max_duration_p95_ms,
    threshold_consecutive_failure_limit: strategy.thresholds?.consecutive_failure_limit,
    threshold_eject_score: strategy.thresholds?.eject_score,
    weight_success: hasCustomWeights ? strategy.weights?.success : undefined,
    weight_ttft: hasCustomWeights ? strategy.weights?.ttft : undefined,
    weight_duration: hasCustomWeights ? strategy.weights?.duration : undefined,
    weight_stability: hasCustomWeights ? strategy.weights?.stability : undefined,
    weight_cost: hasCustomWeights ? strategy.weights?.cost : undefined,
  }
}

function StrategySettingsFields() {
  return (
    <>
      <ProFormText name="name" label="策略名称（可选）" placeholder="例如：生产环境稳定优先" />
      <ProFormSelect
        name="type"
        label="策略"
        options={strategyOptions}
        rules={[{ required: true }]}
      />
      <ProFormSwitch name="auto_apply" label="低风险自动执行" />
      <ProFormSwitch name="approval_required" label="强制人工审批" />
      <ProFormDigit name="cooldown_seconds" label="切换冷却（秒）" min={0} max={604800} />
      <ProFormDigit
        name="recovery_observation_seconds"
        label="恢复观察（秒）"
        min={0}
        max={604800}
      />
      <ProFormDigit name="max_auto_switches_per_hour" label="每小时上限" min={0} max={3600} />
      <ProFormGroup
        title="健康门槛"
        tooltip="留空时使用所选策略的内置默认值；成功率使用 0 到 1 的小数。"
        grid
      >
        <ProFormDigit
          name="threshold_min_samples"
          label="最少样本数"
          min={1}
          fieldProps={{ precision: 0 }}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_target_success_rate"
          label="目标成功率"
          min={0.0001}
          max={1}
          fieldProps={{ precision: 4, step: 0.01 }}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_floor_success_rate"
          label="成功率硬底线"
          min={0.0001}
          max={1}
          fieldProps={{ precision: 4, step: 0.01 }}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_max_ttft_p95_ms"
          label="首字延迟 p95 上限（ms）"
          min={1}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_max_duration_p95_ms"
          label="总耗时 p95 上限（ms）"
          min={1}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_consecutive_failure_limit"
          label="连续失败上限"
          min={1}
          fieldProps={{ precision: 0 }}
          colProps={{ xs: 24, md: 8 }}
        />
        <ProFormDigit
          name="threshold_eject_score"
          label="摘除分数线"
          min={1}
          max={100}
          fieldProps={{ precision: 0 }}
          colProps={{ xs: 24, md: 8 }}
        />
      </ProFormGroup>
      <ProFormGroup
        title="评分权重"
        tooltip="五项必须同时填写且合计为 1；全部留空则使用所选策略的内置权重。"
        grid
      >
        {[
          ['success', '成功率'],
          ['ttft', '首字延迟'],
          ['duration', '总耗时'],
          ['stability', '稳定性'],
          ['cost', '成本'],
        ].map(([key, label]) => (
          <ProFormDigit
            key={key}
            name={`weight_${key}`}
            label={label}
            min={0}
            max={1}
            fieldProps={{ precision: 4, step: 0.05 }}
            colProps={{ xs: 24, sm: 12, md: 8 }}
          />
        ))}
      </ProFormGroup>
    </>
  )
}

function statusTag(status?: string) {
  const color: Record<string, string> = {
    active: 'success',
    maintenance: 'processing',
    retired: 'default',
    draft: 'default',
    published: 'success',
    suspended: 'warning',
    pending: 'gold',
    disabled: 'warning',
    failed: 'error',
    revoked: 'default',
  }
  return (
    <Tag color={color[status ?? ''] ?? 'default'}>
      {statusLabels[status ?? ''] ?? status ?? '-'}
    </Tag>
  )
}

function healthTag(state?: string) {
  const color: Record<string, string> = {
    healthy: 'success',
    degraded: 'warning',
    unhealthy: 'error',
    quarantined: 'volcano',
    recovering: 'processing',
    unknown: 'default',
  }
  return (
    <Tag color={color[state ?? ''] ?? 'default'}>
      {healthStateLabels[state ?? ''] ?? state ?? '未知'}
    </Tag>
  )
}

function riskTag(risk?: string) {
  const color: Record<string, string> = { L0: 'default', L1: 'blue', L2: 'gold', L3: 'red' }
  return <Tag color={color[risk ?? ''] ?? 'default'}>{risk || '-'}</Tag>
}

function decisionStatusTag(status?: string) {
  const color: Record<string, string> = {
    proposed: 'gold',
    approved: 'cyan',
    rejected: 'default',
    skipped: 'default',
    applying: 'processing',
    observing: 'blue',
    completed: 'success',
    rolled_back: 'orange',
    failed: 'error',
  }
  return (
    <Tag color={color[status ?? ''] ?? 'default'}>
      {decisionStatusLabels[status ?? ''] ?? status ?? '-'}
    </Tag>
  )
}

function actionTag(type?: string) {
  const color: Record<string, string> = {
    create: 'green',
    enable: 'blue',
    disable: 'orange',
    revoke: 'volcano',
    update: 'purple',
    deprovision: 'red',
    hold: 'gold',
    noop: 'default',
  }
  return <Tag color={color[type ?? ''] ?? 'default'}>{actionLabels[type ?? ''] ?? type ?? '-'}</Tag>
}

function PoolsTab() {
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<UpstreamPool | null>(null)
  const { data, isLoading, refetch } = useUpstreamPools()
  const createPool = useCreateUpstreamPool()
  const updatePool = useUpdateUpstreamPool()

  const columns: ProColumns<UpstreamPool>[] = [
    { title: '名称', dataIndex: 'name', width: 180, ellipsis: true },
    { title: '服务商', dataIndex: 'provider', width: 120, render: (v) => v || '-' },
    {
      title: '模型',
      dataIndex: 'models',
      width: 220,
      ellipsis: true,
      render: (_, r) => listText(r.models),
    },
    { title: '区域', dataIndex: 'region', width: 120, render: (v) => v || '-' },
    { title: '状态', dataIndex: 'status', width: 96, render: (_, r) => statusTag(r.status) },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.updated_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 80,
      render: (_, r) => (
        <a
          onClick={() => {
            setEditing(r)
            setOpen(true)
          }}
        >
          编辑
        </a>
      ),
    },
  ]

  return (
    <>
      <ProTable<UpstreamPool>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          <Button
            key="new"
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setOpen(true)
            }}
          >
            新建上游池
          </Button>,
        ]}
        locale={{ emptyText: <EmptyTeach title="还没有上游池" /> }}
      />
      <ModalForm
        key={editing?.id ?? 'new-pool'}
        title={editing ? `编辑上游池 ${editing.name}` : '新建上游池'}
        open={open}
        onOpenChange={setOpen}
        modalProps={{ destroyOnHidden: true }}
        initialValues={
          editing
            ? {
                ...editing,
                models_text: listFormValue(editing.models),
                labels_text: formatLabels(editing.labels),
              }
            : { status: 'maintenance' }
        }
        onFinish={async (values) => {
          const v = values as Record<string, string>
          const body: UpstreamPoolInput = {
            name: v.name,
            provider: v.provider,
            models: splitList(v.models_text),
            region: v.region,
            status: v.status as UpstreamPoolStatus,
            description: v.description,
            labels: parseLabels(v.labels_text),
          }
          if (editing) await updatePool.mutateAsync({ id: editing.id, body })
          else await createPool.mutateAsync(body)
          return true
        }}
      >
        <ProFormText name="name" label="名称" rules={[{ required: true }]} />
        <ProFormSelect
          name="status"
          label="状态"
          options={poolStatusOptions}
          rules={[{ required: true }]}
        />
        <ProFormText name="provider" label="服务商" placeholder="Anthropic / OpenAI / Gemini" />
        <ProFormText
          name="models_text"
          label="模型"
          placeholder="claude-3-5-sonnet, claude-3-opus"
        />
        <ProFormText name="region" label="区域" placeholder="us-east / hk / jp" />
        <ProFormTextArea name="description" label="备注" fieldProps={{ rows: 3 }} />
        <ProFormTextArea
          name="labels_text"
          label="标签（JSON）"
          placeholder={'{"environment":"production"}'}
          fieldProps={{ rows: 5 }}
          rules={[{ validator: labelsFieldValidator }]}
        />
      </ModalForm>
    </>
  )
}

function ChannelsTab() {
  const [poolId, setPoolId] = useState<string | undefined>()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<UpstreamChannel | null>(null)
  const pools = useUpstreamPools()
  const { data, isLoading, refetch } = useUpstreamChannels(poolId)
  const createChannel = useCreateUpstreamChannel()
  const updateChannel = useUpdateUpstreamChannel()

  const poolName = (id: string) => pools.data?.find((p) => p.id === id)?.name ?? id
  const poolOptions = (pools.data ?? []).map((p) => ({ value: p.id, label: p.name }))

  const columns: ProColumns<UpstreamChannel>[] = [
    { title: '渠道', dataIndex: 'display_name', width: 160, ellipsis: true },
    {
      title: '上游池',
      dataIndex: 'pool_id',
      width: 160,
      ellipsis: true,
      render: (_, r) => poolName(r.pool_id),
    },
    {
      title: '上游来源 ID',
      dataIndex: 'source_id',
      width: 180,
      ellipsis: true,
      render: (value) => value || '-',
    },
    {
      title: '账号归属',
      dataIndex: 'account_ownership',
      width: 110,
      render: (_, r) => accountOwnershipLabels[r.account_ownership] ?? r.account_ownership,
    },
    {
      title: '探测作用域',
      dataIndex: 'probe_endpoint_path',
      width: 210,
      ellipsis: true,
      render: (_, r) => probeScopeText(r),
    },
    { title: '服务商', dataIndex: 'provider', width: 120, render: (v) => v || '-' },
    {
      title: '模型',
      dataIndex: 'models',
      width: 220,
      ellipsis: true,
      render: (_, r) => listText(r.models),
    },
    {
      title: '分组',
      dataIndex: 'groups',
      width: 160,
      ellipsis: true,
      render: (_, r) => listText(r.groups),
    },
    {
      title: '凭证绑定 ID',
      dataIndex: 'credential_binding_id',
      width: 190,
      ellipsis: true,
      render: (v) => v || '-',
    },
    {
      title: '代理绑定 ID',
      dataIndex: 'proxy_binding_id',
      width: 190,
      ellipsis: true,
      render: (v) => v || '-',
    },
    { title: '权重', dataIndex: 'weight', width: 80, render: (v) => v || '-' },
    { title: '状态', dataIndex: 'status', width: 96, render: (_, r) => statusTag(r.status) },
    {
      title: '远端账号',
      dataIndex: 'labels',
      width: 140,
      ellipsis: true,
      render: (_, r) => r.labels?.remote_id || '-',
    },
    {
      title: '操作',
      valueType: 'option',
      width: 80,
      render: (_, r) => (
        <a
          onClick={() => {
            setEditing(r)
            setOpen(true)
          }}
        >
          编辑
        </a>
      ),
    },
  ]

  const initial = editing
    ? {
        ...editing,
        probe_capability: editing.probe_capability === 'text_stream' ? 'text_stream' : 'disabled',
        models_text: listFormValue(editing.models),
        groups_text: listFormValue(editing.groups),
        labels_text: formatLabels(editing.labels),
      }
    : {
        status: 'maintenance',
        inventory_state: 'draft',
        pool_id: poolId,
        account_ownership: 'platform_managed',
        probe_capability: 'disabled',
      }

  return (
    <>
      <ProTable<UpstreamChannel>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          <ProFormSelect
            key="pool"
            noStyle
            fieldProps={{
              style: { minWidth: 220 },
              value: poolId,
              onChange: setPoolId,
              allowClear: true,
            }}
            options={poolOptions}
            placeholder="全部上游池"
          />,
          <Button
            key="new"
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setOpen(true)
            }}
          >
            新建渠道
          </Button>,
        ]}
        locale={{ emptyText: <EmptyTeach title="还没有渠道" /> }}
      />
      <ModalForm
        key={editing?.id ?? 'new-channel'}
        title={editing ? `编辑渠道 ${editing.display_name}` : '新建渠道'}
        open={open}
        onOpenChange={setOpen}
        modalProps={{ destroyOnHidden: true }}
        initialValues={initial}
        onFinish={async (values) => {
          const body = channelInputFromForm(
            values as UpstreamChannelFormValues,
            editing?.account_ownership,
          )
          if (editing) await updateChannel.mutateAsync({ id: editing.id, body })
          else await createChannel.mutateAsync(body)
          return true
        }}
      >
        <ProFormSelect
          name="pool_id"
          label="上游池"
          options={poolOptions}
          rules={[{ required: true }]}
        />
        <ProFormText
          name="source_id"
          label="上游来源 ID"
          tooltip="同一上游投入的多个 Key 使用同一个稳定来源 ID；调度切换只发生在不同来源之间。"
          placeholder="例如 upstream-a"
          rules={[{ required: true }, { whitespace: true, message: '请输入上游来源 ID' }]}
        />
        <ProFormSelect
          name="account_ownership"
          label="账号归属"
          options={accountOwnershipOptions}
          rules={[{ required: true }]}
          fieldProps={{ disabled: Boolean(editing) }}
          tooltip={editing ? '账号归属在渠道创建后不可修改。' : undefined}
        />
        <ProFormText name="display_name" label="渠道名" rules={[{ required: true }]} />
        <ProFormSelect
          name="status"
          label="状态"
          options={channelStatusOptions}
          rules={[{ required: true }]}
        />
        <ProFormText name="provider" label="服务商" />
        <ProFormText
          name="models_text"
          label="模型"
          placeholder="claude-3-5-sonnet, claude-3-opus"
        />
        <ProFormSelect
          name="probe_capability"
          label="主动探测"
          options={probeCapabilityOptions}
          rules={[{ required: true }]}
        />
        <ProFormDependency name={['probe_capability']}>
          {({ probe_capability }) =>
            probe_capability === 'text_stream' ? (
              <ProFormSelect
                name="probe_endpoint_path"
                label="探测接口路径"
                options={probeEndpointOptions}
                rules={[{ required: true, message: '请选择探测接口路径' }]}
                preserve={false}
              />
            ) : null
          }
        </ProFormDependency>
        <ProFormText name="groups_text" label="网关分组" placeholder="default, vip" />
        <ProFormText
          name="credential_binding_id"
          label="凭证绑定 ID"
          tooltip="连接器本地网关中的凭证或账号标识；发布创建动作时必填。"
          placeholder="例如：local-sub2api-account-2"
        />
        <ProFormText
          name="proxy_binding_id"
          label="代理绑定 ID"
          tooltip="连接器本地网关中的代理标识；需要独立出口的 OAuth 渠道填写。"
          placeholder="例如：local-cpa-primary"
        />
        <ProFormDigit name="priority" label="优先级" min={0} />
        <ProFormDigit name="weight" label="权重" min={0} />
        <ProFormDigit name="cost_hint" label="成本提示" min={0} />
        <ProFormTextArea
          name="labels_text"
          label="标签（JSON）"
          placeholder={'{"remote_id":"account-1","type":"oauth"}'}
          fieldProps={{ rows: 5 }}
          rules={[{ validator: labelsFieldValidator }]}
        />
      </ModalForm>
    </>
  )
}

function AutoSwitchPanel({ planId }: { planId?: string }) {
  const { message, modal } = App.useApp()
  const [strategyOpen, setStrategyOpen] = useState(false)
  const summary = useAutoSwitchSummary(planId)
  const runs = useReconcileRuns(planId)
  const strategies = useRouteStrategies(
    planId ? { scope: 'plan', plan_id: planId } : undefined,
    !!planId,
  )
  const evaluate = useEvaluateAutoSwitch()
  const observe = useObserveAutoSwitchDecision(planId)
  const approve = useApproveAutoSwitchDecision(planId)
  const reject = useRejectAutoSwitchDecision(planId)
  const execute = useExecuteAutoSwitchDecision(planId)
  const upsertStrategy = useUpsertRouteStrategy(planId)
  const deleteStrategy = useDeleteRouteStrategy(planId)

  const planStrategy = strategies.data?.find((s) => s.scope === 'plan' && s.plan_id === planId)
  const current: AutoSwitchSummary | undefined = summary.data

  const confirmApprove = (decision: AutoSwitchDecision) => {
    modal.confirm({
      title: '批准这份切换方案？',
      content: '批准只锁定当前预检动作，不会立即修改网关。批准后仍需单独点击“执行”。',
      okText: '批准',
      cancelText: '取消',
      onOk: () => approve.mutateAsync({ decisionId: decision.id }),
    })
  }

  const confirmReject = (decision: AutoSwitchDecision) => {
    modal.confirm({
      title: '拒绝这份切换方案？',
      content: '拒绝后该方案结束，不会修改网关；如故障仍持续，系统可基于新证据生成新方案。',
      okText: '确认拒绝',
      cancelText: '取消',
      okButtonProps: { danger: true },
      onOk: () => reject.mutateAsync({ decisionId: decision.id }),
    })
  }

  const confirmExecute = (decision: AutoSwitchDecision) => {
    modal.confirm({
      title: '执行已批准的切换方案？',
      content: '系统会先重新预检；只有动作类型和影响线路与批准时一致才会修改网关，否则会拒绝执行。',
      okText: '执行切换',
      cancelText: '取消',
      onOk: () => execute.mutateAsync({ decisionId: decision.id }),
    })
  }

  const healthColumns: ProColumns<AutoSwitchChannelHealth>[] = [
    {
      title: '渠道',
      dataIndex: 'display_name',
      width: 180,
      ellipsis: true,
      render: (_, r) => r.display_name || r.channel_id,
    },
    {
      title: '调度态',
      dataIndex: 'circuit_state',
      width: 120,
      render: (_, r) => {
        const state = schedulingStateView(r)
        return <Tag color={state.color}>{state.label}</Tag>
      },
    },
    {
      title: '绑定态',
      dataIndex: 'binding_state',
      width: 104,
      render: (_, r) => statusTag(r.binding_state || (r.live ? 'active' : undefined)),
    },
    { title: '样本', dataIndex: 'sample_count', width: 72 },
    { title: '模型', dataIndex: 'model', width: 120, ellipsis: true, render: (v) => v || '-' },
    {
      title: '上游错误率',
      dataIndex: 'upstream_error_rate',
      width: 112,
      render: (v) => percent(v as number),
    },
    { title: '首字 p95', dataIndex: 'ttft_p95', width: 100, render: (v) => ms(v as number) },
    { title: '总耗时 p95', dataIndex: 'duration_p95', width: 112, render: (v) => ms(v as number) },
    {
      title: '质量分（100）',
      dataIndex: 'quality_score',
      width: 140,
      render: (_, r) =>
        r.evidence_updated_at ? (
          <Progress
            percent={Math.round(r.quality_score ?? 0)}
            status={r.quality_below_threshold ? 'exception' : 'normal'}
            size="small"
            style={{ minWidth: 110 }}
          />
        ) : (
          <Tag>未知</Tag>
        ),
    },
    {
      title: '证据',
      width: 180,
      render: (_, r) =>
        r.evidence_updated_at ? (
          <Space size={4}>
            <Tag color={r.evidence_fresh ? 'success' : 'warning'}>
              {r.evidence_fresh ? '新鲜' : '已过期'}
            </Tag>
            <span>{Math.round((r.evidence_confidence ?? 0) * 100)}%</span>
            <RelativeTime value={r.evidence_updated_at} />
          </Space>
        ) : (
          <Tag>无证据</Tag>
        ),
    },
    {
      title: '扣分构成',
      width: 330,
      render: (_, r) => penaltyBreakdownText(r),
    },
    {
      title: 'Circuit',
      dataIndex: 'circuit_state',
      width: 112,
      render: (_, r) => {
        const state = circuitStateView(r.circuit_state)
        return <Tag color={state.color}>{state.label}</Tag>
      },
    },
    {
      title: '故障影响',
      width: 240,
      render: (_, r) => failureScopeText(r),
    },
    {
      title: '恢复进度',
      dataIndex: 'consecutive_probe_successes',
      width: 108,
      render: (_, r) => recoveryProgressText(r),
    },
    {
      title: '回归观察截止',
      dataIndex: 'recovery_observe_after',
      width: 132,
      render: (_, r) => <RelativeTime value={r.recovery_observe_after} />,
    },
    {
      title: '下次探测',
      dataIndex: 'probe_after',
      width: 120,
      render: (_, r) => <RelativeTime value={r.probe_after} />,
    },
    {
      title: '上次探测',
      dataIndex: 'last_probe_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.last_probe_at} />,
    },
    {
      title: 'Circuit 最近分',
      dataIndex: 'last_score',
      width: 116,
      render: (value) => (typeof value === 'number' ? value.toFixed(1) : '-'),
    },
    {
      title: 'Circuit 原因',
      dataIndex: 'last_reason',
      width: 220,
      ellipsis: true,
      render: (_, r) => circuitReasonText(r),
    },
  ]

  const decisionColumns: ProColumns<AutoSwitchDecision>[] = [
    {
      title: '状态',
      dataIndex: 'status',
      width: 120,
      render: (_, r) => decisionStatusTag(r.status),
    },
    {
      title: '策略',
      dataIndex: 'strategy',
      width: 120,
      render: (v) => strategyText(v as RouteStrategyType),
    },
    { title: '风险', dataIndex: 'risk_level', width: 80, render: (_, r) => riskTag(r.risk_level) },
    {
      title: '切换路径',
      width: 220,
      render: (_, r) => `${r.from_channel_id || '-'} → ${r.to_channel_id || '-'}`,
      ellipsis: true,
    },
    {
      title: '原因',
      dataIndex: 'trigger_reason',
      width: 220,
      ellipsis: true,
      render: (v) => friendlyInlineError(v as string) || '-',
    },
    {
      title: '影响范围',
      width: 210,
      render: (_, r) =>
        decisionImpactText(
          r,
          current?.channels?.find((channel) => channel.channel_id === r.from_channel_id),
        ),
    },
    {
      title: '执行说明',
      width: 260,
      ellipsis: true,
      render: (_, r) => friendlyInlineError(decisionDetailText(r)),
    },
    {
      title: '预检',
      dataIndex: 'dry_run_result',
      width: 100,
      render: (_, r) => `${r.dry_run_result?.actions?.length ?? 0} 个动作`,
    },
    {
      title: '创建',
      dataIndex: 'created_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 176,
      render: (_, r) => {
        if (r.status === 'proposed') {
          return (
            <Space size={4}>
              <Button
                size="small"
                type="primary"
                loading={approve.isPending && approve.variables?.decisionId === r.id}
                disabled={reject.isPending}
                onClick={() => confirmApprove(r)}
              >
                批准
              </Button>
              <Button
                size="small"
                danger
                loading={reject.isPending && reject.variables?.decisionId === r.id}
                disabled={approve.isPending}
                onClick={() => confirmReject(r)}
              >
                拒绝
              </Button>
            </Space>
          )
        }
        if (r.status === 'approved') {
          return (
            <Button
              size="small"
              type="primary"
              loading={execute.isPending && execute.variables?.decisionId === r.id}
              onClick={() => confirmExecute(r)}
            >
              执行
            </Button>
          )
        }
        if (r.status === 'observing') {
          return (
            <Button size="small" icon={<EyeOutlined />} onClick={() => observe.mutate(r.id)}>
              观察
            </Button>
          )
        }
        return null
      },
    },
  ]

  const runColumns: ProColumns<ReconcileRun>[] = [
    {
      title: '类型',
      dataIndex: 'kind',
      width: 100,
      render: (v) => runKindLabels[String(v)] ?? String(v),
    },
    {
      title: '触发',
      dataIndex: 'trigger',
      width: 100,
      render: (v) => triggerLabels[String(v)] ?? String(v),
    },
    { title: '结果', dataIndex: 'status', width: 96, render: (_, r) => statusTag(r.status) },
    { title: '动作数', dataIndex: 'actions', width: 88, render: (_, r) => r.actions?.length ?? 0 },
    {
      title: '错误',
      dataIndex: 'error',
      width: 220,
      ellipsis: true,
      render: (v) => friendlyInlineError(v as string) || '-',
    },
    {
      title: '完成时间',
      dataIndex: 'finished_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.finished_at} />,
    },
  ]

  const strategyInitial = strategyFormValues(
    planStrategy ?? {
      scope: 'plan',
      plan_id: planId,
      type: current?.strategy ?? 'stability_first',
      auto_apply: current?.auto_apply ?? true,
      approval_required: false,
      cooldown_seconds: 600,
      recovery_observation_seconds: 900,
      max_auto_switches_per_hour: 3,
    },
  )

  return (
    <ProCard
      title="自动切换运营"
      bordered
      style={{ marginBottom: 16 }}
      extra={
        planId ? (
          <Space>
            <Button icon={<SettingOutlined />} onClick={() => setStrategyOpen(true)}>
              策略设置
            </Button>
            {planStrategy?.id ? (
              <Popconfirm
                title="恢复继承的自动切换策略？"
                description="当前计划的自定义策略将被删除。"
                okText="恢复默认"
                cancelText="取消"
                okButtonProps={{ danger: true, loading: deleteStrategy.isPending }}
                onConfirm={() => deleteStrategy.mutate(planStrategy.id!)}
              >
                <Button danger icon={<DeleteOutlined />} loading={deleteStrategy.isPending}>
                  恢复默认
                </Button>
              </Popconfirm>
            ) : null}
            <Button
              type="primary"
              icon={<ReloadOutlined />}
              loading={evaluate.isPending}
              onClick={() => evaluate.mutate(planId)}
            >
              立即评估
            </Button>
          </Space>
        ) : null
      }
    >
      {!planId ? (
        <EmptyTeach title="选择一个发布计划查看自动切换状态" />
      ) : summary.isLoading ? (
        <ProCard loading />
      ) : (
        <>
          {summary.error && (
            <Alert
              type="warning"
              showIcon
              message="自动切换摘要暂不可用"
              style={{ marginBottom: 12 }}
            />
          )}
          {current?.active_decision && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 12 }}
              message={`当前处于${decisionStatusLabels[current.active_decision.status] ?? current.active_decision.status}：${friendlyInlineError(current.active_decision.trigger_reason || current.active_decision.risk_reason) || '等待观察结果'}`}
            />
          )}
          <Descriptions
            size="small"
            column={4}
            bordered
            style={{ marginBottom: 16 }}
            items={[
              {
                label: '当前策略',
                children: <Tag color="blue">{strategyText(current?.strategy)}</Tag>,
              },
              {
                label: '策略来源',
                children: scopeLabels[current?.strategy_source ?? ''] ?? '内置默认',
              },
              {
                label: '自动执行',
                children: current?.auto_apply ? <Tag color="success">开启</Tag> : <Tag>仅建议</Tag>,
              },
              {
                label: '最近决策',
                children: `${current?.recent_decisions?.length ?? 0} 条`,
              },
            ]}
          />
          <Typography.Text type="secondary">
            调度质量从 100 分开始，仅扣上游错误（最多 55）、首字耗时（最多 25）和总耗时（最多
            20）；达到摘除线的软故障按稳定下游批次扩大，认证和余额硬故障仅作用于有证据的当前下游。隔离渠道只通过主动探测恢复。
          </Typography.Text>
          <ProTable<AutoSwitchChannelHealth>
            rowKey="channel_id"
            size="small"
            style={{ marginTop: 12 }}
            search={false}
            options={false}
            pagination={false}
            scroll={{ x: 'max-content' }}
            columns={healthColumns}
            dataSource={current?.channels ?? []}
            locale={{ emptyText: <EmptyTeach title="还没有渠道健康快照" /> }}
          />
          <ProTable<AutoSwitchDecision>
            rowKey="id"
            size="small"
            headerTitle="最近自动切换决策"
            search={false}
            options={false}
            pagination={false}
            scroll={{ x: 'max-content' }}
            columns={decisionColumns}
            dataSource={current?.recent_decisions ?? []}
            locale={{ emptyText: <EmptyTeach title="还没有自动切换决策" /> }}
          />
          <ProTable<ReconcileRun>
            rowKey="id"
            size="small"
            headerTitle="发布执行历史"
            loading={runs.isLoading}
            search={false}
            options={{ reload: () => runs.refetch() }}
            pagination={false}
            scroll={{ x: 'max-content' }}
            columns={runColumns}
            dataSource={runs.data ?? []}
            locale={{ emptyText: <EmptyTeach title="还没有预检、发布或回滚记录" /> }}
          />
        </>
      )}

      <ModalForm
        key={`${planId ?? 'none'}-${planStrategy?.id ?? 'default'}`}
        title="自动切换策略"
        open={strategyOpen}
        onOpenChange={setStrategyOpen}
        modalProps={{ destroyOnHidden: true }}
        initialValues={strategyInitial}
        onFinish={async (values) => {
          const v = values as Partial<RouteStrategy> & Record<string, unknown>
          const validationError = strategyValidationError(v)
          if (validationError) {
            message.error(validationError)
            return false
          }
          await upsertStrategy.mutateAsync({
            ...strategyFromForm(
              { ...v, plan_id: planId, type: v.type ?? current?.strategy },
              'plan',
            ),
          })
          return true
        }}
      >
        <StrategySettingsFields />
      </ModalForm>
    </ProCard>
  )
}

function PlansTab({ requestedPlanId }: { requestedPlanId?: string }) {
  const [userId, setUserId] = useState<number | undefined>()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<RoutePlan | null>(null)
  const [manualSelectedPlanId, setManualSelectedPlanId] = useState<string | undefined>(
    requestedPlanId,
  )
  const [lastReconcile, setLastReconcile] = useState<ReconcilePlan | null>(null)
  const [previewByPlan, setPreviewByPlan] = useState<Record<string, ReconcilePlan>>({})

  const instances = useInstances()
  const pools = useUpstreamPools()
  const plans = useRoutePlans(userId)
  const selectedPlanId = selectedPlanFromLocation(
    manualSelectedPlanId,
    (plans.data ?? []).map((plan) => plan.id),
  )
  const bindings = usePublishedBindings(selectedPlanId)
  const createPlan = useCreateRoutePlan()
  const updatePlan = useUpdateRoutePlan()
  const reconcile = useReconcileRoutePlan()
  const rollback = useRollbackRoutePlan()

  const poolName = (id: string) => pools.data?.find((p) => p.id === id)?.name ?? id
  const instanceName = (id: string) => instances.data?.find((i) => i.id === id)?.name ?? id
  const instanceOptions = (instances.data ?? []).map((i) => ({
    value: i.id,
    label: `${i.name} (${i.kind})`,
  }))
  const poolOptions = (pools.data ?? []).map((p) => ({ value: p.id, label: p.name }))

  const runReconcile = async (id: string, dryRun: boolean) => {
    if (!dryRun && !previewByPlan[id]) return
    setManualSelectedPlanId(id)
    const result = await reconcile.mutateAsync({ id, dryRun })
    setLastReconcile(result)
    setPreviewByPlan((current) => {
      if (dryRun) return { ...current, [id]: result }
      const next = { ...current }
      delete next[id]
      return next
    })
  }

  const columns: ProColumns<RoutePlan>[] = [
    { title: '计划', dataIndex: 'id', width: 150, ellipsis: true },
    {
      title: '实例',
      dataIndex: 'instance_id',
      width: 160,
      ellipsis: true,
      render: (_, r) => instanceName(r.instance_id),
    },
    {
      title: '上游池',
      dataIndex: 'pool_id',
      width: 160,
      ellipsis: true,
      render: (_, r) => poolName(r.pool_id),
    },
    { title: '状态', dataIndex: 'status', width: 96, render: (_, r) => statusTag(r.status) },
    { title: '策略', dataIndex: 'tier', width: 100, render: (v) => v || '-' },
    {
      title: '发布',
      dataIndex: 'rollout',
      width: 150,
      render: (_, r) => (
        <Space size={4}>
          <Tag>{rolloutLabels[r.rollout || 'immediate'] ?? r.rollout}</Tag>
          {r.rollout === 'canary' && <Tag>灰度 {r.rollout_canary_count || 1}</Tag>}
          {r.rollout === 'batched' && <Tag>每批 {r.rollout_batch_size || 1}</Tag>}
        </Space>
      ),
    },
    { title: '上限', dataIndex: 'max_channels', width: 80, render: (v) => v || '-' },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.updated_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 300,
      render: (_, r) => {
        const preview = previewByPlan[r.id]
        const applyBlocked = reconcile.isPending || !preview
        const applyTitle = !preview ? '请先执行预检，确认本次完整动作集' : undefined
        return (
          <Space className="e2m-table-actions">
            <a
              onClick={() => {
                setEditing(r)
                setOpen(true)
              }}
            >
              编辑
            </a>
            <Button
              size="small"
              icon={<ReloadOutlined />}
              loading={reconcile.isPending && selectedPlanId === r.id}
              onClick={() => runReconcile(r.id, true)}
            >
              预检
            </Button>
            <Button
              size="small"
              type="primary"
              icon={<SendOutlined />}
              disabled={applyBlocked}
              title={applyTitle}
              onClick={() => runReconcile(r.id, false)}
            >
              应用
            </Button>
            <Popconfirm
              title="回滚这个发布计划？"
              onConfirm={async () => {
                setLastReconcile(await rollback.mutateAsync(r.id))
                setManualSelectedPlanId(r.id)
              }}
            >
              <Button size="small" danger icon={<RollbackOutlined />}>
                回滚
              </Button>
            </Popconfirm>
          </Space>
        )
      },
    },
  ]

  const bindingColumns: ProColumns<PublishedBinding>[] = [
    { title: '渠道', dataIndex: 'channel_id', width: 180, ellipsis: true },
    {
      title: '远端账号',
      dataIndex: 'remote_id',
      width: 140,
      ellipsis: true,
      render: (v) => v || '-',
    },
    { title: '状态', dataIndex: 'state', width: 96, render: (_, r) => statusTag(r.state) },
    {
      title: '可调用验活',
      dataIndex: 'verification_status',
      width: 160,
      render: (_, record) => {
        const status = record.verification_status || 'published_pending'
        const labels = {
          published_pending: '已发布，待验证',
          awaiting_first_request: '等待首次真实请求',
          probe_verified: '主动探测通过',
          passive_verified: '真实请求通过',
          verification_failed: '验活失败',
        } as const
        const color =
          status === 'probe_verified' || status === 'passive_verified'
            ? 'success'
            : status === 'verification_failed'
              ? 'error'
              : 'warning'
        return (
          <Space direction="vertical" size={0}>
            <Tag color={color}>{labels[status]}</Tag>
            {record.verified_at ? <RelativeTime value={record.verified_at} /> : null}
          </Space>
        )
      },
    },
    {
      title: '错误',
      dataIndex: 'last_error',
      width: 180,
      ellipsis: true,
      render: (v) => friendlyInlineError(v as string) || '-',
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.updated_at} />,
    },
  ]

  const actionColumns: ProColumns<ReconcileAction>[] = [
    { title: '动作', dataIndex: 'type', width: 96, render: (_, r) => actionTag(r.type) },
    { title: '渠道', dataIndex: 'channel_id', width: 180, ellipsis: true },
    {
      title: '远端账号',
      dataIndex: 'remote_id',
      width: 160,
      ellipsis: true,
      render: (v) => v || '-',
    },
    {
      title: '详情',
      dataIndex: 'detail',
      width: 220,
      ellipsis: true,
      render: (v) => (v ? reconcileDetailLabel(String(v)) : '-'),
    },
  ]

  const initial = editing
    ? {
        ...editing,
        rollout: editing.rollout || 'immediate',
        labels_text: formatLabels(editing.labels),
      }
    : { status: 'draft', rollout: 'canary', rollout_canary_count: 1, rollout_batch_size: 1 }

  return (
    <>
      <ProTable<RoutePlan>
        rowKey="id"
        loading={plans.isLoading}
        dataSource={plans.data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => plans.refetch() }}
        rowSelection={{
          type: 'radio',
          selectedRowKeys: selectedPlanId ? [selectedPlanId] : [],
          onChange: (keys) => setManualSelectedPlanId(keys[0] as string),
        }}
        toolBarRender={() => [
          <UserSelect key="user" value={userId} onChange={setUserId} placeholder="全部账号" />,
          <Button
            key="new"
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setOpen(true)
            }}
          >
            新建发布计划
          </Button>,
        ]}
        locale={{ emptyText: <EmptyTeach title="还没有发布计划" /> }}
      />

      {lastReconcile && (
        <ProCard
          title={lastReconcile.dry_run ? '预检结果' : '执行结果'}
          style={{ marginBottom: 16 }}
          bordered
        >
          <ProTable<ReconcileAction>
            rowKey={(r) => `${r.type}-${r.channel_id}-${r.remote_id ?? ''}`}
            size="small"
            search={false}
            options={false}
            pagination={false}
            scroll={{ x: 'max-content' }}
            columns={actionColumns}
            dataSource={lastReconcile.actions}
          />
        </ProCard>
      )}

      <AutoSwitchPanel planId={selectedPlanId} />

      <ProCard title="发布与可调用状态" bordered>
        <ProTable<PublishedBinding>
          rowKey="id"
          size="small"
          loading={bindings.isLoading}
          search={false}
          options={{ reload: () => bindings.refetch() }}
          pagination={false}
          scroll={{ x: 'max-content' }}
          columns={bindingColumns}
          dataSource={bindings.data ?? []}
          locale={{ emptyText: <EmptyTeach title="选择一个发布计划查看绑定" /> }}
        />
      </ProCard>

      <ModalForm
        key={editing?.id ?? 'new-plan'}
        title={editing ? `编辑发布计划 ${editing.id}` : '新建发布计划'}
        open={open}
        onOpenChange={setOpen}
        modalProps={{ destroyOnHidden: true }}
        initialValues={initial}
        onFinish={async (values) => {
          const v = values as Record<string, string | number | undefined>
          const body: RoutePlanInput = {
            instance_id: String(v.instance_id ?? editing?.instance_id ?? ''),
            pool_id: String(v.pool_id ?? editing?.pool_id ?? ''),
            tier: v.tier as string | undefined,
            status: v.status as RoutePlanStatus,
            max_channels: v.max_channels as number | undefined,
            rollout: v.rollout as RolloutMode,
            rollout_batch_size: v.rollout_batch_size as number | undefined,
            rollout_canary_count: v.rollout_canary_count as number | undefined,
            labels: parseLabels(v.labels_text as string | undefined),
          }
          if (editing) {
            await updatePlan.mutateAsync({ id: editing.id, body })
            setPreviewByPlan((current) => {
              const next = { ...current }
              delete next[editing.id]
              return next
            })
          } else await createPlan.mutateAsync(body)
          return true
        }}
      >
        <ProFormSelect
          name="instance_id"
          label="目标实例"
          options={instanceOptions}
          rules={[{ required: true }]}
          disabled={!!editing}
        />
        <ProFormSelect
          name="pool_id"
          label="上游池"
          options={poolOptions}
          rules={[{ required: true }]}
          disabled={!!editing}
        />
        <ProFormSelect
          name="status"
          label="状态"
          options={planStatusOptions.map((option) => ({
            ...option,
            disabled: option.value === 'published' && editing?.status !== 'published',
          }))}
          rules={[{ required: true }]}
        />
        <ProFormText name="tier" label="策略" placeholder="stability / cost / performance" />
        <ProFormDigit name="max_channels" label="渠道上限" min={0} />
        <ProFormSelect
          name="rollout"
          label="发布模式"
          options={rolloutOptions}
          rules={[{ required: true }]}
        />
        <ProFormDigit name="rollout_canary_count" label="灰度数量" min={0} />
        <ProFormDigit name="rollout_batch_size" label="批次大小" min={0} />
        <ProFormTextArea
          name="labels_text"
          label="标签（JSON）"
          placeholder={'{"environment":"production"}'}
          fieldProps={{ rows: 5 }}
          rules={[{ validator: labelsFieldValidator }]}
        />
      </ModalForm>
    </>
  )
}

type ManagedStrategyScope = Exclude<StrategyScope, 'plan'>

function StrategiesTab() {
  const { message } = App.useApp()
  const [scope, setScope] = useState<ManagedStrategyScope | undefined>()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<RouteStrategy | null>(null)
  const strategies = useRouteStrategies(scope ? { scope } : undefined)
  const pools = useUpstreamPools()
  const users = useUsers()
  const upsert = useUpsertRouteStrategy()
  const remove = useDeleteRouteStrategy()

  const poolName = (id?: string) => pools.data?.find((pool) => pool.id === id)?.name ?? id ?? '-'
  const userName = (id?: number) => {
    const user = users.data?.find((item) => item.id === id)
    return user?.display_name || user?.email || (id ? String(id) : '-')
  }
  const targetText = (strategy: RouteStrategy) => {
    if (strategy.scope === 'plan') return strategy.plan_id || '-'
    if (strategy.scope === 'pool') return poolName(strategy.pool_id)
    return userName(strategy.user_id)
  }

  const openCreate = () => {
    setEditing(null)
    setOpen(true)
  }
  const openEdit = (strategy: RouteStrategy) => {
    setEditing(strategy)
    setOpen(true)
  }

  const columns: ProColumns<RouteStrategy>[] = [
    {
      title: '作用域',
      dataIndex: 'scope',
      width: 110,
      render: (_, record) => <Tag>{scopeLabels[record.scope ?? ''] ?? record.scope}</Tag>,
    },
    {
      title: '目标',
      width: 200,
      ellipsis: true,
      render: (_, record) => targetText(record),
    },
    {
      title: '名称',
      dataIndex: 'name',
      width: 180,
      ellipsis: true,
      render: (value) => value || '-',
    },
    {
      title: '策略',
      dataIndex: 'type',
      width: 120,
      render: (value) => strategyText(value as RouteStrategyType),
    },
    {
      title: '执行模式',
      dataIndex: 'auto_apply',
      width: 110,
      render: (_, record) =>
        record.approval_required ? (
          <Tag color="gold">强制审批</Tag>
        ) : record.auto_apply ? (
          <Tag color="success">自动执行</Tag>
        ) : (
          <Tag>仅建议</Tag>
        ),
    },
    {
      title: '冷却',
      dataIndex: 'cooldown_seconds',
      width: 100,
      render: (value) => (typeof value === 'number' ? `${value} 秒` : '-'),
    },
    {
      title: '更新',
      dataIndex: 'updated_at',
      width: 120,
      render: (_, record) => <RelativeTime value={record.updated_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 160,
      render: (_, record) => [
        <a key="edit" onClick={() => openEdit(record)}>
          编辑
        </a>,
        <Popconfirm
          key="delete"
          title={`删除${scopeLabels[record.scope ?? ''] ?? ''}？`}
          description="删除后，受影响的发布计划将回退到下一层策略或内置默认值。"
          okText="删除"
          cancelText="取消"
          okButtonProps={{ danger: true, loading: remove.isPending }}
          onConfirm={() => record.id && remove.mutate(record.id)}
        >
          <a style={{ color: '#ff4d4f' }}>删除</a>
        </Popconfirm>,
      ],
    },
  ]

  const initialValues = strategyFormValues(
    editing ?? {
      scope: scope ?? 'user',
      type: 'stability_first',
      auto_apply: false,
      approval_required: false,
      cooldown_seconds: 600,
      recovery_observation_seconds: 900,
      max_auto_switches_per_hour: 3,
    },
  )

  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="这里维护跨计划的策略默认值。生效优先级为计划 > 上游池 > 用户 > 内置默认；计划级覆盖仍在“发布计划”页签中维护。"
      />
      <ProTable<RouteStrategy>
        rowKey="id"
        loading={strategies.isLoading}
        dataSource={(strategies.data ?? []).filter((strategy) => strategy.scope !== 'plan')}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => strategies.refetch() }}
        toolBarRender={() => [
          <ProFormSelect
            key="scope"
            noStyle
            fieldProps={{
              style: { minWidth: 180 },
              value: scope,
              onChange: setScope,
              allowClear: true,
            }}
            options={[
              { value: 'user', label: '用户策略' },
              { value: 'pool', label: '上游池策略' },
            ]}
            placeholder="全部作用域"
          />,
          <Button key="new" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建策略
          </Button>,
        ]}
        locale={{ emptyText: <EmptyTeach title="还没有跨计划路由策略" /> }}
      />

      <ModalForm
        key={editing?.id ?? `new-${scope ?? 'strategy'}`}
        title={editing ? `编辑策略 ${editing.name || targetText(editing)}` : '新建路由策略'}
        open={open}
        onOpenChange={(next) => {
          setOpen(next)
          if (!next) setEditing(null)
        }}
        modalProps={{ destroyOnHidden: true }}
        initialValues={initialValues}
        submitter={{ submitButtonProps: { loading: upsert.isPending } }}
        onFinish={async (values) => {
          const v = values as Partial<RouteStrategy> & Record<string, unknown>
          const targetScope = (editing?.scope ?? v.scope) as ManagedStrategyScope
          const validationError = strategyValidationError(v)
          if (validationError) {
            message.error(validationError)
            return false
          }
          await upsert.mutateAsync(strategyFromForm(v, targetScope))
          return true
        }}
      >
        <ProFormSelect
          name="scope"
          label="作用域"
          rules={[{ required: true }]}
          disabled={!!editing}
          options={[
            { value: 'user', label: '用户：该用户所有发布计划的默认策略' },
            { value: 'pool', label: '上游池：使用该池的所有发布计划' },
          ]}
        />
        <ProFormDependency name={['scope']}>
          {({ scope: selectedScope }) =>
            selectedScope === 'pool' ? (
              <ProFormSelect
                name="pool_id"
                label="上游池"
                rules={[{ required: true }]}
                disabled={!!editing}
                options={(pools.data ?? []).map((pool) => ({ value: pool.id, label: pool.name }))}
                fieldProps={{ showSearch: true, optionFilterProp: 'label' }}
              />
            ) : (
              <ProFormSelect
                name="user_id"
                label="用户"
                rules={[{ required: true }]}
                disabled={!!editing}
                options={(users.data ?? []).map((user) => ({
                  value: user.id,
                  label: user.display_name || user.email,
                }))}
                fieldProps={{ showSearch: true, optionFilterProp: 'label' }}
              />
            )
          }
        </ProFormDependency>
        <StrategySettingsFields />
      </ModalForm>
    </>
  )
}

export default function Upstream() {
  const [searchParams, setSearchParams] = useSearchParams()
  const location = upstreamLocationFromSearch(`?${searchParams.toString()}`)
  const tabs = useMemo(
    () => [
      { key: 'plans', tab: '发布计划' },
      { key: 'channels', tab: '渠道' },
      { key: 'pools', tab: '上游池' },
      { key: 'strategies', tab: '路由策略' },
    ],
    [],
  )
  const changeTab = (nextTab: string) => {
    const next = new URLSearchParams(searchParams)
    next.set('tab', nextTab)
    if (nextTab !== 'plans') next.delete('plan_id')
    setSearchParams(next, { replace: true })
  }
  return (
    <PageContainer
      title="上游编排"
      tabList={tabs}
      tabActiveKey={location.tab}
      onTabChange={changeTab}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="渠道只维护 Connector 本地凭证与代理绑定 ID；Connector v2 可对平台托管账号执行创建、更新和延迟删除，用户自有账号仅允许更新。"
      />
      {location.tab === 'plans' ? (
        <PlansTab key={location.planId ?? 'plans'} requestedPlanId={location.planId} />
      ) : location.tab === 'channels' ? (
        <ChannelsTab />
      ) : location.tab === 'pools' ? (
        <PoolsTab />
      ) : (
        <StrategiesTab />
      )}
    </PageContainer>
  )
}
