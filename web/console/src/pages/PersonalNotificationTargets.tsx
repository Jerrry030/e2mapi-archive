import { useState } from 'react'
import {
  ModalForm,
  ProCard,
  ProFormDependency,
  ProFormSwitch,
  ProFormText,
} from '@ant-design/pro-components'
import { Alert, Button, Popconfirm, Space, Tag, Typography } from 'antd'
import {
  useDeleteNotificationTarget,
  useNotificationTargets,
  useUpdateNotificationTarget,
} from '../api/hooks'
import type {
  NotificationPersonalChannel,
  NotificationTarget,
  UpdateNotificationTargetInput,
} from '../api/types'
import { friendlyErrorMessage } from '../api/errors'
import { personalTargetForChannel } from './notificationTargets'

const { Text } = Typography

interface TargetFormValues {
  webhook_url?: string
  signing_secret?: string
  clear_signing_secret?: boolean
  onebot_url?: string
  access_token?: string
  group_id?: string
}

function cleanOptional(value?: string) {
  const cleaned = value?.trim()
  return cleaned || undefined
}

function targetSummary(target: NotificationTarget | undefined) {
  if (!target?.configured) return '尚未配置'
  if (target.channel === 'feishu') {
    return target.endpoint_host ? `机器人地址：${target.endpoint_host}` : '机器人地址已安全保存'
  }
  const parts = [
    target.endpoint_host ? `OneBot：${target.endpoint_host}` : 'OneBot 地址已安全保存',
    target.group_id_masked ? `群号：${target.group_id_masked}` : undefined,
  ].filter(Boolean)
  return parts.join(' · ')
}

function PersonalTargetCard({
  channel,
  target,
  onEdit,
  onDelete,
  deleting,
}: {
  channel: NotificationPersonalChannel
  target?: NotificationTarget
  onEdit: () => void
  onDelete: () => void
  deleting: boolean
}) {
  const configured = Boolean(target?.configured)
  const title = channel === 'feishu' ? '我的飞书机器人' : '我的 QQ 群'

  return (
    <ProCard
      bordered
      title={title}
      extra={<Tag color={configured ? 'green' : 'default'}>{configured ? '已配置' : '未配置'}</Tag>}
    >
      <Space direction="vertical" size={8} style={{ width: '100%' }}>
        <Text type="secondary">{targetSummary(target)}</Text>
        {configured ? (
          <Text type="secondary">
            {channel === 'feishu'
              ? `签名密钥：${target?.signing_secret_configured ? '已配置' : '未配置'}`
              : `访问令牌：${target?.access_token_configured ? '已配置' : '未配置'}`}
          </Text>
        ) : null}
        <Space>
          <Button size="small" type={configured ? 'default' : 'primary'} onClick={onEdit}>
            {configured ? '修改配置' : '立即配置'}
          </Button>
          {configured ? (
            <Popconfirm
              title={`删除${title}？`}
              description="如仍有启用的通知或发送中的消息使用该渠道，系统会阻止删除并提示你先处理。"
              onConfirm={onDelete}
            >
              <Button size="small" danger loading={deleting}>
                删除
              </Button>
            </Popconfirm>
          ) : null}
        </Space>
      </Space>
    </ProCard>
  )
}

export default function PersonalNotificationTargets({ userId }: { userId?: number }) {
  const targets = useNotificationTargets(userId, Boolean(userId))
  const updateTarget = useUpdateNotificationTarget()
  const deleteTarget = useDeleteNotificationTarget()
  const [editingChannel, setEditingChannel] = useState<NotificationPersonalChannel | null>(null)
  const editingTarget = editingChannel
    ? personalTargetForChannel(targets.data, editingChannel)
    : undefined

  if (!userId) {
    return (
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="选择一个站长账号后，可查看和维护该账号自己的飞书机器人或 QQ 群。"
      />
    )
  }

  return (
    <>
      <ProCard
        title="我的接收渠道"
        subTitle="凭证只保存在 Vault，页面仅显示地址域名，不回显完整地址、Token、签名密钥或完整群号"
        gutter={16}
        wrap
        loading={targets.isLoading}
        style={{ marginBottom: 16 }}
      >
        {targets.isError ? (
          <Alert
            type="warning"
            showIcon
            message="个人接收渠道加载失败"
            description={friendlyErrorMessage(targets.error)}
            action={<Button onClick={() => targets.refetch()}>重试</Button>}
          />
        ) : (
          <>
            <PersonalTargetCard
              channel="feishu"
              target={personalTargetForChannel(targets.data, 'feishu')}
              onEdit={() => setEditingChannel('feishu')}
              onDelete={() => deleteTarget.mutate({ channel: 'feishu', userId })}
              deleting={deleteTarget.isPending && deleteTarget.variables?.channel === 'feishu'}
            />
            <PersonalTargetCard
              channel="qq"
              target={personalTargetForChannel(targets.data, 'qq')}
              onEdit={() => setEditingChannel('qq')}
              onDelete={() => deleteTarget.mutate({ channel: 'qq', userId })}
              deleting={deleteTarget.isPending && deleteTarget.variables?.channel === 'qq'}
            />
          </>
        )}
      </ProCard>

      <ModalForm<TargetFormValues>
        key={`${editingChannel ?? 'closed'}-${editingTarget?.configured ? 'edit' : 'new'}`}
        title={
          editingChannel === 'feishu'
            ? `${editingTarget?.configured ? '修改' : '配置'}我的飞书机器人`
            : `${editingTarget?.configured ? '修改' : '配置'}我的 QQ 群`
        }
        open={Boolean(editingChannel)}
        onOpenChange={(open) => {
          if (!open) setEditingChannel(null)
        }}
        modalProps={{ destroyOnHidden: true }}
        submitter={{
          searchConfig: { submitText: '安全保存', resetText: '取消' },
          submitButtonProps: { loading: updateTarget.isPending },
        }}
        onFinish={async (values) => {
          if (!editingChannel) return false
          const body: UpdateNotificationTargetInput = { user_id: userId }
          if (editingChannel === 'feishu') {
            body.webhook_url = cleanOptional(values.webhook_url)
            body.signing_secret = cleanOptional(values.signing_secret)
            body.clear_signing_secret = Boolean(values.clear_signing_secret && !body.signing_secret)
          } else {
            body.onebot_url = cleanOptional(values.onebot_url)
            body.access_token = cleanOptional(values.access_token)
            body.group_id = cleanOptional(values.group_id)
          }
          await updateTarget.mutateAsync({ channel: editingChannel, body })
          return true
        }}
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={
            editingTarget?.configured
              ? '出于安全考虑，已保存内容不会回显。留空表示保持原值。'
              : '填写内容将直接加密保存到 Vault，通知设置只保存安全引用。'
          }
        />
        {editingChannel === 'feishu' ? (
          <>
            <ProFormText.Password
              name="webhook_url"
              label="飞书机器人 Webhook"
              placeholder={
                editingTarget?.configured ? '已保存，留空保持不变' : '粘贴飞书机器人 Webhook'
              }
              rules={
                editingTarget?.configured
                  ? []
                  : [{ required: true, message: '请输入飞书机器人 Webhook' }]
              }
            />
            <ProFormText.Password
              name="signing_secret"
              label="签名密钥（可选）"
              placeholder={
                editingTarget?.signing_secret_configured ? '已配置，留空保持不变' : '未配置，可留空'
              }
            />
            {editingTarget?.signing_secret_configured ? (
              <ProFormSwitch name="clear_signing_secret" label="清除已保存的签名密钥" />
            ) : null}
          </>
        ) : null}
        {editingChannel === 'qq' ? (
          <>
            <ProFormText.Password
              name="onebot_url"
              label="OneBot 地址"
              placeholder={
                editingTarget?.configured ? '已保存，留空保持不变' : '例如：https://bot.example.com'
              }
              rules={
                editingTarget?.configured ? [] : [{ required: true, message: '请输入 OneBot 地址' }]
              }
            />
            <ProFormText.Password
              name="group_id"
              label="QQ群号"
              placeholder={
                editingTarget?.group_id_masked
                  ? `已保存（${editingTarget.group_id_masked}），留空保持不变`
                  : '请输入接收消息的群号'
              }
              rules={
                editingTarget?.configured ? [] : [{ required: true, message: '请输入 QQ 群号' }]
              }
            />
            <ProFormText.Password
              name="access_token"
              label="访问令牌"
              placeholder={
                editingTarget?.access_token_configured
                  ? '已配置，留空保持不变'
                  : '请输入 OneBot 访问令牌'
              }
              rules={
                editingTarget?.configured
                  ? []
                  : [{ required: true, message: '请输入 OneBot 访问令牌' }]
              }
            />
          </>
        ) : null}
      </ModalForm>
    </>
  )
}
