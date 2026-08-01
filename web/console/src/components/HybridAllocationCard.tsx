import { ProCard } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Empty,
  Form,
  InputNumber,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { useEffect, useMemo, useState } from 'react'
import { useInstances } from '../api/hooks'
import {
  useExecuteHybridRouting,
  useHybridAllocation,
  useHybridRoutingExecutions,
  useUpdateHybridAllocation,
} from '../api/hybridSupplyHooks'
import type { HybridAllocationRule } from '../api/hybridSupplyTypes'
import { friendlyErrorMessage } from '../api/errors'
import { ApiError } from '../api/client'
import { getLocale } from '../i18n'

interface AllocationForm {
  owner: number
  economy: number
  stable: number
  ownerMax: number
  economyMax: number
  stableMax: number
  dailyBudget: number
  maxUnitPrice: number
}

const defaultAllocationForm: AllocationForm = {
  owner: 80,
  economy: 20,
  stable: 0,
  ownerMax: 100,
  economyMax: 30,
  stableMax: 30,
  dailyBudget: 0,
  maxUnitPrice: 0,
}

function statusTag(status: string) {
  const color = status === 'succeeded' ? 'success' : status === 'failed' ? 'error' : 'processing'
  return <Tag color={color}>{status}</Tag>
}

export default function HybridAllocationCard() {
  const english = getLocale() === 'en'
  const instances = useInstances()
  const available = useMemo(
    () =>
      (instances.data ?? []).filter(
        (instance) => instance.kind === 'newapi' && instance.connector_id,
      ),
    [instances.data],
  )
  const [selectedPreference, setSelectedPreference] = useState<string>()
  const selected = available.some((instance) => instance.id === selectedPreference)
    ? selectedPreference
    : available[0]?.id
  const allocation = useHybridAllocation(selected)
  const executions = useHybridRoutingExecutions(selected)
  const update = useUpdateHybridAllocation(selected ?? '')
  const execute = useExecuteHybridRouting(selected ?? '')
  const [form] = Form.useForm<AllocationForm>()

  useEffect(() => {
    if (!selected || !allocation.data) {
      form.setFieldsValue(defaultAllocationForm)
      return
    }
    const rule = allocation.data.default_rule
    form.setFieldsValue({
      owner: rule.owner_percent,
      economy: rule.economy_percent,
      stable: rule.stable_percent,
      ownerMax: rule.owner_burst_max,
      economyMax: rule.economy_burst_max,
      stableMax: rule.stable_burst_max,
      dailyBudget: allocation.data.daily_budget_micros / 1_000_000,
      maxUnitPrice: allocation.data.max_unit_price_micros / 1_000_000,
    })
  }, [allocation.data, form, selected])

  const allocationMissing = allocation.error instanceof ApiError && allocation.error.status === 404
  const allocationUnavailable = Boolean(allocation.error) && !allocationMissing

  const save = (values: AllocationForm) => {
    if (allocationUnavailable) return
    const rule: HybridAllocationRule = {
      owner_percent: values.owner,
      economy_percent: values.economy,
      stable_percent: values.stable,
      owner_burst_max: values.ownerMax,
      economy_burst_max: values.economyMax,
      stable_burst_max: values.stableMax,
    }
    update.mutate({
      basis: 'requests',
      default_rule: rule,
      model_overrides: allocation.data?.model_overrides ?? [],
      daily_budget_micros: Math.round(values.dailyBudget * 1_000_000),
      max_unit_price_micros: Math.round(values.maxUnitPrice * 1_000_000),
      expected_version: allocation.data?.version ?? 0,
    })
  }

  return (
    <ProCard
      title={english ? 'Three-pool allocation' : '三池流量配置'}
      subTitle={
        english
          ? 'Allocate request traffic across your own pool, E2M Economy, and E2M Stable.'
          : '按请求数配置自有号池、E2M 低价池与 E2M 稳定池；异常调整不会超过弹性上限。'
      }
      bordered
      style={{ marginBottom: 16 }}
    >
      {available.length === 0 ? (
        <Empty
          description={english ? 'Connect a NewAPI instance first' : '请先接入一个 NewAPI 实例'}
        />
      ) : (
        <>
          <Space style={{ marginBottom: 16 }} wrap>
            <Typography.Text>{english ? 'Instance' : '实例'}</Typography.Text>
            <Select
              value={selected}
              style={{ minWidth: 220 }}
              options={available.map((instance) => ({ label: instance.name, value: instance.id }))}
              onChange={setSelectedPreference}
            />
            <Tag color="blue">{english ? 'Request-count basis' : '按请求数'}</Tag>
          </Space>
          {allocationMissing ? (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message={english ? 'No allocation saved yet' : '尚未保存三池配置'}
              description={friendlyErrorMessage(allocation.error)}
            />
          ) : null}
          {allocationUnavailable ? (
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 16 }}
              message={english ? 'Allocation could not be loaded' : '三池配置加载失败'}
              description={friendlyErrorMessage(allocation.error)}
              action={
                <Button onClick={() => allocation.refetch()}>{english ? 'Retry' : '重试'}</Button>
              }
            />
          ) : null}
          <Form<AllocationForm>
            form={form}
            layout="vertical"
            initialValues={defaultAllocationForm}
            onFinish={save}
          >
            <Space wrap align="start" size="large">
              {[
                ['owner', english ? 'Own pool %' : '自有号池 %'],
                ['economy', english ? 'Economy %' : 'E2M 低价池 %'],
                ['stable', english ? 'Stable %' : 'E2M 稳定池 %'],
              ].map(([name, label]) => (
                <Form.Item
                  key={name}
                  name={name}
                  label={label}
                  dependencies={['owner', 'economy', 'stable']}
                  rules={[
                    { required: true },
                    ({ getFieldValue }) => ({
                      validator: () =>
                        Number(getFieldValue('owner')) +
                          Number(getFieldValue('economy')) +
                          Number(getFieldValue('stable')) ===
                        100
                          ? Promise.resolve()
                          : Promise.reject(
                              new Error(
                                english
                                  ? 'The three shares must total 100%'
                                  : '三池比例之和必须为 100%',
                              ),
                            ),
                    }),
                  ]}
                >
                  <InputNumber min={0} max={100} />
                </Form.Item>
              ))}
            </Space>
            <Space wrap align="start" size="large">
              {[
                ['ownerMax', english ? 'Own elasticity max %' : '自有池弹性上限 %'],
                ['economyMax', english ? 'Economy elasticity max %' : '低价池弹性上限 %'],
                ['stableMax', english ? 'Stable elasticity max %' : '稳定池弹性上限 %'],
              ].map(([name, label]) => (
                <Form.Item
                  key={name}
                  name={name}
                  label={label}
                  dependencies={[
                    name === 'ownerMax' ? 'owner' : name === 'economyMax' ? 'economy' : 'stable',
                  ]}
                  rules={[
                    { required: true },
                    ({ getFieldValue }) => ({
                      validator: (_, value) => {
                        const target = getFieldValue(
                          name === 'ownerMax'
                            ? 'owner'
                            : name === 'economyMax'
                              ? 'economy'
                              : 'stable',
                        )
                        return Number(value) >= Number(target)
                          ? Promise.resolve()
                          : Promise.reject(
                              new Error(
                                english
                                  ? 'Elastic maximum cannot be below its target'
                                  : '弹性上限不能低于目标比例',
                              ),
                            )
                      },
                    }),
                  ]}
                >
                  <InputNumber min={0} max={100} />
                </Form.Item>
              ))}
            </Space>
            <Space wrap align="start" size="large">
              <Form.Item
                name="dailyBudget"
                label={english ? 'Daily platform budget' : '平台池日预算'}
              >
                <InputNumber min={0} precision={2} addonAfter="CNY" />
              </Form.Item>
              <Form.Item
                name="maxUnitPrice"
                label={english ? 'Max per-request price' : '单请求价格上限'}
              >
                <InputNumber min={0} precision={4} addonAfter="CNY" />
              </Form.Item>
            </Space>
            <Space>
              <Button
                type="primary"
                htmlType="submit"
                loading={update.isPending}
                disabled={allocationUnavailable}
              >
                {english ? 'Save allocation' : '保存比例'}
              </Button>
              <Button
                loading={execute.isPending}
                disabled={!allocation.data}
                onClick={() =>
                  allocation.data && execute.mutate({ version: allocation.data.version })
                }
              >
                {english ? 'Apply now' : '立即执行'}
              </Button>
            </Space>
          </Form>
          {update.error || execute.error ? (
            <Alert
              type="error"
              showIcon
              style={{ marginTop: 12 }}
              message={friendlyErrorMessage(update.error ?? execute.error)}
            />
          ) : null}
          <Table
            style={{ marginTop: 20 }}
            rowKey="id"
            size="small"
            pagination={false}
            dataSource={executions.data ?? []}
            columns={[
              { title: english ? 'Status' : '状态', dataIndex: 'status', render: statusTag },
              { title: english ? 'Generation' : '代次', dataIndex: 'generation' },
              {
                title: english ? 'Target / effective / actual' : '目标 / 生效 / 实际',
                render: (_, row) =>
                  [row.target, row.effective, row.actual]
                    .map(
                      (value) =>
                        value &&
                        `${value.owner ?? '-'} / ${value.economy ?? '-'} / ${value.stable ?? '-'}`,
                    )
                    .join('  ·  '),
              },
              { title: english ? 'Error' : '异常', dataIndex: 'error_code' },
            ]}
          />
        </>
      )}
    </ProCard>
  )
}
