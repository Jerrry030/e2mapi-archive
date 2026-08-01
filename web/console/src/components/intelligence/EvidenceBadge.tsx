import { Space, Tag, Tooltip } from 'antd'
import type {
  IntelligenceAccuracy,
  IntelligenceCoverage,
  IntelligenceEvidence,
  IntelligenceFreshness,
} from '../../api/upstreamIntelligence'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

export function EvidenceBadge({ evidence }: { evidence: IntelligenceEvidence }) {
  useLocaleVersion()
  const explanation = [
    evidence.reason_code
      ? t(`upstreamIntelligence.evidence.reasonCodes.${evidence.reason_code}`, evidence.reason_code)
      : '',
    evidence.missing_fields.length
      ? t('upstreamIntelligence.evidence.missing', undefined, {
          fields: evidence.missing_fields
            .map((field) => t(`upstreamIntelligence.evidence.missingFields.${field}`, field))
            .join('、'),
        })
      : '',
    evidence.confidence
      ? t('upstreamIntelligence.evidence.confidence', undefined, {
          value: evidence.confidence,
        })
      : '',
  ]
    .filter(Boolean)
    .join('；')
  return (
    <Tooltip title={explanation || t('upstreamIntelligence.evidence.complete')}>
      <Space size={4} wrap>
        <Tag
          color={
            evidence.accuracy === 'exact'
              ? 'green'
              : evidence.accuracy === 'unknown'
                ? 'default'
                : 'blue'
          }
        >
          {t(`upstreamIntelligence.accuracy.${evidence.accuracy}`, evidence.accuracy)}
        </Tag>
        <Tag color={evidence.coverage === 'complete' ? 'green' : 'orange'}>
          {t(`upstreamIntelligence.coverage.${evidence.coverage}`, evidence.coverage)}
        </Tag>
        <Tag
          color={
            evidence.freshness === 'current'
              ? 'green'
              : evidence.freshness === 'stale'
                ? 'orange'
                : 'red'
          }
        >
          {t(`upstreamIntelligence.freshness.${evidence.freshness}`, evidence.freshness)}
        </Tag>
      </Space>
    </Tooltip>
  )
}
