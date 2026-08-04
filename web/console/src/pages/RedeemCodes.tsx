import { useMemo, useState } from 'react'
import { CopyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
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
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { endpoints } from '../api/endpoints'
import { friendlyErrorMessage } from '../api/errors'
import type { RedeemCode, RedeemCodeListParams, RedeemCodeStatus } from '../api/types'
import { AbsoluteTime } from '../components/common'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

const STATUS_COLORS: Record<RedeemCodeStatus, string> = {
  unused: 'green',
  used: 'blue',
  disabled: 'default',
  expired: 'orange',
}

function formatMicros(micros: number): string {
  return (micros / 1_000_000).toFixed(2)
}

export default function RedeemCodes() {
  useLocaleVersion()
  const { message } = App.useApp()
  const queryClient = useQueryClient()
  const [filters, setFilters] = useState<RedeemCodeListParams>({ page: 1, page_size: 20 })
  const [generateOpen, setGenerateOpen] = useState(false)
  const [generatedCodes, setGeneratedCodes] = useState<string[]>([])
  const [form] = Form.useForm()

  const list = useQuery({
    queryKey: ['admin', 'redeem-codes', filters],
    queryFn: () => endpoints.listRedeemCodes(filters),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['admin', 'redeem-codes'] })

  const generate = useMutation({
    mutationFn: endpoints.generateRedeemCodes,
    onSuccess: (result) => {
      invalidate()
      setGenerateOpen(false)
      form.resetFields()
      setGeneratedCodes(result.codes)
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })

  const disable = useMutation({
    mutationFn: endpoints.disableRedeemCode,
    onSuccess: () => {
      invalidate()
      message.success(t('redeemCodes.messages.disabled', '兑换码已停用'))
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })

  const remove = useMutation({
    mutationFn: endpoints.deleteRedeemCode,
    onSuccess: () => {
      invalidate()
      message.success(t('redeemCodes.messages.deleted', '兑换码已删除'))
    },
    onError: (error) => message.error(friendlyErrorMessage(error)),
  })

  const copyGenerated = async () => {
    await navigator.clipboard.writeText(generatedCodes.join('\n'))
    message.success(t('redeemCodes.messages.copied', '已复制全部兑换码'))
  }

  const columns: ColumnsType<RedeemCode> = useMemo(
    () => [
      {
        title: t('redeemCodes.columns.code', '兑换码'),
        dataIndex: 'code_prefix',
        render: (prefix: string) => <Typography.Text code>{prefix}-****</Typography.Text>,
      },
      {
        title: t('redeemCodes.columns.type', '类型'),
        dataIndex: 'type',
        render: (value: string) =>
          value === 'balance'
            ? t('redeemCodes.typeBalance', '余额')
            : t('redeemCodes.typeInvitation', '邀请'),
      },
      {
        title: t('redeemCodes.columns.amount', '面额'),
        dataIndex: 'amount_micros',
        render: (micros: number, row) =>
          row.type === 'balance' ? `¥${formatMicros(micros)}` : '--',
      },
      {
        title: t('redeemCodes.columns.status', '状态'),
        dataIndex: 'status',
        render: (status: RedeemCodeStatus) => (
          <Tag color={STATUS_COLORS[status]}>{status}</Tag>
        ),
      },
      { title: t('redeemCodes.columns.batch', '批次'), dataIndex: 'batch_id', ellipsis: true },
      {
        title: t('redeemCodes.columns.usedBy', '使用人'),
        dataIndex: 'used_by',
        render: (value?: number) => (value ? `#${value}` : '--'),
      },
      {
        title: t('redeemCodes.columns.createdAt', '创建时间'),
        dataIndex: 'created_at',
        render: (value: string) => <AbsoluteTime value={value} />,
      },
      {
        title: t('redeemCodes.columns.actions', '操作'),
        key: 'actions',
        render: (_, row) => (
          <Space>
            <Button
              size="small"
              disabled={row.status !== 'unused'}
              onClick={() => disable.mutate(row.id)}
            >
              {t('redeemCodes.actions.disable', '停用')}
            </Button>
            <Popconfirm
              title={t('redeemCodes.actions.deleteConfirm', '删除后不可恢复，确认删除？')}
              disabled={row.status === 'used'}
              onConfirm={() => remove.mutate(row.id)}
            >
              <Button size="small" danger disabled={row.status === 'used'}>
                {t('redeemCodes.actions.delete', '删除')}
              </Button>
            </Popconfirm>
          </Space>
        ),
      },
    ],
    [disable, remove],
  )

  return (
    <PageContainer
      title={t('redeemCodes.title', '兑换码管理')}
      extra={[
        <Button key="refresh" icon={<ReloadOutlined />} onClick={() => list.refetch()}>
          {t('redeemCodes.refresh', '刷新')}
        </Button>,
        <Button
          key="generate"
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setGenerateOpen(true)}
        >
          {t('redeemCodes.generate', '生成兑换码')}
        </Button>,
      ]}
    >
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Alert
          type="info"
          showIcon
          message={t('redeemCodes.intro', '兑换码明文只在生成时展示一次，数据库仅保存哈希；请生成后立即复制保存。')}
        />
        <ProCard>
          <Space style={{ marginBottom: 16 }} wrap>
            <Select
              allowClear
              style={{ width: 140 }}
              placeholder={t('redeemCodes.filters.type', '类型')}
              value={filters.type}
              onChange={(value) => setFilters((prev) => ({ ...prev, type: value, page: 1 }))}
              options={[
                { label: t('redeemCodes.typeBalance', '余额'), value: 'balance' },
                { label: t('redeemCodes.typeInvitation', '邀请'), value: 'invitation' },
              ]}
            />
            <Select
              allowClear
              style={{ width: 140 }}
              placeholder={t('redeemCodes.filters.status', '状态')}
              value={filters.status}
              onChange={(value) => setFilters((prev) => ({ ...prev, status: value, page: 1 }))}
              options={(['unused', 'used', 'disabled', 'expired'] as const).map((status) => ({
                label: status,
                value: status,
              }))}
            />
            <Input.Search
              allowClear
              style={{ width: 260 }}
              placeholder={t('redeemCodes.filters.batch', '按批次筛选')}
              onSearch={(value) =>
                setFilters((prev) => ({ ...prev, batch_id: value || undefined, page: 1 }))
              }
            />
          </Space>
          <Table<RedeemCode>
            rowKey="id"
            size="small"
            loading={list.isLoading}
            columns={columns}
            dataSource={list.data?.items ?? []}
            pagination={{
              current: list.data?.page ?? 1,
              pageSize: list.data?.page_size ?? 20,
              total: list.data?.total ?? 0,
              onChange: (page, pageSize) =>
                setFilters((prev) => ({ ...prev, page, page_size: pageSize })),
            }}
          />
        </ProCard>
      </Space>

      <Modal
        title={t('redeemCodes.generateTitle', '生成兑换码')}
        open={generateOpen}
        confirmLoading={generate.isPending}
        onCancel={() => setGenerateOpen(false)}
        onOk={() => {
          form
            .validateFields()
            .then((values) =>
              generate.mutate({
                type: values.type,
                count: values.count,
                amount: values.type === 'balance' ? Number(values.amount).toFixed(2) : undefined,
                currency: 'CNY',
                notes: values.notes || undefined,
              }),
            )
            .catch(() => undefined)
        }}
      >
        <Form form={form} layout="vertical" initialValues={{ type: 'balance', count: 10 }}>
          <Form.Item name="type" label={t('redeemCodes.form.type', '类型')} rules={[{ required: true }]}>
            <Select
              options={[
                { label: t('redeemCodes.typeBalance', '余额'), value: 'balance' },
                { label: t('redeemCodes.typeInvitation', '邀请'), value: 'invitation' },
              ]}
            />
          </Form.Item>
          <Form.Item name="count" label={t('redeemCodes.form.count', '数量（1–1000）')} rules={[{ required: true }]}>
            <InputNumber min={1} max={1000} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(prev, next) => prev.type !== next.type}>
            {({ getFieldValue }) =>
              getFieldValue('type') === 'balance' ? (
                <Form.Item
                  name="amount"
                  label={t('redeemCodes.form.amount', '单张面额（CNY）')}
                  rules={[{ required: true }]}
                >
                  <InputNumber min={0.01} precision={2} prefix="¥" style={{ width: '100%' }} />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item name="notes" label={t('redeemCodes.form.notes', '备注')}>
            <Input maxLength={200} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={t('redeemCodes.generatedTitle', '兑换码已生成（仅展示一次）')}
        open={generatedCodes.length > 0}
        onCancel={() => setGeneratedCodes([])}
        footer={[
          <Button key="copy" type="primary" icon={<CopyOutlined />} onClick={copyGenerated}>
            {t('redeemCodes.copyAll', '复制全部')}
          </Button>,
          <Button key="close" onClick={() => setGeneratedCodes([])}>
            {t('redeemCodes.close', '我已保存，关闭')}
          </Button>,
        ]}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('redeemCodes.generatedWarning', '关闭后将无法再次查看明文，请立即复制保存。')}
        />
        <Input.TextArea
          readOnly
          rows={Math.min(10, generatedCodes.length)}
          value={generatedCodes.join('\n')}
        />
      </Modal>
    </PageContainer>
  )
}
