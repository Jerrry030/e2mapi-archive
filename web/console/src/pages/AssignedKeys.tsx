import { PageContainer, ProTable } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Tag, Typography } from 'antd'
import { useAssignedUpstreamKeys } from '../api/hooks'
import { RelativeTime } from '../components/common'
import type { AssignedUpstreamKey } from '../api/types'
import { deliveryKeyProofView } from './deliveryKeyProofView'

export default function AssignedKeys() {
  const keys = useAssignedUpstreamKeys()

  const columns: ProColumns<AssignedUpstreamKey>[] = [
    { title: '上游', dataIndex: 'display_name', ellipsis: true },
    {
      title: '提供方',
      dataIndex: 'provider',
      width: 140,
      render: (_, item) => item.provider || '-',
    },
    {
      title: 'Key 标识',
      dataIndex: 'masked_value',
      width: 220,
      render: (_, item) => <Typography.Text code>{item.masked_value}</Typography.Text>,
    },
    {
      title: '版本',
      dataIndex: 'key_version',
      width: 90,
      render: (_, item) => `v${item.key_version}`,
    },
    {
      title: '本地绑定校验',
      width: 160,
      render: (_, item) => {
        const proof = deliveryKeyProofView(item.proof_status)
        return <Tag color={proof.color}>{proof.label}</Tag>
      },
    },
    {
      title: '最近校验',
      width: 150,
      render: (_, item) => <RelativeTime value={item.proof_checked_at} />,
    },
    {
      title: '分配时间',
      dataIndex: 'allocated_at',
      width: 150,
      render: (_, item) => <RelativeTime value={item.allocated_at} />,
    },
  ]

  return (
    <PageContainer title="已交付资源" subTitle="平台托管 Key 与本地安装证明">
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="平台分配的独占 Key 会加密安装到 Connector，并通过本地绑定证明完成验收。为保证统一余额、可信计量和防透支，控制台不提供上游 Key 明文导出。"
      />
      <ProTable<AssignedUpstreamKey>
        rowKey="id"
        search={false}
        loading={keys.isLoading}
        dataSource={keys.data ?? []}
        columns={columns}
        options={{ reload: () => keys.refetch() }}
        locale={{ emptyText: '当前还没有已分配的资源' }}
      />
    </PageContainer>
  )
}
