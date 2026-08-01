import { Alert } from 'antd'
import type { IntelligenceOverview } from '../../api/upstreamIntelligence'
import { getLocale, t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

export function DataQualityBanner({ data }: { data?: IntelligenceOverview }) {
  useLocaleVersion()
  if (!data) return null
  const {
    failed_source_count: failed,
    stale_source_count: stale,
    expired_source_count: expired,
  } = data.metrics
  const unhealthy = failed + stale + expired
  if (!unhealthy) {
    return (
      <Alert
        type="success"
        showIcon
        message={t('upstreamIntelligence.qualityBanner.healthyTitle', undefined, {
          version: data.fact_version,
        })}
        description={t('upstreamIntelligence.qualityBanner.healthyDescription', undefined, {
          time: data.metrics.next_poll_at
            ? new Date(data.metrics.next_poll_at).toLocaleString(
                getLocale() === 'zh' ? 'zh-CN' : 'en-US',
              )
            : t('upstreamIntelligence.common.notScheduled'),
        })}
      />
    )
  }
  return (
    <Alert
      type={expired || failed ? 'warning' : 'info'}
      showIcon
      message={t('upstreamIntelligence.qualityBanner.unhealthyTitle', undefined, {
        count: unhealthy,
      })}
      description={t('upstreamIntelligence.qualityBanner.unhealthyDescription', undefined, {
        failed,
        stale,
        expired,
      })}
    />
  )
}
