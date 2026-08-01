import { PageContainer } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Badge } from 'antd'
import { useCapabilities } from '../api/hooks'
import { KindTag, ModeTag, RiskLevelTag } from '../components/tags'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type { AdapterCapability } from '../api/types'
import { capabilityDescriptionLabel, capabilityNameLabel } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

export default function Capabilities() {
  useLocaleVersion()
  const { data, isLoading, refetch } = useCapabilities()

  const columns: ProColumns<AdapterCapability>[] = [
    {
      title: '网关',
      dataIndex: 'system',
      width: 110,
      render: (_, r) => <KindTag kind={r.system} />,
      filters: true,
      onFilter: true,
      valueEnum: {
        sub2api: { text: 'sub2api' },
        newapi: { text: 'new-api' },
        cpa: { text: 'CPA' },
      },
    },
    {
      title: '能力',
      dataIndex: 'name',
      width: 180,
      ellipsis: true,
      render: (_, record) => capabilityNameLabel(record.name),
    },
    { title: '模式', dataIndex: 'mode', width: 110, render: (_, r) => <ModeTag mode={r.mode} /> },
    {
      title: '风险等级',
      dataIndex: 'risk_level',
      width: 110,
      render: (_, r) => <RiskLevelTag level={r.risk_level} />,
    },
    {
      title: '支持',
      dataIndex: 'supported',
      width: 90,
      render: (_, r) =>
        r.supported ? <Badge status="success" text="是" /> : <Badge status="default" text="否" />,
    },
    {
      title: '说明',
      dataIndex: 'description',
      width: 260,
      ellipsis: true,
      render: (_, record) =>
        record.description ? capabilityDescriptionLabel(record.description) : '—',
    },
  ]

  return (
    <PageContainer title="网关能力" subTitle="以 Connector v2 和 Core 真实入口为准">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message="仅展示可直接执行的网关原子能力；编排、批量封装和生命周期流程不在此列。"
      />
      <ProTable<AdapterCapability>
        rowKey={(r) => `${r.system}-${r.name}-${r.mode}`}
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        rowClassName={(r) => (r.supported ? '' : 'e2m-row-disabled')}
      />
    </PageContainer>
  )
}
