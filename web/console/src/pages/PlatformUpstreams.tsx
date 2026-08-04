import { useMemo, useState } from 'react'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import {
  Alert,
  App,
  Button,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import {
  EditOutlined,
  MinusCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import {
  useCreatePlatformUpstream,
  useDeletePlatformUpstream,
  usePlatformGroups,
  usePlatformUpstreams,
  useTestPlatformUpstream,
  useUpdatePlatformUpstream,
} from '../api/platformDistributionHooks'
import type { PlatformUpstream } from '../api/types'
import {
  COOLDOWN_RULES_LABEL,
  MODEL_MAPPING_LABEL,
  cooldownRowsFromLabel,
  labelsFromForm,
  modelMappingRowsFromLabel,
  otherLabelsText,
} from './upstreamLabels'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

export default function PlatformUpstreams() {
  useLocaleVersion()
  const { message } = App.useApp()
  const groups = usePlatformGroups(true)
  const upstreams = usePlatformUpstreams(true)
  const createUpstream = useCreatePlatformUpstream()
  const updateUpstream = useUpdatePlatformUpstream()
  const deleteUpstream = useDeletePlatformUpstream()
  const testUpstream = useTestPlatformUpstream()
  const [open, setOpen] = useState(false)
  const [editingId, setEditingId] = useState<string>()
  const [form] = Form.useForm()

  const groupMap = useMemo(
    () => new Map((groups.data ?? []).map((group) => [group.id, group])),
    [groups.data],
  )
  const hasActiveGroup = (groups.data ?? []).some((group) => group.status === 'active')

  const openNew = () => {
    setEditingId(undefined)
    form.resetFields()
    form.setFieldsValue({
      group_id: (groups.data ?? []).find((group) => group.status === 'active')?.id,
      priority: 0,
      weight: 1,
      max_concurrency: 0,
      capacity_percent: 100,
      max_request_micros: 1,
      status: 'active',
    })
    setOpen(true)
  }
  const openEdit = (upstream: PlatformUpstream) => {
    const firstPrice = upstream.models.map((model) => upstream.prices?.[model]).find(Boolean)
    setEditingId(upstream.id)
    form.resetFields()
    form.setFieldsValue({
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
      labels: otherLabelsText(upstream.labels),
      model_mapping: modelMappingRowsFromLabel(upstream.labels?.[MODEL_MAPPING_LABEL]),
      cooldown_rules: cooldownRowsFromLabel(upstream.labels?.[COOLDOWN_RULES_LABEL]),
    })
    setOpen(true)
  }
  const close = () => {
    setOpen(false)
    setEditingId(undefined)
    form.resetFields()
  }
  const testAndLoadModels = async () => {
    if (!editingId) return
    const result = await testUpstream.mutateAsync(editingId)
    if (result.ok && result.models?.length) {
      form.setFieldValue('models', result.models.join(', '))
      message.info(
        t('platformUpstreams.discovered', '已从上游发现 {n} 个模型，请确认后保存', {
          n: result.models.length,
        }),
      )
    }
  }

  return (
    <PageContainer
      title={t('platformUpstreams.title', '上游账号')}
      subTitle={t(
        'platformUpstreams.subtitle',
        'E2M 平台侧接入的 OpenAI 兼容供给账号（凭证 Vault 加密，永不回显）',
      )}
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => upstreams.refetch()}>
            {t('common.refresh', '刷新')}
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openNew} disabled={!hasActiveGroup}>
            {t('platformUpstreams.new', '接入上游')}
          </Button>
        </Space>
      }
    >
      {!hasActiveGroup ? (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('platformUpstreams.needGroup', '请先在「分组管理」创建并启用一个分组，再接入上游账号')}
        />
      ) : null}
      <ProCard bordered>
        <Table<PlatformUpstream>
          rowKey="id"
          size="small"
          loading={upstreams.isLoading}
          dataSource={upstreams.data ?? []}
          pagination={{ pageSize: 10 }}
          columns={[
            { title: t('platformUpstreams.columns.name', '名称'), dataIndex: 'name' },
            {
              title: t('platformUpstreams.columns.group', '分组'),
              dataIndex: 'group_id',
              render: (v: string) => groupMap.get(v)?.name ?? v,
            },
            { title: t('platformUpstreams.columns.baseUrl', '地址'), dataIndex: 'base_url', ellipsis: true },
            {
              title: t('platformUpstreams.columns.models', '模型'),
              dataIndex: 'models',
              render: (v: string[]) => (v ?? []).join(', '),
            },
            {
              title: t('platformUpstreams.columns.apiKey', 'API Key'),
              dataIndex: 'api_key_masked',
              render: (v: string) => v || t('platformUpstreams.configured', '已配置'),
            },
            {
              title: t('platformUpstreams.columns.status', '状态'),
              dataIndex: 'status',
              render: (v: string) => (
                <Tag color={v === 'active' ? 'green' : v === 'retired' ? 'default' : 'orange'}>
                  {v === 'active'
                    ? t('platformGroups.status.active', '启用')
                    : v === 'retired'
                      ? t('platformGroups.status.retired', '已退休')
                      : t('platformGroups.status.maintenance', '维护中')}
                </Tag>
              ),
            },
            { title: t('platformUpstreams.columns.priority', '优先级'), dataIndex: 'priority' },
            { title: t('platformUpstreams.columns.weight', '权重'), dataIndex: 'weight' },
            {
              title: t('platformUpstreams.columns.capacity', '容量'),
              render: (_: unknown, row: PlatformUpstream) =>
                `${row.capacity?.capacity_percent ?? 100}% / ${
                  row.capacity?.max_concurrency || t('platformUpstreams.unlimited', '不限')
                }`,
            },
            {
              title: t('platformGroups.columns.actions', '操作'),
              key: 'actions',
              render: (_: unknown, row: PlatformUpstream) => (
                <Space size="small" wrap>
                  <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>
                    {t('common.edit', '编辑')}
                  </Button>
                  <Button
                    size="small"
                    icon={<ThunderboltOutlined />}
                    loading={testUpstream.isPending && testUpstream.variables === row.id}
                    onClick={() => testUpstream.mutate(row.id)}
                  >
                    {t('platformUpstreams.test', '测试')}
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
                      {row.status === 'active'
                        ? t('common.disable', '停用')
                        : t('common.enable', '启用')}
                    </Button>
                  ) : null}
                  {row.status !== 'retired' ? (
                    <Popconfirm
                      title={t('platformUpstreams.retireConfirm', '确认下线此上游？')}
                      description={t(
                        'platformUpstreams.retireDesc',
                        '上游将停止接收新请求，凭证和审计记录会保留。',
                      )}
                      okText={t('platformUpstreams.retireOk', '确认下线')}
                      cancelText={t('common.cancel', '取消')}
                      onConfirm={() => deleteUpstream.mutate(row.id)}
                    >
                      <Button size="small" danger loading={deleteUpstream.isPending}>
                        {t('platformUpstreams.retire', '下线')}
                      </Button>
                    </Popconfirm>
                  ) : null}
                </Space>
              ),
            },
          ]}
        />
      </ProCard>

      <Modal
        title={
          editingId ? t('platformUpstreams.editTitle', '编辑上游') : t('platformUpstreams.new', '接入上游')
        }
        open={open}
        onCancel={close}
        confirmLoading={createUpstream.isPending || updateUpstream.isPending}
        onOk={() => form.submit()}
        width={640}
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ priority: 0, weight: 1, capacity_percent: 100, max_request_micros: 1, status: 'active' }}
          onFinish={async (values) => {
            const models = String(values.models ?? '')
              .split(',')
              .map((model: string) => model.trim())
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
              labels: labelsFromForm(values),
              capacity: {
                max_concurrency: values.max_concurrency || 0,
                capacity_percent: values.capacity_percent ?? 100,
                max_request_micros: Math.round((values.max_request_micros || 1) * 1_000_000),
              },
            }
            if (editingId) {
              await updateUpstream.mutateAsync({ id: editingId, input })
            } else {
              await createUpstream.mutateAsync({ ...input, api_key: values.api_key as string })
            }
            close()
          }}
        >
          <Alert
            type="info"
            showIcon
            message={t(
              'platformUpstreams.protocolHint',
              '当前数据面支持 OpenAI-compatible API（Bearer 凭证与 /v1/chat/completions）；其他协议需先接入适配器。',
            )}
            style={{ marginBottom: 16 }}
          />
          <Form.Item name="group_id" label={t('platformUpstreams.form.group', '所属分组')} rules={[{ required: true }]}>
            <Select
              disabled={Boolean(editingId)}
              options={(groups.data ?? []).map((g) => ({
                value: g.id,
                label: g.name,
                disabled: g.status !== 'active',
              }))}
            />
          </Form.Item>
          <Form.Item name="name" label={t('platformUpstreams.form.name', '名称')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="base_url" label={t('platformUpstreams.form.baseUrl', 'Base URL')} rules={[{ required: true }]}>
            <Input placeholder="https://api.example.com/v1" />
          </Form.Item>
          <Form.Item
            name="api_key"
            label={
              editingId
                ? t('platformUpstreams.form.apiKeyEdit', '上游 API Key（留空表示不更换）')
                : t('platformUpstreams.form.apiKey', '上游 API Key')
            }
            rules={editingId ? [] : [{ required: true }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item label={t('platformUpstreams.form.models', '模型（逗号分隔）')} required>
            <Space.Compact style={{ width: '100%' }}>
              <Form.Item name="models" noStyle rules={[{ required: true }]}>
                <Input placeholder="gpt-4o-mini, gpt-4.1-mini" />
              </Form.Item>
              {editingId ? (
                <Button
                  icon={<ThunderboltOutlined />}
                  loading={testUpstream.isPending}
                  onClick={() => void testAndLoadModels()}
                >
                  {t('platformUpstreams.form.testPull', '测试并拉取')}
                </Button>
              ) : null}
            </Space.Compact>
          </Form.Item>
          <Space>
            <Form.Item name="input_price" label={t('platformUpstreams.form.inputPrice', '输入价（元/百万 Token）')} rules={[{ required: true }]}>
              <InputNumber min={0} />
            </Form.Item>
            <Form.Item name="output_price" label={t('platformUpstreams.form.outputPrice', '输出价（元/百万 Token）')} rules={[{ required: true }]}>
              <InputNumber min={0} />
            </Form.Item>
          </Space>
          <Space wrap>
            <Form.Item name="priority" label={t('platformUpstreams.form.priority', '优先级')}>
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="weight" label={t('platformUpstreams.form.weight', '权重')}>
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="max_concurrency" label={t('platformUpstreams.form.maxConcurrency', '最大并发')}>
              <InputNumber min={0} precision={0} />
            </Form.Item>
            <Form.Item name="capacity_percent" label={t('platformUpstreams.form.capacityPercent', '容量百分比')}>
              <InputNumber min={0} max={100} precision={0} />
            </Form.Item>
            <Form.Item name="max_request_micros" label={t('platformUpstreams.form.maxRequest', '单请求上限（元）')}>
              <InputNumber min={0.000001} precision={6} />
            </Form.Item>
          </Space>
          {editingId ? (
            <Form.Item name="status" label={t('platformUpstreams.form.status', '状态')}>
              <Select
                options={[
                  { value: 'active', label: t('platformGroups.status.active', '启用') },
                  { value: 'maintenance', label: t('platformGroups.status.maintenance', '维护中') },
                ]}
              />
            </Form.Item>
          ) : null}
          <Divider orientation="left" plain>
            {t('platformUpstreams.form.mappingTitle', '模型映射（可选）')}
          </Divider>
          <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
            {t(
              'platformUpstreams.form.mappingHint',
              '把下游请求的模型名改写为该上游实际支持的模型名；不填表示原样透传。',
            )}
          </Typography.Paragraph>
          <Form.List name="model_mapping">
            {(fields, { add, remove }) => (
              <>
                {fields.map((field) => (
                  <Space key={field.key} align="baseline" style={{ display: 'flex', marginBottom: 8 }}>
                    <Form.Item
                      name={[field.name, 'from']}
                      noStyle
                      rules={[{ required: true, message: t('platformUpstreams.form.mappingFromRequired', '请填写请求模型') }]}
                    >
                      <Input
                        style={{ width: 220 }}
                        placeholder={t('platformUpstreams.form.mappingFrom', '请求模型')}
                      />
                    </Form.Item>
                    <span>→</span>
                    <Form.Item
                      name={[field.name, 'to']}
                      noStyle
                      rules={[{ required: true, message: t('platformUpstreams.form.mappingToRequired', '请填写上游模型') }]}
                    >
                      <Input
                        style={{ width: 220 }}
                        placeholder={t('platformUpstreams.form.mappingTo', '上游实际模型')}
                      />
                    </Form.Item>
                    <Button type="text" danger icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} />
                  </Space>
                ))}
                <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add()}>
                  {t('platformUpstreams.form.mappingAdd', '添加映射')}
                </Button>
              </>
            )}
          </Form.List>

          <Divider orientation="left" plain style={{ marginTop: 24 }}>
            {t('platformUpstreams.form.cooldownTitle', '错误冷却规则（可选）')}
          </Divider>
          <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
            {t(
              'platformUpstreams.form.cooldownHint',
              '命中的错误会让该上游临时退出调度，冷却期内新请求自动避开。关键词留空表示只匹配状态码。',
            )}
          </Typography.Paragraph>
          <Form.List name="cooldown_rules">
            {(fields, { add, remove }) => (
              <>
                {fields.map((field) => (
                  <Space key={field.key} align="baseline" wrap style={{ display: 'flex', marginBottom: 8 }}>
                    <Form.Item
                      name={[field.name, 'status']}
                      noStyle
                      rules={[{ required: true, message: t('platformUpstreams.form.cooldownStatusRequired', '请填写状态码') }]}
                    >
                      <InputNumber
                        min={100}
                        max={599}
                        precision={0}
                        style={{ width: 110 }}
                        placeholder={t('platformUpstreams.form.cooldownStatus', '状态码')}
                      />
                    </Form.Item>
                    <Form.Item name={[field.name, 'keywords']} noStyle>
                      <Input
                        style={{ width: 260 }}
                        placeholder={t('platformUpstreams.form.cooldownKeywords', '关键词（逗号分隔，可空）')}
                      />
                    </Form.Item>
                    <Form.Item
                      name={[field.name, 'cooldown_seconds']}
                      noStyle
                      rules={[{ required: true, message: t('platformUpstreams.form.cooldownSecondsRequired', '请填写冷却秒数') }]}
                    >
                      <InputNumber
                        min={1}
                        max={86400}
                        precision={0}
                        style={{ width: 140 }}
                        placeholder={t('platformUpstreams.form.cooldownSeconds', '冷却秒数')}
                      />
                    </Form.Item>
                    <Button type="text" danger icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} />
                  </Space>
                ))}
                <Button
                  type="dashed"
                  block
                  icon={<PlusOutlined />}
                  onClick={() => add({ status: 429, keywords: '', cooldown_seconds: 300 })}
                >
                  {t('platformUpstreams.form.cooldownAdd', '添加规则')}
                </Button>
              </>
            )}
          </Form.List>

          <Divider orientation="left" plain style={{ marginTop: 24 }}>
            {t('platformUpstreams.form.advanced', '其他')}
          </Divider>
          <Form.Item name="labels" label={t('platformUpstreams.form.labels', '标签（key=value，逗号分隔）')}>
            <Input placeholder="region=cn, lane=primary" />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
