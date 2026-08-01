import { useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { UpstreamChannel } from '../../api/types'
import {
  type IntelligenceLink,
  type IntelligenceLinkInput,
  type IntelligencePriceDimension,
  type IntelligenceSource,
} from '../../api/upstreamIntelligence'
import { intelligenceLinkStatusInput } from './intelligenceLinkInput'
import { t } from '../../i18n'
import { useLocaleVersion } from '../../i18n/react'

interface LinkFormValues {
  intelligence_source_id: string
  link_scope: 'source_identity' | 'channel'
  upstream_source_identity?: string
  channel_id?: string
  price_dimension: IntelligencePriceDimension
}

interface Props {
  userId?: number
  sources: IntelligenceSource[]
  channels: UpstreamChannel[]
  links: IntelligenceLink[]
  loading?: boolean
  saving?: boolean
  onSave: (input: IntelligenceLinkInput) => Promise<unknown>
}

export function IntelligenceLinkManager({
  userId,
  sources,
  channels,
  links,
  loading,
  saving,
  onSave,
}: Props) {
  useLocaleVersion()
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm<LinkFormValues>()
  const scope = Form.useWatch('link_scope', form) ?? 'channel'
  const sourceNames = useMemo(
    () => new Map(sources.map((source) => [source.id, source.display_name])),
    [sources],
  )
  const channelNames = useMemo(
    () => new Map(channels.map((channel) => [channel.id, channel.display_name])),
    [channels],
  )
  const dimensions: Array<{ value: IntelligencePriceDimension; label: string }> = [
    'input',
    'output',
    'cached_input',
    'request',
  ].map((value) => ({
    value: value as IntelligencePriceDimension,
    label: t(`upstreamIntelligence.priceDimensions.${value}`, value),
  }))

  const submit = async () => {
    if (!userId) return
    const values = await form.validateFields()
    await onSave({ ...values, user_id: userId, status: 'active' })
    setOpen(false)
    form.resetFields()
  }

  const setStatus = (link: IntelligenceLink, active: boolean) =>
    onSave(intelligenceLinkStatusInput(link, userId!, active))

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t('upstreamIntelligence.links.noticeTitle')}
        description={t('upstreamIntelligence.links.noticeDescription')}
      />
      <Space style={{ width: '100%', justifyContent: 'space-between' }}>
        <Typography.Text type="secondary">
          {t('upstreamIntelligence.links.count', undefined, { count: links.length })}
        </Typography.Text>
        <Button type="primary" disabled={!userId} onClick={() => setOpen(true)}>
          {t('upstreamIntelligence.links.create')}
        </Button>
      </Space>
      <div
        className="intelligence-scroll-region"
        role="region"
        aria-label={t('upstreamIntelligence.links.regionLabel')}
        tabIndex={0}
      >
        <Table<IntelligenceLink>
          rowKey="id"
          loading={loading}
          dataSource={links}
          pagination={false}
          scroll={{ x: 'max-content' }}
          columns={[
            {
              title: t('upstreamIntelligence.links.intelligenceSource'),
              render: (_, link) =>
                sourceNames.get(link.intelligence_source_id) ?? link.intelligence_source_id,
            },
            {
              title: t('upstreamIntelligence.links.qualityChannel'),
              render: (_, link) => channelNames.get(link.channel_id) ?? link.channel_id,
            },
            {
              title: t('upstreamIntelligence.links.priceDimension'),
              dataIndex: 'price_dimension',
              render: (value) =>
                t(`upstreamIntelligence.priceDimensions.${String(value)}`, String(value)),
            },
            {
              title: t('upstreamIntelligence.links.mappingMethod'),
              render: (_, link) =>
                link.link_scope === 'channel'
                  ? t('upstreamIntelligence.links.channelId')
                  : t('upstreamIntelligence.links.hiddenIdentity'),
            },
            {
              title: t('upstreamIntelligence.links.verification'),
              render: (_, link) =>
                link.verified_at ? (
                  <Tag color="green">{t('upstreamIntelligence.links.verified')}</Tag>
                ) : (
                  <Tag>{t('upstreamIntelligence.links.unverified')}</Tag>
                ),
            },
            {
              title: t('upstreamIntelligence.links.enabled'),
              render: (_, link) => (
                <Switch
                  aria-label={t('upstreamIntelligence.links.enableAria')}
                  checked={link.status === 'active'}
                  loading={saving}
                  onChange={(checked) => void setStatus(link, checked)}
                />
              ),
            },
          ]}
        />
      </div>
      <Modal
        title={t('upstreamIntelligence.links.modalTitle')}
        open={open}
        confirmLoading={saving}
        onOk={() => void submit()}
        onCancel={() => {
          setOpen(false)
          form.resetFields()
        }}
        destroyOnHidden
      >
        <Form<LinkFormValues>
          form={form}
          layout="vertical"
          initialValues={{ link_scope: 'channel', price_dimension: 'input' }}
        >
          <Form.Item
            name="intelligence_source_id"
            label={t('upstreamIntelligence.links.intelligenceSource')}
            rules={[{ required: true }]}
          >
            <Select
              options={sources.map((source) => ({ value: source.id, label: source.display_name }))}
            />
          </Form.Item>
          <Form.Item
            name="price_dimension"
            label={t('upstreamIntelligence.links.priceDimension')}
            rules={[{ required: true }]}
          >
            <Select options={dimensions} />
          </Form.Item>
          <Form.Item
            name="link_scope"
            label={t('upstreamIntelligence.links.targetType')}
            rules={[{ required: true }]}
          >
            <Select
              options={[
                { value: 'channel', label: t('upstreamIntelligence.links.chooseChannel') },
                {
                  value: 'source_identity',
                  label: t('upstreamIntelligence.links.enterIdentity'),
                },
              ]}
            />
          </Form.Item>
          {scope === 'channel' ? (
            <Form.Item
              name="channel_id"
              label={t('upstreamIntelligence.links.qualityChannel')}
              rules={[{ required: true }]}
            >
              <Select
                showSearch
                optionFilterProp="label"
                options={channels.map((channel) => ({
                  value: channel.id,
                  label: `${channel.display_name} · ${channel.source_id || channel.id}`,
                }))}
              />
            </Form.Item>
          ) : (
            <Form.Item
              name="upstream_source_identity"
              label={t('upstreamIntelligence.links.stableIdentity')}
              extra={t('upstreamIntelligence.links.identityHelp')}
              rules={[{ required: true }]}
            >
              <Input autoComplete="off" />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </Space>
  )
}
