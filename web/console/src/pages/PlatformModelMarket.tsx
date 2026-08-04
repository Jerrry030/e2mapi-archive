import { ShopOutlined } from '@ant-design/icons'
import { PageContainer, ProCard } from '@ant-design/pro-components'
import { Alert, Empty, Space, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { useQuery } from '@tanstack/react-query'
import { endpoints } from '../api/endpoints'
import type { ModelMarketPrice } from '../api/types'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

function formatPricePerMillion(micros?: number): string {
  if (!micros || micros <= 0) return '--'
  return `¥${(micros / 1_000_000).toFixed(4)}`
}

const RESOURCE_CLASS_LABELS: Record<string, string> = {
  economy: '低价池',
  stable: '稳定池',
}

export default function PlatformModelMarket() {
  useLocaleVersion()
  const market = useQuery({
    queryKey: ['platform', 'model-market'],
    queryFn: () => endpoints.getModelMarket(),
    refetchInterval: 60_000,
  })

  const columns: ColumnsType<ModelMarketPrice> = [
    {
      title: t('platformModelMarket.columns.model', '模型'),
      dataIndex: 'model',
      render: (model: string) => <Typography.Text code>{model}</Typography.Text>,
    },
    {
      title: t('platformModelMarket.columns.input', '输入价 / 百万 token'),
      dataIndex: 'input_micros_per_million',
      render: (_, row) => formatPricePerMillion(row.input_micros_per_million),
    },
    {
      title: t('platformModelMarket.columns.output', '输出价 / 百万 token'),
      dataIndex: 'output_micros_per_million',
      render: (_, row) => formatPricePerMillion(row.output_micros_per_million),
    },
    {
      title: t('platformModelMarket.columns.availability', '可用性'),
      dataIndex: 'available',
      render: (available: boolean) =>
        available ? (
          <Tag color="green">{t('platformModelMarket.available', '可用')}</Tag>
        ) : (
          <Tag color="orange">{t('platformModelMarket.unavailable', '暂不可用')}</Tag>
        ),
    },
  ]

  return (
    <PageContainer title={t('platformModelMarket.title', '模型市场')}>
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Alert
          type="info"
          showIcon
          icon={<ShopOutlined />}
          message={t('platformModelMarket.intro.message', '按分组展示当前可购买模型与到手结算价')}
          description={t(
            'platformModelMarket.intro.description',
            '价格为该分组内当前最优报价（CNY / 百万 token），结算以请求发起时的快照为准；在"平台分发"页为对应分组创建 Key 即可使用。',
          )}
        />
        {market.data?.length === 0 && !market.isLoading ? (
          <ProCard>
            <Empty description={t('platformModelMarket.empty', '平台暂未上架任何分组')} />
          </ProCard>
        ) : null}
        {(market.data ?? []).map((group) => (
          <ProCard
            key={group.group_id}
            title={
              <Space>
                {group.group_name}
                <Tag>{RESOURCE_CLASS_LABELS[group.resource_class] ?? group.resource_class}</Tag>
              </Space>
            }
            extra={group.description}
            loading={market.isLoading}
          >
            <Table<ModelMarketPrice>
              rowKey="model"
              size="small"
              pagination={false}
              columns={columns}
              dataSource={group.models}
            />
          </ProCard>
        ))}
      </Space>
    </PageContainer>
  )
}
