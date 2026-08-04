import { useState } from 'react'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import { Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag } from 'antd'
import { EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import {
  useCreatePlatformGroup,
  useDeletePlatformGroup,
  usePlatformGroups,
  useUpdatePlatformGroup,
} from '../api/platformDistributionHooks'
import type { PlatformGroup } from '../api/types'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

const RATE_MULTIPLIER_LABEL = 'e2m.rate_multiplier_bps'

function parseLabels(value: unknown): Record<string, string> {
  const labels: Record<string, string> = {}
  for (const entry of String(value ?? '').split(',')) {
    const [rawKey, ...rawValue] = entry.split('=')
    const key = rawKey?.trim()
    const labelValue = rawValue.join('=').trim()
    if (key && labelValue) labels[key] = labelValue
  }
  return labels
}

// PlatformGroups is the dedicated admin page for distribution groups. A group
// is simply a named, sellable pool of upstreams; it carries no fixed product
// tier — the server assigns a resource class internally.
export default function PlatformGroups() {
  useLocaleVersion()
  const groups = usePlatformGroups(true)
  const createGroup = useCreatePlatformGroup()
  const updateGroup = useUpdatePlatformGroup()
  const deleteGroup = useDeletePlatformGroup()
  const [open, setOpen] = useState(false)
  const [editingId, setEditingId] = useState<string>()
  const [form] = Form.useForm()

  const openNew = () => {
    setEditingId(undefined)
    form.resetFields()
    form.setFieldsValue({ status: 'active' })
    setOpen(true)
  }
  const openEdit = (group: PlatformGroup) => {
    setEditingId(group.id)
    form.resetFields()
    const multiplierBps = Number(group.labels?.[RATE_MULTIPLIER_LABEL] ?? '')
    form.setFieldsValue({
      name: group.name,
      provider: group.provider,
      region: group.region,
      description: group.description,
      status: group.status,
      models: (group.models ?? []).join(', '),
      labels: Object.entries(group.labels ?? {})
        .filter(([key]) => key !== RATE_MULTIPLIER_LABEL)
        .map(([key, value]) => `${key}=${value}`)
        .join(', '),
      rate_multiplier:
        Number.isFinite(multiplierBps) && multiplierBps > 0 ? multiplierBps / 10000 : undefined,
    })
    setOpen(true)
  }
  const close = () => {
    setOpen(false)
    setEditingId(undefined)
    form.resetFields()
  }

  return (
    <PageContainer
      title={t('platformGroups.title', '分组管理')}
      subTitle={t('platformGroups.subtitle', '分组是可售卖的上游资源池，下游 Key 绑定到分组消费')}
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => groups.refetch()}>
            {t('common.refresh', '刷新')}
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>
            {t('platformGroups.new', '新建分组')}
          </Button>
        </Space>
      }
    >
      <ProCard bordered>
        <Table<PlatformGroup>
          rowKey="id"
          size="small"
          loading={groups.isLoading}
          dataSource={groups.data ?? []}
          pagination={{ pageSize: 12 }}
          columns={[
            { title: t('platformGroups.columns.name', '名称'), dataIndex: 'name' },
            {
              title: t('platformGroups.columns.models', '模型'),
              dataIndex: 'models',
              render: (v: string[]) => (v ?? []).join(', ') || t('platformGroups.unlimited', '未限定'),
            },
            {
              title: t('platformGroups.columns.multiplier', '售价倍率'),
              dataIndex: 'labels',
              render: (labels: Record<string, string> | undefined) => {
                const bps = Number(labels?.[RATE_MULTIPLIER_LABEL] ?? '')
                return Number.isFinite(bps) && bps > 0 ? `${bps / 10000}×` : '1×'
              },
            },
            {
              title: t('platformGroups.columns.status', '状态'),
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
            {
              title: t('platformGroups.columns.actions', '操作'),
              key: 'actions',
              render: (_: unknown, row: PlatformGroup) => (
                <Space size="small">
                  <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(row)}>
                    {t('common.edit', '编辑')}
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
                      {row.status === 'active'
                        ? t('common.disable', '停用')
                        : t('common.enable', '启用')}
                    </Button>
                  ) : null}
                  {row.status !== 'retired' ? (
                    <Popconfirm
                      title={t('platformGroups.retireConfirm', '确认退休此分组？')}
                      description={t(
                        'platformGroups.retireDesc',
                        '退休后不会再接受新的请求，历史账务仍会保留。',
                      )}
                      okText={t('platformGroups.retireOk', '确认退休')}
                      cancelText={t('common.cancel', '取消')}
                      onConfirm={() => deleteGroup.mutate(row.id)}
                    >
                      <Button size="small" danger loading={deleteGroup.isPending}>
                        {t('platformGroups.retire', '退休')}
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
        title={editingId ? t('platformGroups.editTitle', '编辑分组') : t('platformGroups.new', '新建分组')}
        open={open}
        onCancel={close}
        confirmLoading={createGroup.isPending || updateGroup.isPending}
        onOk={() => form.submit()}
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ status: 'active' }}
          onFinish={async (values) => {
            const input = {
              name: values.name,
              provider: values.provider,
              region: values.region,
              description: values.description,
              status: values.status,
              labels: parseLabels(values.labels),
              models: String(values.models ?? '')
                .split(',')
                .map((model: string) => model.trim())
                .filter(Boolean),
              rate_multiplier:
                values.rate_multiplier != null && values.rate_multiplier !== ''
                  ? String(values.rate_multiplier)
                  : undefined,
            }
            if (editingId) {
              await updateGroup.mutateAsync({ id: editingId, input })
            } else {
              await createGroup.mutateAsync(input)
            }
            close()
          }}
        >
          <Form.Item name="name" label={t('platformGroups.form.name', '名称')} rules={[{ required: true }]}>
            <Input placeholder={t('platformGroups.form.namePlaceholder', '例如：主力池 / 备用池')} />
          </Form.Item>
          <Form.Item
            name="rate_multiplier"
            label={t('platformGroups.form.multiplier', '售价倍率')}
            extra={t(
              'platformGroups.form.multiplierExtra',
              '基于基准价目表的售价倍数（如 1.25）；留空保持 1。创建上游不填价格时按 基准价×汇率×倍率 自动定价。',
            )}
          >
            <InputNumber min={0.0001} max={100} step={0.05} style={{ width: '100%' }} placeholder="1" />
          </Form.Item>
          <Form.Item name="models" label={t('platformGroups.form.models', '模型（逗号分隔）')}>
            <Input placeholder="gpt-4o-mini, claude-3-5-sonnet" />
          </Form.Item>
          <Form.Item name="provider" label={t('platformGroups.form.provider', '供应商说明')}>
            <Input />
          </Form.Item>
          <Form.Item name="region" label={t('platformGroups.form.region', '区域')}>
            <Input placeholder={t('platformGroups.form.regionPlaceholder', '可选，例如 cn 或 us')} />
          </Form.Item>
          <Form.Item name="description" label={t('platformGroups.form.description', '描述')}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="labels" label={t('platformGroups.form.labels', '标签（key=value，逗号分隔）')}>
            <Input placeholder="tier=premium, owner=ops" />
          </Form.Item>
          {editingId ? (
            <Form.Item name="status" label={t('platformGroups.form.status', '状态')}>
              <Select
                options={[
                  { value: 'active', label: t('platformGroups.status.active', '启用') },
                  { value: 'maintenance', label: t('platformGroups.status.maintenance', '维护中') },
                ]}
              />
            </Form.Item>
          ) : null}
        </Form>
      </Modal>
    </PageContainer>
  )
}
