import { useMemo, useState } from 'react'
import { useParams, Link } from 'react-router'
import { ModalForm, PageContainer, ProFormSelect, ProFormText } from '@ant-design/pro-components'
import type { ProColumns } from '@ant-design/pro-components'
import { Alert, Badge, Button, Space, Switch, Tag } from 'antd'
import { AuditOutlined, SwapOutlined } from '@ant-design/icons'
import {
  useInstanceAccounts,
  useActiveRoleUser,
  useSetSchedulable,
  useSubmitApproval,
  useSwitchUpstream,
} from '../api/hooks'
import { EmptyTeach } from '../components/common'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import type { GatewayAccount } from '../api/types'
import { canWriteOwner } from '../api/auth'
import { friendlyErrorMessage } from '../api/errors'

function accountStatusBadge(status?: string) {
  const map: Record<string, { s: 'success' | 'error' | 'warning' | 'default'; t: string }> = {
    active: { s: 'success', t: '正常' },
    banned: { s: 'error', t: '已封禁' },
    disabled: { s: 'default', t: '已停用' },
    error: { s: 'error', t: '错误' },
    expired: { s: 'warning', t: '已过期' },
    rate_limited: { s: 'warning', t: '限流中' },
  }
  const m = map[status ?? ''] ?? { s: 'default' as const, t: status ?? '—' }
  return <Badge status={m.s} text={m.t} />
}

export default function InstanceAccounts() {
  const user = useActiveRoleUser()
  const { id = '' } = useParams()
  const { data, isLoading, isError, error, refetch } = useInstanceAccounts(id)
  const setSchedulable = useSetSchedulable(id)
  const switchUpstream = useSwitchUpstream(id)
  const submitApproval = useSubmitApproval()
  const [switchOpen, setSwitchOpen] = useState(false)
  const [batchOpen, setBatchOpen] = useState(false)
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const writable = canWriteOwner(user)

  const accounts = useMemo(() => data ?? [], [data])
  const options = useMemo(
    () =>
      accounts.map((a) => ({
        value: a.id,
        label: `${a.display_name || a.id}（${a.platform ?? ''} · ${a.schedulable ? '调度中' : '已停用'}）`,
      })),
    [accounts],
  )

  const columns: ProColumns<GatewayAccount>[] = [
    {
      title: '账号',
      dataIndex: 'display_name',
      width: 180,
      ellipsis: true,
      render: (v, r) => v || r.id,
    },
    { title: '平台', dataIndex: 'platform', width: 120, render: (v) => v || '—' },
    {
      title: '类型',
      dataIndex: 'type',
      width: 100,
      render: (v) => (v ? <Tag>{v as string}</Tag> : '—'),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 130,
      render: (_, r) => accountStatusBadge(r.status),
    },
    {
      title: '余额',
      dataIndex: 'balance',
      width: 96,
      render: (_, r) => {
        if (r.balance == null) return '—'
        const low = r.balance < 10
        return (
          <Tag color={low ? 'error' : 'default'}>
            {low ? '⚠ ' : ''}
            {r.balance.toFixed(2)}
          </Tag>
        )
      },
    },
    { title: '优先级', dataIndex: 'priority', width: 96, render: (v) => v ?? '—' },
    writable
      ? {
          title: '参与调度',
          dataIndex: 'schedulable',
          width: 150,
          render: (_, r) => (
            <Switch
              checked={r.schedulable}
              loading={setSchedulable.isPending}
              checkedChildren="调度中"
              unCheckedChildren="已停用"
              onChange={(checked) =>
                setSchedulable.mutate({
                  accountId: r.id,
                  schedulable: checked,
                  reason: checked ? '手动启用调度' : '手动停用调度',
                })
              }
            />
          ),
        }
      : {
          title: '参与调度',
          dataIndex: 'schedulable',
          width: 112,
          render: (_, r) => (r.schedulable ? '调度中' : '已停用'),
        },
  ]

  return (
    <PageContainer
      title="自有账号（高级）"
      subTitle="仅管理你自行配置到网关的账号；平台托管线路不会显示，也不能在这里启停或切换"
      extra={[
        <Link key="back" to="/instances">
          <Button>返回实例</Button>
        </Link>,
        writable ? (
          <Button
            key="switch"
            type="primary"
            icon={<SwapOutlined />}
            disabled={accounts.length < 1}
            onClick={() => setSwitchOpen(true)}
          >
            一键切换自有账号
          </Button>
        ) : null,
      ]}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="智能路由与平台托管线路请前往“服务质量与路由”；本页仅用于自有账号的高级人工处置。"
        action={
          <Link to="/pool-health">
            <Button type="link">前往服务质量与路由</Button>
          </Link>
        }
      />
      {isError && (
        <Alert
          type="error"
          style={{ marginBottom: 16 }}
          message="无法读取账号列表"
          description={friendlyErrorMessage(error)}
        />
      )}
      <ProTable<GatewayAccount>
        rowKey="id"
        loading={isLoading}
        dataSource={accounts}
        columns={columns}
        search={false}
        scroll={{ x: 'max-content' }}
        options={{ reload: () => refetch() }}
        rowSelection={
          writable
            ? {
                selectedRowKeys: selectedIds,
                onChange: (keys) => setSelectedIds(keys as string[]),
              }
            : false
        }
        tableAlertOptionRender={
          writable
            ? () => (
                <Space>
                  <Button size="small" icon={<AuditOutlined />} onClick={() => setBatchOpen(true)}>
                    批量启停自有账号（L2 需审批）
                  </Button>
                </Space>
              )
            : false
        }
        locale={{
          emptyText: (
            <EmptyTeach title="没有读取到自有账号 - 确认连接器已上线，并已在连接器本地配置页保存网关凭证" />
          ),
        }}
      />

      <ModalForm<{ schedulable: string; reason: string }>
        title={`批量启停 ${selectedIds.length} 个自有账号（L2 审批）`}
        open={batchOpen}
        onOpenChange={setBatchOpen}
        modalProps={{ destroyOnHidden: true }}
        onFinish={async (values) => {
          await submitApproval.mutateAsync({
            instance_id: id,
            action: 'batch_set_schedulable',
            account_ids: selectedIds,
            schedulable: values.schedulable === 'enable',
            reason: values.reason,
          })
          setSelectedIds([])
          return true
        }}
      >
        <Alert
          type="warning"
          style={{ marginBottom: 16 }}
          message="批量启停需人工确认：提交后会生成审批单，需在【审批中心】批准后才会执行；如已开启通知，系统会发送提醒。"
        />
        <ProFormSelect
          name="schedulable"
          label="批量动作"
          rules={[{ required: true }]}
          options={[
            { value: 'disable', label: `停用 ${selectedIds.length} 个账号` },
            { value: 'enable', label: `启用 ${selectedIds.length} 个账号` },
          ]}
        />
        <ProFormText name="reason" label="原因" placeholder="例如：批量下线过期账号" />
      </ModalForm>

      <ModalForm<{ disable_account_id: string; enable_account_id: string; reason: string }>
        title="一键切换自有账号"
        open={switchOpen}
        onOpenChange={setSwitchOpen}
        modalProps={{ destroyOnHidden: true }}
        onFinish={async (values) => {
          await switchUpstream.mutateAsync({
            disable_account_id: values.disable_account_id,
            enable_account_id: values.enable_account_id,
            reason: values.reason,
          })
          return true
        }}
      >
        <Alert
          type="info"
          style={{ marginBottom: 16 }}
          message="将停用「问题账号」的调度并启用「备用账号」。两步都在 sub2api 上生效，且各留一条 L1 审计。"
        />
        <ProFormSelect
          name="disable_account_id"
          label="停用（问题账号）"
          options={options}
          rules={[{ required: true }]}
        />
        <ProFormSelect name="enable_account_id" label="启用（备用账号）" options={options} />
        <ProFormText name="reason" label="原因（可选）" placeholder="例如：连续 429 限流" />
      </ModalForm>
    </PageContainer>
  )
}
