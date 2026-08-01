import { Alert, Descriptions, Drawer, Skeleton, Space, Typography } from 'antd'
import { useIntelligenceEvidence } from '../../api/upstreamIntelligenceHooks'
import { EvidenceBadge } from './EvidenceBadge'
import { getLocale, t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

export function EvidenceDrawer({
  userId,
  evidenceId,
  onClose,
}: {
  userId?: number
  evidenceId?: string
  onClose: () => void
}) {
  useLocaleVersion()
  const query = useIntelligenceEvidence(userId, evidenceId)
  const data = query.data
  const fact = data?.wallet ?? data?.offer
  const formatDate = (value: string) =>
    new Date(value).toLocaleString(getLocale() === 'zh' ? 'zh-CN' : 'en-US')
  return (
    <Drawer
      title={t('upstreamIntelligence.evidence.drawerTitle')}
      open={Boolean(evidenceId)}
      width={560}
      onClose={onClose}
    >
      {query.isLoading ? <Skeleton active /> : null}
      {query.error ? (
        <Alert type="error" showIcon message={t('upstreamIntelligence.evidence.loadError')} />
      ) : null}
      {data ? (
        <Space direction="vertical" size="large" style={{ width: '100%' }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t('upstreamIntelligence.evidence.id')}>
              {data.id}
            </Descriptions.Item>
            <Descriptions.Item label={t('upstreamIntelligence.evidence.type')}>
              {t(`upstreamIntelligence.evidence.kinds.${data.kind}`, data.kind)}
            </Descriptions.Item>
            <Descriptions.Item label={t('upstreamIntelligence.common.source')}>
              {data.source.display_name}
            </Descriptions.Item>
            <Descriptions.Item label={t('upstreamIntelligence.evidence.factVersion')}>
              v{data.fact_version}
            </Descriptions.Item>
            <Descriptions.Item label={t('upstreamIntelligence.common.generatedAt')}>
              {formatDate(data.generated_at)}
            </Descriptions.Item>
            <Descriptions.Item label={t('upstreamIntelligence.evidence.collectionRun')}>
              {data.run
                ? `${data.run.id} · ${t(
                    `upstreamIntelligence.evidence.runStatuses.${data.run.status}`,
                    data.run.status,
                  )} · ${t('upstreamIntelligence.common.facts', undefined, { count: data.run.fact_count })}`
                : t('upstreamIntelligence.evidence.noUniqueRun')}
            </Descriptions.Item>
          </Descriptions>
          {fact ? <EvidenceBadge evidence={fact.evidence} /> : null}
          {data.offer ? (
            <Descriptions
              column={1}
              size="small"
              bordered
              title={t('upstreamIntelligence.evidence.priceFormula')}
            >
              <Descriptions.Item label={t('upstreamIntelligence.common.model')}>
                {data.offer.model_key}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.common.group')}>
                {data.offer.group_key}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.publishedPrice')}>
                {data.offer.published_unit_price ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.groupMultiplier')}>
                {data.offer.group_multiplier ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.rechargeYield')}>
                {data.offer.recharge_yield ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.effectiveMultiplier')}>
                {data.offer.effective_multiplier ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.effectiveUnitCost')}>
                {data.offer.effective_unit_cost ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.formulaVersion')}>
                {data.offer.formula_version || t('upstreamIntelligence.common.notCalculated')}
              </Descriptions.Item>
            </Descriptions>
          ) : null}
          {data.change ? (
            <Descriptions
              column={1}
              size="small"
              bordered
              title={t('upstreamIntelligence.evidence.changeEvidence')}
            >
              <Descriptions.Item label={t('upstreamIntelligence.evidence.event')}>
                {t(`upstreamIntelligence.events.${data.change.event_type}`, data.change.event_type)}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.confirmedAt')}>
                {formatDate(data.change.confirmed_at)}
              </Descriptions.Item>
              <Descriptions.Item label={t('upstreamIntelligence.evidence.changeAmount')}>
                {data.change.percentage_change ?? t('upstreamIntelligence.common.unknown')}
              </Descriptions.Item>
            </Descriptions>
          ) : null}
          <Typography.Text type="secondary">
            {t('upstreamIntelligence.evidence.privacyNotice')}
          </Typography.Text>
        </Space>
      ) : null}
    </Drawer>
  )
}
