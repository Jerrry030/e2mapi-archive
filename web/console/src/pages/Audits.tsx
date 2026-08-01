import { useState } from 'react'
import { PageContainer } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { useActiveRoleUser, useAudits, useInstances } from '../api/hooks'
import { AbsoluteTime } from '../components/common'
import { EventLevelTag, ResultTag, RiskLevelTag } from '../components/tags'
import { effectiveEventLevel } from '../eventLevel'
import { UserSelect } from '../components/fields'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { activeRoleCanUseOwnerSurface, currentUserId, isPlatformAdmin } from '../api/auth'
import type { OperationAudit } from '../api/types'
import { auditActivityDescription, auditActorLabel, auditTargetTypeLabel } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import { friendlyInlineError } from '../api/errors'

export default function Audits() {
  useLocaleVersion()
  const user = useActiveRoleUser()
  const platform = isPlatformAdmin(user)
  const [userId, setUserId] = useState<number | undefined>(
    !platform ? currentUserId(user) : undefined,
  )
  const { data, isLoading, refetch } = useAudits(userId)
  const instances = useInstances(userId, activeRoleCanUseOwnerSurface(user))
  const instanceNames = new Map(
    (instances.data ?? []).map((instance) => [instance.id, instance.name]),
  )

  const columns: ProColumns<OperationAudit>[] = [
    {
      title: '时间',
      dataIndex: 'created_at',
      width: 180,
      render: (_, r) => <AbsoluteTime value={r.created_at} />,
      sorter: (a, b) => a.created_at.localeCompare(b.created_at),
      defaultSortOrder: 'descend',
    },
    {
      title: '发起者',
      dataIndex: 'actor_id',
      width: 140,
      ellipsis: true,
      render: (_, r) => auditActorLabel(r.actor_type, r.actor_id),
    },
    {
      title: '记录描述',
      dataIndex: 'action',
      width: 280,
      ellipsis: true,
      render: (_, r) =>
        auditActivityDescription(
          r.action,
          r.result,
          r.error_message,
          instanceNames.get(r.instance_id ?? '') ?? '',
          r.risk_level,
          r.details,
        ),
    },
    {
      title: '事件等级',
      dataIndex: 'event_level',
      width: 125,
      render: (_, r) => (
        <EventLevelTag level={effectiveEventLevel(r.event_level, r.risk_level, r.result)} />
      ),
    },
    {
      title: '状态',
      dataIndex: 'result',
      width: 100,
      render: (_, r) => <ResultTag result={r.result} />,
    },
    {
      title: '操作风险',
      dataIndex: 'risk_level',
      width: 110,
      render: (_, r) => <RiskLevelTag level={r.risk_level} />,
    },
    {
      title: '目标',
      dataIndex: 'target_id',
      width: 180,
      ellipsis: true,
      render: (_, r) =>
        r.target_id ? `${auditTargetTypeLabel(r.target_type)}：${r.target_id}` : '—',
    },
    {
      title: '实例',
      dataIndex: 'instance_id',
      width: 160,
      ellipsis: true,
      render: (v) => instanceNames.get(String(v ?? '')) || v || '—',
    },
  ]

  return (
    <PageContainer title="审计日志">
      <ProTable<OperationAudit>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        toolBarRender={() => [
          <UserSelect key="u" value={userId} onChange={setUserId} placeholder="全部账号" />,
        ]}
        expandable={{
          expandedRowRender: (r) => (
            <div style={{ color: '#666' }}>
              <div>技术动作：{r.action || '—'}</div>
              <div>原始结果：{r.result || '—'}</div>
              <div>事件等级：{effectiveEventLevel(r.event_level, r.risk_level, r.result)}</div>
              <div>业务上下文：{r.details ? JSON.stringify(r.details) : '—'}</div>
              <div>请求摘要：{r.request_payload_hash || '—'}</div>
              <div>审批单：{r.approval_id || '—'}</div>
              <div>工作流：{r.workflow_run_id || '—'}</div>
              <div>错误：{friendlyInlineError(r.error_message) || '—'}</div>
            </div>
          ),
        }}
      />
    </PageContainer>
  )
}
