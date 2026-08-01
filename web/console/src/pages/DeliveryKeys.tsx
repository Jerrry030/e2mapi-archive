import { useMemo, useState } from 'react'
import { PageContainer, ProFormText } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Modal, Space, Tag, Typography } from 'antd'
import { KeyOutlined } from '@ant-design/icons'
import {
  useUpsertUpstreamDeliveryKey,
  useUpstreamChannels,
  useUpstreamKeyDeliveries,
} from '../api/hooks'
import { RelativeTime } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type { UpstreamChannel } from '../api/types'
import { deliveryKeyProofView } from './deliveryKeyProofView'

export default function DeliveryKeys() {
  const channels = useUpstreamChannels()
  const deliveries = useUpstreamKeyDeliveries()
  const upsert = useUpsertUpstreamDeliveryKey()
  const [selected, setSelected] = useState<UpstreamChannel | null>(null)
  const [value, setValue] = useState('')

  const deliveryByChannel = useMemo(
    () => new Map((deliveries.data ?? []).map((item) => [item.channel_id, item])),
    [deliveries.data],
  )
  const managedChannels = (channels.data ?? []).filter(
    (channel) => channel.account_ownership !== 'owner_provided',
  )

  const columns: ProColumns<UpstreamChannel>[] = [
    { title: '上游账号', dataIndex: 'display_name', ellipsis: true },
    {
      title: '提供方',
      dataIndex: 'provider',
      width: 140,
      render: (_, item) => item.provider || '-',
    },
    {
      title: '交付状态',
      width: 180,
      render: (_, item) => {
        const delivery = deliveryByChannel.get(item.id)
        return delivery ? (
          <Typography.Text code>{delivery.masked_value}</Typography.Text>
        ) : (
          <Tag>未配置</Tag>
        )
      },
    },
    {
      title: '本地绑定校验',
      width: 160,
      render: (_, item) => {
        const proof = deliveryKeyProofView(deliveryByChannel.get(item.id)?.proof_status)
        return <Tag color={proof.color}>{proof.label}</Tag>
      },
    },
    {
      title: '最近校验',
      width: 150,
      render: (_, item) => (
        <RelativeTime value={deliveryByChannel.get(item.id)?.proof_checked_at} />
      ),
    },
    {
      title: '更新时间',
      width: 150,
      render: (_, item) => <RelativeTime value={deliveryByChannel.get(item.id)?.updated_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 120,
      render: (_, item) => {
        const configured = deliveryByChannel.has(item.id)
        return (
          <Button
            type="text"
            icon={<KeyOutlined />}
            title={configured ? '请前往号池库存运营执行双版本轮换' : '写入交付 Key'}
            aria-label={
              configured
                ? `${item.display_name} 已配置，请执行双版本轮换`
                : `写入 ${item.display_name} 交付 Key`
            }
            disabled={configured}
            onClick={() => {
              setValue('')
              setSelected(item)
            }}
          />
        )
      },
    },
  ]

  return (
    <PageContainer title="Key 交付配置">
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 16 }}
        message="交付 Key 会通过随机挑战与 Connector 本地 API Key 做一致性校验；此状态只证明本地绑定匹配，不代表远端读回。平台不会为用户自有账号保存交付 Key。"
      />
      <ProTable<UpstreamChannel>
        rowKey="id"
        search={false}
        loading={channels.isLoading || deliveries.isLoading}
        dataSource={managedChannels}
        columns={columns}
        options={{
          reload: async () => {
            await Promise.all([channels.refetch(), deliveries.refetch()])
          },
        }}
        locale={{ emptyText: '当前没有平台托管的上游账号' }}
      />

      <Modal
        title={
          <Space>
            <KeyOutlined />
            写入交付 Key
          </Space>
        }
        open={!!selected}
        destroyOnHidden
        confirmLoading={upsert.isPending}
        okText="安全写入"
        cancelText="取消"
        okButtonProps={{ disabled: !value.trim() }}
        onCancel={() => {
          setValue('')
          setSelected(null)
        }}
        onOk={async () => {
          if (!selected || !value.trim()) return
          await upsert.mutateAsync({ channelId: selected.id, value })
          setValue('')
          setSelected(null)
        }}
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="写入后页面和普通 API 都不会回显明文；已有 Key 的变更请在“号池库存运营”执行双版本轮换。"
        />
        <ProFormText.Password
          label="API Key"
          fieldProps={{
            value,
            autoComplete: 'new-password',
            onChange: (event) => setValue(event.target.value),
          }}
        />
      </Modal>
    </PageContainer>
  )
}
