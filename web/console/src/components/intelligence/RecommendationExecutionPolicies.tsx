import { useMemo, useState } from 'react'
import {
  Alert,
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
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import type { RoutePlan, UpstreamPool } from '../../api/types'
import type {
  RecommendationExecutionPolicy,
  RecommendationExecutionPolicyInput,
  RecommendationExecutionScope,
} from '../../api/recommendationLab'
import {
  useRecommendationExecutionPolicies,
  useSaveRecommendationExecutionPolicy,
} from '../../api/recommendationLabHooks'
import { friendlyErrorMessage } from '../../api/errors'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

interface PolicyFormValues {
  scope: RecommendationExecutionScope
  target_id: string
  enabled: boolean
  kill_switch: boolean
  daily_execution_cap: number
  cooldown_seconds: number
  minimum_savings: string
}

export function RecommendationExecutionPolicies({
  userId,
  plans,
  pools,
}: {
  userId?: number
  plans: RoutePlan[]
  pools: UpstreamPool[]
}) {
  useLocaleVersion()
  const policies = useRecommendationExecutionPolicies(userId)
  const save = useSaveRecommendationExecutionPolicy()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<RecommendationExecutionPolicy>()
  const [form] = Form.useForm<PolicyFormValues>()
  const scope = Form.useWatch('scope', form)

  const planById = useMemo(() => new Map(plans.map((plan) => [plan.id, plan])), [plans])
  const poolById = useMemo(() => new Map(pools.map((pool) => [pool.id, pool])), [pools])

  const targetOptions =
    scope === 'pool'
      ? pools.map((pool) => ({ value: pool.id, label: pool.name }))
      : plans.map((plan) => ({
          value: plan.id,
          label: `${plan.id} · ${t(`upstreamIntelligence.policies.planStatuses.${plan.status}`, plan.status)}`,
        }))

  const showCreate = () => {
    setEditing(undefined)
    form.setFieldsValue({
      scope: 'plan',
      target_id: undefined,
      enabled: false,
      kill_switch: false,
      daily_execution_cap: 1,
      cooldown_seconds: 3600,
      minimum_savings: '0.05',
    })
    setOpen(true)
  }

  const showEdit = (policy: RecommendationExecutionPolicy) => {
    setEditing(policy)
    form.setFieldsValue({
      scope: policy.scope,
      target_id: policy.scope === 'plan' ? policy.plan_id : policy.pool_id,
      enabled: policy.enabled,
      kill_switch: policy.kill_switch,
      daily_execution_cap: policy.daily_execution_cap,
      cooldown_seconds: policy.cooldown_seconds,
      minimum_savings: policy.minimum_savings,
    })
    setOpen(true)
  }

  const columns: ColumnsType<RecommendationExecutionPolicy> = [
    {
      title: t('upstreamIntelligence.policies.scope'),
      dataIndex: 'scope',
      render: (value: RecommendationExecutionScope) => (
        <Tag color={value === 'plan' ? 'blue' : 'purple'}>
          {value === 'plan'
            ? t('upstreamIntelligence.policies.scopePlan')
            : t('upstreamIntelligence.policies.scopePool')}
        </Tag>
      ),
    },
    {
      title: t('upstreamIntelligence.policies.target'),
      render: (_, policy) => {
        if (policy.scope === 'plan') {
          return policy.plan_id ? (planById.get(policy.plan_id)?.id ?? policy.plan_id) : '—'
        }
        return policy.pool_id ? (poolById.get(policy.pool_id)?.name ?? policy.pool_id) : '—'
      },
    },
    {
      title: t('upstreamIntelligence.policies.authorization'),
      render: (_, policy) => (
        <Space>
          <Tag color={policy.enabled ? 'green' : 'default'}>
            {policy.enabled
              ? t('upstreamIntelligence.policies.authorized')
              : t('upstreamIntelligence.policies.unauthorized')}
          </Tag>
          {policy.kill_switch ? (
            <Tag color="red">{t('upstreamIntelligence.policies.killSwitch')}</Tag>
          ) : null}
        </Space>
      ),
    },
    { title: t('upstreamIntelligence.policies.dailyCap'), dataIndex: 'daily_execution_cap' },
    {
      title: t('upstreamIntelligence.policies.cooldown'),
      dataIndex: 'cooldown_seconds',
      render: (value: number) => t('upstreamIntelligence.policies.seconds', undefined, { value }),
    },
    {
      title: t('upstreamIntelligence.policies.minimumSavings'),
      dataIndex: 'minimum_savings',
      render: (value: string) => formatSavings(value),
    },
    {
      title: t('upstreamIntelligence.policies.version'),
      dataIndex: 'version',
      render: (value: number) => `v${value}`,
    },
    {
      title: t('upstreamIntelligence.common.actions'),
      render: (_, policy) => (
        <Button type="link" onClick={() => showEdit(policy)}>
          {t('upstreamIntelligence.policies.edit')}
        </Button>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Alert
        type="warning"
        showIcon
        message={t('upstreamIntelligence.policies.noticeTitle')}
        description={t('upstreamIntelligence.policies.noticeDescription')}
      />
      <Space wrap>
        <Button type="primary" icon={<PlusOutlined />} disabled={!userId} onClick={showCreate}>
          {t('upstreamIntelligence.policies.create')}
        </Button>
        <Button
          icon={<ReloadOutlined />}
          disabled={!userId}
          loading={policies.isFetching}
          onClick={() => policies.refetch()}
        >
          {t('upstreamIntelligence.common.refresh')}
        </Button>
        <Typography.Text type="secondary">
          {t('upstreamIntelligence.policies.versionNotice')}
        </Typography.Text>
      </Space>
      {policies.error ? (
        <Alert
          type="error"
          showIcon
          message={t('upstreamIntelligence.policies.loadError')}
          description={friendlyErrorMessage(policies.error)}
        />
      ) : null}
      <div
        className="intelligence-scroll-region"
        role="region"
        aria-label={t('upstreamIntelligence.policies.regionLabel')}
        tabIndex={0}
      >
        <Table<RecommendationExecutionPolicy>
          rowKey="id"
          loading={policies.isLoading}
          dataSource={policies.data ?? []}
          columns={columns}
          pagination={false}
          scroll={{ x: 'max-content' }}
          locale={{
            emptyText: userId
              ? t('upstreamIntelligence.policies.empty')
              : t('upstreamIntelligence.common.selectCustomer'),
          }}
        />
      </div>

      <Modal
        title={
          editing
            ? t('upstreamIntelligence.policies.editTitle')
            : t('upstreamIntelligence.policies.createTitle')
        }
        open={open}
        destroyOnClose
        confirmLoading={save.isPending}
        okText={t('upstreamIntelligence.policies.save')}
        onCancel={() => {
          setOpen(false)
          form.resetFields()
        }}
        onOk={() => form.submit()}
      >
        <Form<PolicyFormValues>
          form={form}
          layout="vertical"
          onFinish={(values) => {
            if (!userId) return
            const input: RecommendationExecutionPolicyInput = {
              user_id: userId,
              scope: values.scope,
              enabled: values.enabled,
              kill_switch: values.kill_switch,
              daily_execution_cap: values.daily_execution_cap,
              cooldown_seconds: values.cooldown_seconds,
              minimum_savings: values.minimum_savings,
              expected_version: editing?.version ?? 0,
              ...(values.scope === 'plan'
                ? { plan_id: values.target_id }
                : { pool_id: values.target_id }),
            }
            save.mutate(input, {
              onSuccess: () => {
                setOpen(false)
                form.resetFields()
              },
            })
          }}
        >
          <Form.Item
            name="scope"
            label={t('upstreamIntelligence.policies.authorizationScope')}
            rules={[{ required: true }]}
          >
            <Select
              disabled={Boolean(editing)}
              options={[
                { value: 'plan', label: t('upstreamIntelligence.policies.onePlan') },
                { value: 'pool', label: t('upstreamIntelligence.policies.onePool') },
              ]}
              onChange={() => form.setFieldValue('target_id', undefined)}
            />
          </Form.Item>
          <Form.Item
            name="target_id"
            label={t('upstreamIntelligence.policies.authorizationTarget')}
            rules={[{ required: true }]}
          >
            <Select
              showSearch
              optionFilterProp="label"
              disabled={Boolean(editing)}
              options={targetOptions}
              placeholder={
                scope === 'pool'
                  ? t('upstreamIntelligence.policies.choosePool')
                  : t('upstreamIntelligence.policies.choosePlan')
              }
            />
          </Form.Item>
          <Form.Item
            name="enabled"
            label={t('upstreamIntelligence.policies.allowAutomatic')}
            valuePropName="checked"
          >
            <Switch
              checkedChildren={t('upstreamIntelligence.policies.authorized')}
              unCheckedChildren={t('upstreamIntelligence.policies.unauthorized')}
            />
          </Form.Item>
          <Form.Item
            name="kill_switch"
            label={t('upstreamIntelligence.policies.killSwitch')}
            valuePropName="checked"
          >
            <Switch
              checkedChildren={t('upstreamIntelligence.policies.preventNew')}
              unCheckedChildren={t('upstreamIntelligence.policies.off')}
            />
          </Form.Item>
          <Form.Item
            name="daily_execution_cap"
            label={t('upstreamIntelligence.policies.executionCap')}
            rules={[{ required: true }]}
          >
            <InputNumber min={1} precision={0} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item
            name="cooldown_seconds"
            label={t('upstreamIntelligence.policies.cooldownSeconds')}
            rules={[{ required: true }]}
          >
            <InputNumber min={0} precision={0} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item
            name="minimum_savings"
            label={t('upstreamIntelligence.policies.minimumSavingsRatio')}
            extra={t('upstreamIntelligence.policies.minimumSavingsHelp')}
            rules={[
              { required: true },
              {
                pattern: /^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,18})?$/,
                message: t('upstreamIntelligence.policies.nonnegativeDecimal'),
              },
            ]}
          >
            <Input placeholder="0.05" inputMode="decimal" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}

function formatSavings(value: string) {
  const number = Number(value)
  return Number.isFinite(number) ? `${(number * 100).toFixed(2)}%` : value
}
