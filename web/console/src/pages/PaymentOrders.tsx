import { useMemo, useState } from 'react'
import { EyeOutlined, ReloadOutlined, SearchOutlined, StopOutlined } from '@ant-design/icons'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  DatePicker,
  Descriptions,
  Drawer,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import { getStoredUser, isPlatformAdmin } from '../api/auth'
import { friendlyErrorMessage } from '../api/errors'
import {
  useCancelPaymentOrder,
  usePaymentOrder,
  usePaymentOrders,
  usePaymentProviders,
} from '../api/hooks'
import type {
  OperationAudit,
  PaymentOrder,
  PaymentOrderListParams,
  PaymentOrderStatus,
  PaymentOrderType,
} from '../api/types'
import { AbsoluteTime, EmptyTeach } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { UserSelect } from '../components/fields'
import { auditActionLabel, auditActorLabel, auditResultLabel, t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

const { RangePicker } = DatePicker

interface OrderFilters {
  keyword?: string
  status?: PaymentOrderStatus
  payment_type?: string
  provider_instance_id?: string
  order_type?: PaymentOrderType
  user_id?: number
  start_date?: string
  end_date?: string
}

const statusColors: Partial<Record<PaymentOrderStatus, string>> = {
  PENDING: 'processing',
  PAID: 'cyan',
  RECHARGING: 'blue',
  COMPLETED: 'success',
  EXPIRED: 'default',
  CANCELLED: 'default',
  FAILED: 'error',
  REFUND_REQUESTED: 'purple',
  REFUNDING: 'orange',
  REFUND_PENDING: 'orange',
  PARTIALLY_REFUNDED: 'gold',
  REFUNDED: 'purple',
  REFUND_FAILED: 'error',
}

const paymentStatuses: PaymentOrderStatus[] = [
  'PENDING',
  'PAID',
  'RECHARGING',
  'COMPLETED',
  'EXPIRED',
  'CANCELLED',
  'FAILED',
  'REFUND_REQUESTED',
  'REFUNDING',
  'REFUND_PENDING',
  'PARTIALLY_REFUNDED',
  'REFUNDED',
  'REFUND_FAILED',
]

const paymentTypes = [
  'alipay',
  'wxpay',
  'alipay_direct',
  'wxpay_direct',
  'stripe',
  'airwallex',
  'easypay',
  'card',
  'link',
]

function OrderStatusTag({ status }: { status: PaymentOrderStatus }) {
  return <Tag color={statusColors[status]}>{t(`payment.orders.statuses.${status}`, status)}</Tag>
}

function amount(value?: string, currency?: string) {
  return value ? `${currency || 'CNY'} ${value}` : '—'
}

function userLabel(order: PaymentOrder) {
  const name = order.user_name || order.user_email
  return name ? `${name} · #${order.user_id}` : `#${order.user_id}`
}

export default function PaymentOrders() {
  useLocaleVersion()
  const platformAdmin = isPlatformAdmin(getStoredUser())
  const [page, setPage] = useState(1)
  const [pageSize] = useState(20)
  const [keywordDraft, setKeywordDraft] = useState('')
  const [filters, setFilters] = useState<OrderFilters>({})
  const [selectedId, setSelectedId] = useState<string>()

  const params = useMemo<PaymentOrderListParams>(
    () => ({ page, page_size: pageSize, ...filters }),
    [filters, page, pageSize],
  )
  const orders = usePaymentOrders(params, platformAdmin)
  const detail = usePaymentOrder(selectedId)
  const providers = usePaymentProviders(platformAdmin)
  const cancel = useCancelPaymentOrder()
  const selectedOrder =
    detail.data?.order ?? orders.data?.items.find((item) => item.id === selectedId)

  const updateFilters = (next: Partial<OrderFilters>) => {
    setPage(1)
    setFilters((current) => ({ ...current, ...next }))
  }

  const updateDates = (dates: [Dayjs | null, Dayjs | null] | null) =>
    updateFilters({
      start_date: dates?.[0]?.format('YYYY-MM-DD'),
      end_date: dates?.[1]?.format('YYYY-MM-DD'),
    })

  const cancelOrder = async (id: string) => {
    try {
      await cancel.mutateAsync(id)
    } catch {
      // The mutation reports the API error; always refresh stale order state below.
    } finally {
      await orders.refetch()
      if (selectedId === id) await detail.refetch()
    }
  }

  if (!platformAdmin) {
    return (
      <PageContainer title={t('payment.orders.title')}>
        <Typography.Text type="secondary">{t('payment.orders.adminOnly')}</Typography.Text>
      </PageContainer>
    )
  }

  const columns: ProColumns<PaymentOrder>[] = [
    {
      title: t('payment.orders.columns.order'),
      dataIndex: 'out_trade_no',
      width: 230,
      render: (_, order) => (
        <Space direction="vertical" size={0}>
          <Typography.Text copyable={{ text: order.out_trade_no }}>
            {order.out_trade_no}
          </Typography.Text>
          <Typography.Text type="secondary" copyable={{ text: order.id }}>
            #{order.id}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('payment.orders.columns.user'),
      dataIndex: 'user_id',
      width: 210,
      ellipsis: true,
      render: (_, order) => userLabel(order),
    },
    {
      title: t('payment.orders.columns.amount'),
      dataIndex: 'pay_amount',
      width: 155,
      align: 'right',
      render: (_, order) => (
        <Space direction="vertical" size={0} style={{ textAlign: 'right' }}>
          <Typography.Text strong>{amount(order.pay_amount, order.currency)}</Typography.Text>
          {order.amount !== order.pay_amount ? (
            <Typography.Text type="secondary">
              {t('payment.orders.credited')}: {amount(order.amount, order.currency)}
            </Typography.Text>
          ) : null}
        </Space>
      ),
    },
    {
      title: t('payment.orders.columns.method'),
      dataIndex: 'payment_type',
      width: 145,
      render: (_, order) => (
        <Space direction="vertical" size={0}>
          <span>{t(`payment.methods.${order.payment_type}`, order.payment_type)}</span>
          <Typography.Text type="secondary">
            {order.provider_name || order.provider_key || '—'}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('payment.orders.columns.status'),
      dataIndex: 'status',
      width: 125,
      render: (_, order) => <OrderStatusTag status={order.status} />,
    },
    {
      title: t('payment.orders.columns.type'),
      dataIndex: 'order_type',
      width: 110,
      render: (value) => t(`payment.orders.types.${String(value)}`, String(value)),
    },
    {
      title: t('payment.orders.columns.createdAt'),
      dataIndex: 'created_at',
      width: 175,
      render: (value) => <AbsoluteTime value={String(value)} />,
    },
    {
      title: t('payment.orders.columns.actions'),
      valueType: 'option',
      fixed: 'right',
      width: 175,
      render: (_, order) => [
        <Button
          key="detail"
          type="link"
          size="small"
          icon={<EyeOutlined />}
          aria-label={t('payment.orders.actions.view')}
          onClick={() => setSelectedId(order.id)}
        >
          {t('payment.orders.actions.view')}
        </Button>,
        order.status === 'PENDING' && !order.payment_trade_no && !orders.isError ? (
          <Popconfirm
            key="cancel"
            title={t('payment.orders.cancel.title')}
            description={t('payment.orders.cancel.description')}
            okText={t('payment.orders.cancel.confirm')}
            cancelText={t('payment.orders.cancel.keep')}
            okButtonProps={{ danger: true, loading: cancel.isPending }}
            onConfirm={() => cancelOrder(order.id)}
          >
            <Button
              type="link"
              danger
              size="small"
              icon={<StopOutlined />}
              aria-label={t('payment.orders.actions.cancel')}
            >
              {t('payment.orders.actions.cancel')}
            </Button>
          </Popconfirm>
        ) : null,
      ],
    },
  ]

  const auditColumns: ColumnsType<OperationAudit> = [
    {
      title: t('payment.orders.audit.time'),
      dataIndex: 'created_at',
      width: 170,
      render: (value) => <AbsoluteTime value={String(value)} />,
    },
    {
      title: t('payment.orders.audit.action'),
      dataIndex: 'action',
      width: 160,
      render: (value) => auditActionLabel(String(value)),
    },
    {
      title: t('payment.orders.audit.actor'),
      width: 160,
      render: (_, item) => auditActorLabel(item.actor_type, item.actor_id),
    },
    {
      title: t('payment.orders.audit.result'),
      dataIndex: 'result',
      width: 100,
      render: (value) => <Tag>{auditResultLabel(String(value))}</Tag>,
    },
    {
      title: t('payment.orders.audit.error'),
      dataIndex: 'error_message',
      ellipsis: true,
      render: (value) => value || '—',
    },
  ]

  return (
    <PageContainer
      title={t('payment.orders.title')}
      subTitle={t('payment.orders.subtitle')}
      extra={[
        <Button
          key="refresh"
          icon={<ReloadOutlined />}
          loading={orders.isFetching}
          onClick={() => orders.refetch()}
        >
          {t('payment.orders.actions.refresh')}
        </Button>,
      ]}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('payment.orders.executionNotice')}
        description={t('payment.orders.executionDescription')}
      />

      {orders.isError ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t('payment.orders.loadError')}
          description={friendlyErrorMessage(orders.error)}
          action={
            <Button
              size="small"
              aria-label={t('payment.orders.actions.retry')}
              onClick={() => orders.refetch()}
            >
              {t('payment.orders.actions.retry')}
            </Button>
          }
        />
      ) : null}

      <ProCard bordered style={{ marginBottom: 16 }}>
        <Space wrap size={[8, 12]}>
          <Input.Search
            style={{ width: 280 }}
            value={keywordDraft}
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('payment.orders.filters.keyword')}
            enterButton={t('payment.orders.actions.search')}
            onChange={(event) => setKeywordDraft(event.target.value)}
            onSearch={(keyword) => updateFilters({ keyword: keyword.trim() || undefined })}
          />
          <UserSelect
            value={filters.user_id}
            onChange={(user_id) => updateFilters({ user_id })}
            placeholder={t('payment.orders.filters.user')}
          />
          <Select
            aria-label={t('payment.orders.filters.status')}
            style={{ width: 155 }}
            allowClear
            placeholder={t('payment.orders.filters.status')}
            value={filters.status}
            options={paymentStatuses.map((status) => ({
              value: status,
              label: t(`payment.orders.statuses.${status}`, status),
            }))}
            onChange={(status) => updateFilters({ status })}
          />
          <Select
            aria-label={t('payment.orders.filters.method')}
            style={{ width: 155 }}
            allowClear
            placeholder={t('payment.orders.filters.method')}
            value={filters.payment_type}
            options={paymentTypes.map((method) => ({
              value: method,
              label: t(`payment.methods.${method}`, method),
            }))}
            onChange={(payment_type) => updateFilters({ payment_type })}
          />
          <Select
            aria-label={t('payment.orders.filters.provider')}
            style={{ width: 190 }}
            allowClear
            loading={providers.isLoading}
            disabled={providers.isError}
            placeholder={t('payment.orders.filters.provider')}
            value={filters.provider_instance_id}
            options={(providers.data ?? []).map((provider) => ({
              value: provider.id,
              label: provider.name,
            }))}
            onChange={(provider_instance_id) => updateFilters({ provider_instance_id })}
          />
          <Select
            aria-label={t('payment.orders.filters.type')}
            style={{ width: 145 }}
            allowClear
            placeholder={t('payment.orders.filters.type')}
            value={filters.order_type}
            options={(['balance', 'subscription'] as PaymentOrderType[]).map((type) => ({
              value: type,
              label: t(`payment.orders.types.${type}`, type),
            }))}
            onChange={(order_type) => updateFilters({ order_type })}
          />
          <RangePicker
            aria-label={t('payment.orders.filters.date')}
            onChange={(dates) => updateDates(dates as [Dayjs | null, Dayjs | null] | null)}
          />
        </Space>
      </ProCard>

      <ProTable<PaymentOrder>
        rowKey="id"
        search={false}
        options={false}
        loading={orders.isLoading || orders.isFetching}
        columns={columns}
        dataSource={orders.data?.items ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{
          current: page,
          pageSize,
          total: orders.data?.total ?? 0,
          onChange: setPage,
        }}
        locale={{
          emptyText: (
            <EmptyTeach
              title={
                filters.keyword || filters.status || filters.payment_type || filters.order_type
                  ? t('payment.orders.emptyFiltered')
                  : t('payment.orders.empty')
              }
            />
          ),
        }}
      />

      <Drawer
        width={760}
        open={Boolean(selectedId)}
        title={t('payment.orders.detail.title')}
        onClose={() => setSelectedId(undefined)}
        extra={
          selectedOrder?.status === 'PENDING' &&
          !selectedOrder.payment_trade_no &&
          !orders.isError &&
          !detail.isError ? (
            <Popconfirm
              title={t('payment.orders.cancel.title')}
              description={t('payment.orders.cancel.description')}
              okText={t('payment.orders.cancel.confirm')}
              cancelText={t('payment.orders.cancel.keep')}
              okButtonProps={{ danger: true, loading: cancel.isPending }}
              onConfirm={() => cancelOrder(selectedOrder.id)}
            >
              <Button
                danger
                icon={<StopOutlined />}
                aria-label={t('payment.orders.actions.cancel')}
              >
                {t('payment.orders.actions.cancel')}
              </Button>
            </Popconfirm>
          ) : null
        }
      >
        {detail.isError ? (
          <Alert
            type="error"
            showIcon
            message={t('payment.orders.detail.loadError')}
            description={friendlyErrorMessage(detail.error)}
            action={
              <Button
                size="small"
                aria-label={t('payment.orders.actions.retry')}
                onClick={() => detail.refetch()}
              >
                {t('payment.orders.actions.retry')}
              </Button>
            }
          />
        ) : selectedOrder ? (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {selectedOrder.failed_reason ? (
              <Alert type="error" showIcon message={selectedOrder.failed_reason} />
            ) : null}
            <Descriptions
              bordered
              size="small"
              column={2}
              items={[
                {
                  label: t('payment.orders.detail.orderNo'),
                  span: 2,
                  children: (
                    <Typography.Text copyable>{selectedOrder.out_trade_no}</Typography.Text>
                  ),
                },
                {
                  label: t('payment.orders.detail.id'),
                  children: <Typography.Text copyable>{selectedOrder.id}</Typography.Text>,
                },
                {
                  label: t('payment.orders.columns.status'),
                  children: <OrderStatusTag status={selectedOrder.status} />,
                },
                {
                  label: t('payment.orders.columns.user'),
                  span: 2,
                  children: userLabel(selectedOrder),
                },
                {
                  label: t('payment.orders.detail.payAmount'),
                  children: amount(selectedOrder.pay_amount, selectedOrder.currency),
                },
                {
                  label: t('payment.orders.credited'),
                  children: amount(selectedOrder.amount, selectedOrder.currency),
                },
                {
                  label: t('payment.orders.detail.feeRate'),
                  children: `${selectedOrder.fee_rate || '0'}%`,
                },
                {
                  label: t('payment.orders.columns.method'),
                  children: t(
                    `payment.methods.${selectedOrder.payment_type}`,
                    selectedOrder.payment_type,
                  ),
                },
                {
                  label: t('payment.orders.detail.provider'),
                  children:
                    selectedOrder.provider_name ||
                    selectedOrder.provider_key ||
                    selectedOrder.provider_instance_id ||
                    '—',
                },
                {
                  label: t('payment.orders.columns.type'),
                  children: t(
                    `payment.orders.types.${selectedOrder.order_type}`,
                    selectedOrder.order_type,
                  ),
                },
                {
                  label: t('payment.orders.detail.providerTradeNo'),
                  span: 2,
                  children: selectedOrder.payment_trade_no ? (
                    <Typography.Text copyable>{selectedOrder.payment_trade_no}</Typography.Text>
                  ) : (
                    '—'
                  ),
                },
                {
                  label: t('payment.orders.columns.createdAt'),
                  children: <AbsoluteTime value={selectedOrder.created_at} />,
                },
                {
                  label: t('payment.orders.detail.expiresAt'),
                  children: <AbsoluteTime value={selectedOrder.expires_at} />,
                },
                {
                  label: t('payment.orders.detail.paidAt'),
                  children: <AbsoluteTime value={selectedOrder.paid_at} />,
                },
                {
                  label: t('payment.orders.detail.completedAt'),
                  children: <AbsoluteTime value={selectedOrder.completed_at} />,
                },
                {
                  label: t('payment.orders.detail.refundAmount'),
                  children: amount(selectedOrder.refund_amount, selectedOrder.currency),
                },
                {
                  label: t('payment.orders.detail.refundReason'),
                  children:
                    selectedOrder.refund_reason || selectedOrder.refund_request_reason || '—',
                },
              ]}
            />

            <ProCard title={t('payment.orders.audit.title')} bordered>
              <Table<OperationAudit>
                rowKey="id"
                size="small"
                loading={detail.isLoading}
                columns={auditColumns}
                dataSource={detail.data?.audit_logs ?? []}
                pagination={false}
                scroll={{ x: 'max-content' }}
                locale={{ emptyText: t('payment.orders.audit.empty') }}
              />
            </ProCard>
          </Space>
        ) : null}
      </Drawer>
    </PageContainer>
  )
}
