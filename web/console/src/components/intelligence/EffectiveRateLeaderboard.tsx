import type { ProColumns } from '@ant-design/pro-components'
import { Button, Tag, Typography } from 'antd'
import type { IntelligenceRate } from '../../api/upstreamIntelligence'
import { LocalizedProTable as ProTable } from '../LocalizedProTable'
import { EvidenceBadge } from './EvidenceBadge'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

export function EffectiveRateLeaderboard({
  rates,
  loading,
  onEvidence,
}: {
  rates: IntelligenceRate[]
  loading?: boolean
  onEvidence: (id: string) => void
}) {
  useLocaleVersion()
  const columns: ProColumns<IntelligenceRate>[] = [
    {
      title: t('upstreamIntelligence.common.source'),
      render: (_, row) => row.source.display_name,
    },
    { title: t('upstreamIntelligence.common.model'), dataIndex: 'model_key', ellipsis: true },
    { title: t('upstreamIntelligence.common.group'), dataIndex: 'group_key', ellipsis: true },
    {
      title: t('upstreamIntelligence.common.dimension'),
      dataIndex: 'price_dimension',
      width: 110,
      render: (value) => t(`upstreamIntelligence.priceDimensions.${String(value)}`, String(value)),
    },
    {
      title: t('upstreamIntelligence.rates.effectiveMultiplier'),
      width: 120,
      render: (_, row) =>
        row.effective_multiplier ?? (
          <Typography.Text type="secondary">
            {t('upstreamIntelligence.common.unknown')}
          </Typography.Text>
        ),
    },
    {
      title: t('upstreamIntelligence.rates.effectiveCost'),
      width: 180,
      render: (_, row) =>
        row.effective_unit_cost === null ? (
          <Typography.Text type="secondary">
            {t('upstreamIntelligence.common.unknown')}
          </Typography.Text>
        ) : (
          `${row.effective_unit_cost} ${row.settlement_currency || ''} / ${row.per_tokens}`
        ),
    },
    {
      title: t('upstreamIntelligence.rates.comparable'),
      width: 120,
      render: (_, row) =>
        row.comparable ? (
          <Tag color="green">{t('upstreamIntelligence.rates.comparable')}</Tag>
        ) : (
          <Tag color="orange">
            {row.comparability_reason
              ? t(
                  `upstreamIntelligence.comparabilityReasons.${row.comparability_reason}`,
                  row.comparability_reason,
                )
              : t('upstreamIntelligence.rates.notComparable')}
          </Tag>
        ),
    },
    {
      title: t('upstreamIntelligence.common.evidence'),
      render: (_, row) => <EvidenceBadge evidence={row.evidence} />,
    },
    {
      title: t('upstreamIntelligence.common.actions'),
      valueType: 'option',
      render: (_, row) => (
        <Button type="link" size="small" onClick={() => onEvidence(row.observation_id)}>
          {t('upstreamIntelligence.common.viewEvidence')}
        </Button>
      ),
    },
  ]
  return (
    <div
      className="intelligence-scroll-region"
      role="region"
      aria-label={t('upstreamIntelligence.rates.regionLabel')}
      tabIndex={0}
    >
      <ProTable<IntelligenceRate>
        rowKey="observation_id"
        search={false}
        options={false}
        loading={loading}
        dataSource={rates}
        columns={columns}
        pagination={{ pageSize: 20 }}
        scroll={{ x: 'max-content' }}
      />
    </div>
  )
}
