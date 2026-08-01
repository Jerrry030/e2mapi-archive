import { Alert, Card, Col, Progress, Row, Space, Statistic, Tag, Typography } from 'antd'
import type {
  UpstreamMarginBlockedReason,
  UpstreamMarginBrowserView,
  UpstreamMarginCostBucket,
  UpstreamMarginMoney,
} from '../../api/upstreamMargin'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

export function UpstreamMarginSummary({ view }: { view: UpstreamMarginBrowserView }) {
  useLocaleVersion()
  const coverage = percentage(view.attributableCoverage)
  const threshold = percentage(view.minimumAttributableCoverage)
  const blockers = marginBlockers(view)
  const bucketLabels: Record<UpstreamMarginCostBucket, string> = {
    exact: t('upstreamIntelligence.margin.bucketExact'),
    estimated: t('upstreamIntelligence.margin.bucketEstimated'),
    unknown: t('upstreamIntelligence.margin.bucketUnknown'),
    unattributed: t('upstreamIntelligence.margin.bucketUnattributed'),
    expired: t('upstreamIntelligence.margin.bucketExpired'),
  }
  const blockerLabels: Record<UpstreamMarginBlockedReason, string> = {
    no_cost_facts: t('upstreamIntelligence.margin.blockerNoFacts'),
    coverage_below_gate: t('upstreamIntelligence.margin.blockerCoverage'),
    revenue_unavailable: t('upstreamIntelligence.margin.blockerRevenue'),
    cross_currency_without_fx: t('upstreamIntelligence.margin.blockerCurrency'),
  }

  return (
    <Card title={t('upstreamIntelligence.margin.title')}>
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <Alert
          showIcon
          type="warning"
          message={t('upstreamIntelligence.margin.blockedTitle')}
          description={blockers.map((reason) => blockerLabels[reason]).join('；')}
        />
        <Row gutter={[16, 16]}>
          <Col xs={24} md={8}>
            <Statistic
              title={t('upstreamIntelligence.margin.totalFacts')}
              value={view.totalCostFactCount}
            />
          </Col>
          <Col xs={24} md={8}>
            <Statistic
              title={t('upstreamIntelligence.margin.attributableFacts')}
              value={view.attributableCostFactCount}
            />
          </Col>
          <Col xs={24} md={8}>
            <Statistic
              title={t('upstreamIntelligence.margin.uncoveredFacts')}
              value={view.uncoveredCostFactCount}
            />
          </Col>
        </Row>
        <div aria-label={t('upstreamIntelligence.margin.coverageAria')}>
          <Typography.Text strong>
            {t('upstreamIntelligence.margin.coverage', undefined, { coverage, threshold })}
          </Typography.Text>
          <Progress
            percent={coverage}
            status={view.coverageGatePassed ? 'success' : 'exception'}
            format={() => `${coverage}%`}
          />
        </div>
        <Row gutter={[16, 16]}>
          {view.costs.map((column) => (
            <Col xs={24} sm={12} xl={column.bucket === 'exact' ? 8 : 4} key={column.bucket}>
              <Card size="small" title={bucketLabels[column.bucket]}>
                <Statistic
                  title={t('upstreamIntelligence.margin.factCount')}
                  value={column.factCount}
                />
                {column.bucket === 'exact' ? (
                  <Typography.Text type="secondary">
                    {t('upstreamIntelligence.margin.exactComposition', undefined, {
                      exact: view.exactFactCount,
                      derived: view.derivedFactCount,
                    })}
                  </Typography.Text>
                ) : null}
                <div>
                  {column.amounts.length ? (
                    column.amounts.map((money) => (
                      <Tag key={money.currency}>{moneyText(money)}</Tag>
                    ))
                  ) : (
                    <Typography.Text type="secondary">
                      {t('upstreamIntelligence.margin.amountUnavailable')}
                    </Typography.Text>
                  )}
                </div>
              </Card>
            </Col>
          ))}
        </Row>
        {hasMultipleCurrencies(view) ? (
          <Alert
            type="info"
            showIcon
            message={t('upstreamIntelligence.margin.currenciesTitle')}
            description={t('upstreamIntelligence.margin.currenciesDescription')}
          />
        ) : null}
      </Space>
    </Card>
  )
}

function marginBlockers(view: UpstreamMarginBrowserView): UpstreamMarginBlockedReason[] {
  const reasons = [...view.blockedReasons]
  if (!view.coverageGatePassed && !reasons.includes('coverage_below_gate')) {
    reasons.push('coverage_below_gate')
  }
  // The browser-safe DTO intentionally carries no revenue. Even a server claim
  // cannot make this purchase-cost-only component state a margin amount.
  if (!reasons.includes('revenue_unavailable')) {
    reasons.push('revenue_unavailable')
  }
  return reasons
}

function percentage(value: string) {
  if (!/^(?:0|1|0\.[0-9]+)$/.test(value)) return 0
  const result = Number(value) * 100
  return Number.isFinite(result) ? Math.min(100, Math.max(0, result)) : 0
}

function moneyText(money: UpstreamMarginMoney) {
  return `${money.amount} ${money.currency}`
}

function hasMultipleCurrencies(view: UpstreamMarginBrowserView) {
  const currencies = new Set(
    view.costs.flatMap((column) => column.amounts.map((money) => money.currency)),
  )
  return currencies.size > 1
}
