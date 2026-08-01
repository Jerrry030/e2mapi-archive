import { useState } from 'react'
import { PageContainer } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Button, Popconfirm, Segmented, Space, Tag } from 'antd'
import { useActiveRoleUser, useApprovals, useDecideApproval } from '../api/hooks'
import { isPlatformAdmin } from '../api/auth'
import { EmptyTeach, RelativeTime } from '../components/common'
import { UserSelect } from '../components/fields'
import { RiskLevelTag } from '../components/tags'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type { ApprovalRequest, ApprovalStatus } from '../api/types'
import { auditActionLabel } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

const statusMap: Record<ApprovalStatus, { color: string; label: string }> = {
  pending: { color: 'gold', label: '待审批' },
  approved: { color: 'processing', label: '已批准' },
  executed: { color: 'success', label: '已执行' },
  rejected: { color: 'default', label: '已驳回' },
  failed: { color: 'error', label: '执行失败' },
}

export default function Approvals() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const [filter, setFilter] = useState<string>('pending')
  const [userId, setUserId] = useState<number | undefined>()
  const { data, isLoading, refetch } = useApprovals(
    filter === 'all' ? undefined : filter,
    platform ? userId : undefined,
  )
  const decide = useDecideApproval()

  const columns: ProColumns<ApprovalRequest>[] = [
    { title: '审批单', dataIndex: 'id', width: 160, ellipsis: true },
    {
      title: '动作',
      dataIndex: 'action',
      width: 240,
      ellipsis: true,
      render: (_, r) => (
        <Space>
          <span>
            {r.action === 'batch_set_schedulable'
              ? `批量${r.schedulable ? '启用' : '停用'} ${r.account_ids?.length ?? 0} 个账号`
              : auditActionLabel(r.action)}
          </span>
          <RiskLevelTag level={r.risk_level} />
        </Space>
      ),
    },
    { title: '实例', dataIndex: 'instance_id', width: 160, ellipsis: true },
    { title: '原因', dataIndex: 'reason', width: 180, ellipsis: true, render: (v) => v || '—' },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (_, r) => {
        const m = statusMap[r.status] ?? { color: 'default', label: r.status }
        return <Tag color={m.color}>{m.label}</Tag>
      },
    },
    { title: '发起人', dataIndex: 'requested_by', width: 96 },
    {
      title: '决定',
      dataIndex: 'decided_by',
      width: 96,
      render: (_, r) => (r.decided_by ? `${r.decided_by}` : '—'),
    },
    {
      title: '时间',
      dataIndex: 'created_at',
      width: 120,
      render: (_, r) => <RelativeTime value={r.created_at} />,
    },
    {
      title: '操作',
      valueType: 'option',
      width: 120,
      render: (_, r) =>
        r.status === 'pending' ? (
          <Space>
            <Popconfirm
              title="批准并立即执行？"
              description={`将批量${r.schedulable ? '启用' : '停用'} ${r.account_ids?.length ?? 0} 个账号`}
              onConfirm={() => decide.mutate({ id: r.id, decision: 'approve' })}
            >
              <a>批准</a>
            </Popconfirm>
            <Popconfirm
              title="驳回该审批单？"
              onConfirm={() => decide.mutate({ id: r.id, decision: 'reject', note: '控制台驳回' })}
            >
              <a style={{ color: '#ff4d4f' }}>驳回</a>
            </Popconfirm>
          </Space>
        ) : (
          <span style={{ color: '#999' }}>{r.result_note || '—'}</span>
        ),
    },
  ]

  return (
    <PageContainer title="待办审批" subTitle="L2/L3 高危动作需人工批准后才执行">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message="批量启停账号需人工批准后才会执行。如已开启通知，系统会发送提醒；每个账号的执行都会带审批单号写入审计。在实例的账号列表中勾选多个账号可发起批量操作。"
      />
      <ProTable<ApprovalRequest>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          platform ? (
            <UserSelect key="user" value={userId} onChange={setUserId} placeholder="全部账号" />
          ) : null,
          <Segmented
            key="f"
            value={filter}
            onChange={(v) => setFilter(String(v))}
            options={[
              { label: '待审批', value: 'pending' },
              { label: '已执行', value: 'executed' },
              { label: '已驳回', value: 'rejected' },
              { label: '失败', value: 'failed' },
              { label: '全部', value: 'all' },
            ]}
          />,
        ]}
        locale={{
          emptyText: (
            <EmptyTeach title="暂无审批单 — 在实例账号列表勾选多个账号发起批量操作，即会生成 L2 审批单" />
          ),
        }}
      />
    </PageContainer>
  )
}
