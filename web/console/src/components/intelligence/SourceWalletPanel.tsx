import { ProCard } from '@ant-design/pro-components'
import { Button, Empty, Space, Tag, Typography } from 'antd'
import type { IntelligenceWallet } from '../../api/upstreamIntelligence'
import { EvidenceBadge } from './EvidenceBadge'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

function walletValue(wallet: IntelligenceWallet) {
  if (wallet.balance_amount === null) return t('upstreamIntelligence.common.unknown')
  if (wallet.unit_kind === 'fiat')
    return `${wallet.balance_amount} ${wallet.currency || wallet.source.currency || ''}`
  if (wallet.unit_kind === 'credit')
    return t('upstreamIntelligence.wallets.credits', undefined, { amount: wallet.balance_amount })
  return wallet.balance_amount
}

export function SourceWalletPanel({
  wallets,
  onEvidence,
}: {
  wallets: IntelligenceWallet[]
  onEvidence: (id: string) => void
}) {
  useLocaleVersion()
  if (!wallets.length) return <Empty description={t('upstreamIntelligence.wallets.empty')} />
  return (
    <div className="intelligence-wallet-grid">
      {wallets.map((wallet) => (
        <ProCard key={wallet.observation_id} bordered>
          <Space direction="vertical" size={8} style={{ width: '100%' }}>
            <Space wrap>
              <Typography.Text strong>{wallet.source.display_name}</Typography.Text>
              <Tag>{wallet.source.provider}</Tag>
            </Space>
            <Typography.Title level={3} style={{ margin: 0 }}>
              {walletValue(wallet)}
            </Typography.Title>
            <EvidenceBadge evidence={wallet.evidence} />
            <Button
              type="link"
              size="small"
              className="intelligence-inline-link"
              onClick={() => onEvidence(wallet.observation_id)}
            >
              {t('upstreamIntelligence.common.viewEvidence')}
            </Button>
          </Space>
        </ProCard>
      ))}
    </div>
  )
}
