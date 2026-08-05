import { useMemo, useState } from 'react'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import {
  Alert,
  App,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Segmented,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import {
  CheckCircleOutlined,
  EditOutlined,
  MinusCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SwapOutlined,
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
import ModelWhitelistPanel from '../components/ModelWhitelistPanel'
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

function Section({ title, desc }: { title: string; desc?: string }) {
  return (
    <div style={{ margin: '20px 0 12px' }}>
      <Typography.Text strong style={{ fontSize: 15 }}>
        {title}
      </Typography.Text>
      {desc ? (
        <Typography.Paragraph type="secondary" style={{ margin: '4px 0 0', fontSize: 12 }}>
          {desc}
        </Typography.Paragraph>
      ) : null}
    </div>
  )
}

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
  const [modelTab, setModelTab] = useState<'whitelist' | 'mapping'>('whitelist')
  const [form] = Form.useForm()
  // The whitelist suggestions follow the group currently chosen in the form,
  // because a group declares the models it is allowed to sell.
  const watchedGroupId = Form.useWatch('group_id', form)
  const selectedGroupModels = useMemo(
    () => (groups.data ?? []).find((group) => group.id === watchedGroupId)?.models ?? [],
    [groups.data, watchedGroupId],
  )
  const fillFromGroup = () => {
    const current: string[] = form.getFieldValue('models') ?? []
    form.setFieldValue('models', Array.from(new Set([...current, ...selectedGroupModels])))
  }

  const groupMap = useMemo(
    () => new Map((groups.data ?? []).map((group) => [group.id, group])),
    [groups.data],
  )
  const hasActiveGroup = (groups.data ?? []).some((group) => group.status === 'active')

  const openNew = () => {
    setEditingId(undefined)
    setModelTab('whitelist')
    form.resetFields()
    form.setFieldsValue({
      group_id: (groups.data ?? []).find((group) => group.status === 'active')?.id,
      priority: 0,
      weight: 1,
      max_concurrency: 0,
      capacity_percent: 100,
      max_request_micros: 1,
      status: 'active',
      models: [],
    })
    setOpen(true)
  }
  const openEdit = (upstream: PlatformUpstream) => {
    const firstPrice = upstream.models.map((model) => upstream.prices?.[model]).find(Boolean)
    setEditingId(upstream.id)
    setModelTab('whitelist')
    form.resetFields()
    form.setFieldsValue({
      group_id: upstream.group_id,
      name: upstream.name,
      base_url: upstream.base_url,
      api_key: undefined,
      models: [...(upstream.models ?? [])],
      input_price:
        firstPrice && firstPrice.input_micros_per_million > 0
          ? firstPrice.input_micros_per_million / 1_000_000
          : undefined,
      output_price:
        firstPrice && firstPrice.output_micros_per_million > 0
          ? firstPrice.output_micros_per_million / 1_000_000
          : undefined,
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
      // Merge rather than replace: an operator may have added models the
      // upstream does not advertise, and losing them silently would be worse
      // than showing a superset they can trim.
      const current: string[] = form.getFieldValue('models') ?? []
      const merged = Array.from(new Set([...current, ...result.models]))
      form.setFieldValue('models', merged)
      const added = merged.length - current.length
      message.info(
        added > 0
          ? t('platformUpstreams.discovered', '已从上游同步 {n} 个新模型，请确认后保存', { n: added })
          : t('platformUpstreams.discoveredNone', '上游返回的 {n} 个模型均已在白名单中', {
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
        width={720}
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ priority: 0, weight: 1, capacity_percent: 100, max_request_micros: 1, status: 'active' }}
          onFinish={async (values) => {
            const models = ((values.models ?? []) as string[])
              .map((model) => model.trim())
              .filter(Boolean)
            const hasInputPrice = values.input_price != null && values.input_price > 0
            const hasOutputPrice = values.output_price != null && values.output_price > 0
            if (hasInputPrice !== hasOutputPrice) {
              message.error(
                t('platformUpstreams.form.priceBothOrNone', '输入价与输出价需同时填写，或同时留空走自动定价'),
              )
              return
            }
            // Omitted prices mean: base-table auto pricing on create, keep the
            // current prices on edit. The backend enforces both semantics.
            const prices = hasInputPrice
              ? Object.fromEntries(
                  models.map((model) => [
                    model,
                    {
                      input_micros_per_million: Math.round((values.input_price ?? 0) * 1_000_000),
                      output_micros_per_million: Math.round((values.output_price ?? 0) * 1_000_000),
                    },
                  ]),
                )
              : undefined
            const input = {
              group_id: values.group_id,
              name: values.name,
              base_url: values.base_url,
              ...(values.api_key ? { api_key: values.api_key } : {}),
              models,
              ...(prices ? { prices } : {}),
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
          <Form.Item
            name="group_id"
            label={t('platformUpstreams.form.group', '所属分组')}
            rules={[{ required: true }]}
            extra={
              editingId
                ? t(
                    'platformUpstreams.form.groupMoveHint',
                    '可改为其他启用中的分组；历史用量与账务按发生时的分组保留。',
                  )
                : undefined
            }
          >
            <Select
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
          <Section
            title={t('platformUpstreams.form.modelSection', '模型限制')}
            desc={t(
              'platformUpstreams.form.modelSectionDesc',
              '白名单声明该上游可承接的模型；映射把请求模型改写为上游实际模型。',
            )}
          />
          <Segmented
            block
            value={modelTab}
            onChange={(key) => setModelTab(key as 'whitelist' | 'mapping')}
            options={[
              {
                label: t('platformUpstreams.form.whitelistTab', '模型白名单'),
                value: 'whitelist',
                icon: <CheckCircleOutlined />,
              },
              {
                label: t('platformUpstreams.form.mappingTab', '模型映射'),
                value: 'mapping',
                icon: <SwapOutlined />,
              },
            ]}
            style={{ marginBottom: 16 }}
          />
          <div style={{ display: modelTab === 'whitelist' ? 'block' : 'none' }}>
            <Form.Item
              name="models"
              rules={[
                {
                  required: true,
                  message: t('platformUpstreams.form.modelsRequired', '至少选择一个模型'),
                },
              ]}
            >
              <ModelWhitelistPanel
                actions={
                  <Space wrap>
                    <Button size="small" onClick={fillFromGroup} disabled={!selectedGroupModels.length}>
                      {t('platformUpstreams.form.fillFromGroup', '同步分组模型')}
                    </Button>
                    <Button
                      size="small"
                      icon={<ThunderboltOutlined />}
                      loading={testUpstream.isPending}
                      disabled={!editingId}
                      title={
                        !editingId
                          ? t(
                              'platformUpstreams.form.syncAfterSave',
                              '「同步上游支持的模型」需要先保存上游（凭证入库后才能连接上游查询）。',
                            )
                          : undefined
                      }
                      onClick={() => void testAndLoadModels()}
                    >
                      {t('platformUpstreams.form.syncUpstream', '同步上游支持的模型')}
                    </Button>
                    <Button size="small" danger onClick={() => form.setFieldValue('models', [])}>
                      {t('platformUpstreams.form.clearModels', '清除所有模型')}
                    </Button>
                  </Space>
                }
              />
            </Form.Item>
          </div>
          <div style={{ display: modelTab === 'mapping' ? 'block' : 'none' }}>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
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
                        rules={[
                          {
                            required: true,
                            message: t('platformUpstreams.form.mappingFromRequired', '请填写请求模型'),
                          },
                        ]}
                      >
                        <Input
                          style={{ width: 250 }}
                          placeholder={t('platformUpstreams.form.mappingFrom', '请求模型')}
                        />
                      </Form.Item>
                      <SwapOutlined style={{ color: '#999' }} />
                      <Form.Item
                        name={[field.name, 'to']}
                        noStyle
                        rules={[
                          {
                            required: true,
                            message: t('platformUpstreams.form.mappingToRequired', '请填写上游模型'),
                          },
                        ]}
                      >
                        <Input
                          style={{ width: 250 }}
                          placeholder={t('platformUpstreams.form.mappingTo', '上游实际模型')}
                        />
                      </Form.Item>
                      <Button
                        type="text"
                        danger
                        icon={<MinusCircleOutlined />}
                        onClick={() => remove(field.name)}
                      />
                    </Space>
                  ))}
                  <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add()}>
                    {t('platformUpstreams.form.mappingAdd', '添加映射')}
                  </Button>
                </>
              )}
            </Form.List>
          </div>
          <Section
            title={t('platformUpstreams.form.priceSection', '价格（可选覆盖）')}
            desc={
              editingId
                ? t(
                    'platformUpstreams.form.priceSectionDescEdit',
                    '留空保持当前价格；填写则覆盖。自动定价 = 基准价 × 汇率 × 分组倍率。',
                  )
                : t(
                    'platformUpstreams.form.priceSectionDescNew',
                    '留空时按基准价目表自动定价（基准价 × 汇率 × 分组倍率）；填写则以此为准。',
                  )
            }
          />
          <Space>
            <Form.Item name="input_price" label={t('platformUpstreams.form.inputPrice', '输入价（元/百万 Token）')}>
              <InputNumber min={0} placeholder={t('platformUpstreams.form.priceAuto', '自动')} style={{ width: 180 }} />
            </Form.Item>
            <Form.Item name="output_price" label={t('platformUpstreams.form.outputPrice', '输出价（元/百万 Token）')}>
              <InputNumber min={0} placeholder={t('platformUpstreams.form.priceAuto', '自动')} style={{ width: 180 }} />
            </Form.Item>
          </Space>
          <Section title={t('platformUpstreams.form.schedulingSection', '调度')} />
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
          <Section
            title={t('platformUpstreams.form.cooldownTitle', '错误冷却规则（可选）')}
            desc={t(
              'platformUpstreams.form.cooldownHint',
              '命中的错误会让该上游临时退出调度，冷却期内新请求自动避开。关键词留空表示只匹配状态码。',
            )}
          />
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

          <Section title={t('platformUpstreams.form.advanced', '其他')} />
          <Form.Item name="labels" label={t('platformUpstreams.form.labels', '标签（key=value，逗号分隔）')}>
            <Input placeholder="region=cn, lane=primary" />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
