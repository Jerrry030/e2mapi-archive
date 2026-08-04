import { useEffect } from 'react'
import { useSearchParams } from 'react-router'
import {
  PageContainer,
  ProCard,
  ProForm,
  ProFormCheckbox,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProFormInstance } from '@ant-design/pro-components'
import { Alert, Col, Descriptions, Form, Row, Tag, Typography } from 'antd'
import { KeyOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { getStoredUser, isPlatformAdmin } from '../api/auth'
import { useAuthSystemSettings, useUpdateAuthSystemSettings } from '../api/hooks'
import type { UpdateAuthSystemSettingsInput } from '../api/types'
import { AbsoluteTime } from '../components/common'
import { consoleFeatureFlags } from '../config/featureFlags'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import PaymentSettings from './PaymentSettings'
import {
  searchForSystemSettingsView,
  systemSettingsViewFromSearch,
  type SystemSettingsView,
} from './systemSettingsView'

interface FormValues {
  registration_enabled?: boolean
  registration_email_suffixes?: string
  invitation_required?: boolean
  turnstile_enabled?: boolean
  turnstile_site_key?: string
  turnstile_secret_key?: string
  clear_turnstile_secret?: boolean
}

function splitSuffixes(value?: string): string[] {
  return (value ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

function suffixes(value?: string[] | null): string[] {
  return Array.isArray(value) ? value : []
}

export default function SystemSettings() {
  useLocaleVersion()
  const me = getStoredUser()
  const [form] = Form.useForm<FormValues>()
  const platformAdmin = isPlatformAdmin(me)
  const settings = useAuthSystemSettings(platformAdmin)
  const update = useUpdateAuthSystemSettings()
  const turnstileEnabled = Form.useWatch('turnstile_enabled', form)
  const clearSecret = Form.useWatch('clear_turnstile_secret', form)
  const [searchParams, setSearchParams] = useSearchParams()
  const paymentsEnabled = consoleFeatureFlags.payments
  const view = systemSettingsViewFromSearch(searchParams, paymentsEnabled)
  const tabs = [
    { key: 'auth', tab: t('systemSettings.tabs.auth') },
    ...(paymentsEnabled ? [{ key: 'payment', tab: t('systemSettings.tabs.payment') }] : []),
  ]

  useEffect(() => {
    if (!settings.data) return
    form.setFieldsValue({
      registration_enabled: settings.data.registration_enabled,
      registration_email_suffixes: suffixes(settings.data.registration_email_suffix_whitelist).join(
        ', ',
      ),
      invitation_required: settings.data.invitation_required,
      turnstile_enabled: settings.data.turnstile_enabled,
      turnstile_site_key: settings.data.turnstile_site_key,
      turnstile_secret_key: '',
      clear_turnstile_secret: false,
    })
  }, [form, settings.data])

  useEffect(() => {
    if (paymentsEnabled || searchParams.get('view') !== 'payment') return
    setSearchParams(searchForSystemSettingsView(searchParams, 'auth'), {
      replace: true,
    })
  }, [paymentsEnabled, searchParams, setSearchParams])

  if (!platformAdmin) {
    return (
      <PageContainer title={t('systemSettings.title')}>
        <Typography.Text type="secondary">{t('systemSettings.adminOnly')}</Typography.Text>
      </PageContainer>
    )
  }

  const changeView = (key: string) =>
    setSearchParams(searchForSystemSettingsView(searchParams, key as SystemSettingsView), {
      replace: true,
    })

  if (view === 'payment') {
    return (
      <PageContainer
        title={t('systemSettings.title')}
        subTitle={t('systemSettings.subtitle')}
        tabList={tabs}
        tabActiveKey={view}
        onTabChange={changeView}
      >
        <PaymentSettings />
      </PageContainer>
    )
  }

  const onFinish = async (values: FormValues) => {
    const secret = values.turnstile_secret_key?.trim()
    const body: UpdateAuthSystemSettingsInput = {
      registration_enabled: !!values.registration_enabled,
      registration_email_suffix_whitelist: splitSuffixes(values.registration_email_suffixes),
      invitation_required: !!values.invitation_required,
      turnstile_enabled: !!values.turnstile_enabled,
      turnstile_site_key: values.turnstile_site_key?.trim() ?? '',
      clear_turnstile_secret: !!values.clear_turnstile_secret,
    }
    if (secret) body.turnstile_secret_key = secret
    await update.mutateAsync(body)
    form.setFieldsValue({ turnstile_secret_key: '', clear_turnstile_secret: false })
    return true
  }

  return (
    <PageContainer
      title={t('systemSettings.title')}
      subTitle={t('systemSettings.subtitle')}
      tabList={tabs}
      tabActiveKey={view}
      onTabChange={changeView}
    >
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={16}>
          <ProCard bordered>
            <ProForm<FormValues>
              form={form as unknown as ProFormInstance<FormValues>}
              layout="vertical"
              submitter={{
                searchConfig: { submitText: '保存设置', resetText: '重置' },
                submitButtonProps: { loading: update.isPending },
                resetButtonProps: { onClick: () => settings.refetch() },
              }}
              onFinish={onFinish}
            >
              <ProFormSwitch
                name="registration_enabled"
                label="开放账号自助注册"
                fieldProps={{ checkedChildren: '开启', unCheckedChildren: '关闭' }}
              />
              <ProFormTextArea
                name="registration_email_suffixes"
                label="允许注册邮箱后缀"
                placeholder="@example.com, *.edu.cn"
                fieldProps={{ rows: 3 }}
                extra="留空表示不限制；支持 @example.com、example.com、*.edu.cn。只影响公开注册，不影响已有用户登录。"
              />
              <ProFormSwitch
                name="invitation_required"
                label="注册需要邀请码"
                fieldProps={{ checkedChildren: '开启', unCheckedChildren: '关闭' }}
                extra="开启后，公开注册必须提交一张未使用的邀请码（在「兑换码管理」生成邀请类型的码）。"
              />

              <Typography.Title level={5} style={{ marginTop: 8 }}>
                Turnstile
              </Typography.Title>
              <ProFormSwitch
                name="turnstile_enabled"
                label="注册时启用 Turnstile"
                fieldProps={{ checkedChildren: '开启', unCheckedChildren: '关闭' }}
              />
              <ProFormText
                name="turnstile_site_key"
                label="站点密钥"
                disabled={!turnstileEnabled}
              />
              <ProFormText.Password
                name="turnstile_secret_key"
                label="验证密钥"
                disabled={!turnstileEnabled || clearSecret}
                placeholder={
                  settings.data?.turnstile_secret_configured ? '已配置，留空保持不变' : '未配置'
                }
                extra="验证密钥只保存到后端设置，不会出现在公开配置或页面回显中。"
              />
              <ProFormCheckbox
                name="clear_turnstile_secret"
                disabled={!settings.data?.turnstile_secret_configured}
              >
                清空已保存验证密钥
              </ProFormCheckbox>
            </ProForm>
          </ProCard>
        </Col>

        <Col xs={24} lg={8}>
          <ProCard title="当前状态" bordered loading={settings.isLoading}>
            <Descriptions
              column={1}
              size="small"
              items={[
                {
                  label: '开放注册',
                  children: settings.data?.registration_enabled ? (
                    <Tag color="success">开启</Tag>
                  ) : (
                    <Tag>关闭</Tag>
                  ),
                },
                {
                  label: '邮箱后缀',
                  children: suffixes(settings.data?.registration_email_suffix_whitelist).length
                    ? suffixes(settings.data?.registration_email_suffix_whitelist).join(', ')
                    : '不限制',
                },
                {
                  label: 'Turnstile',
                  children: settings.data?.turnstile_enabled ? (
                    <Tag color="blue">开启</Tag>
                  ) : (
                    <Tag>关闭</Tag>
                  ),
                },
                {
                  label: '验证密钥',
                  children: settings.data?.turnstile_secret_configured ? (
                    <Tag color="green">已配置</Tag>
                  ) : (
                    <Tag color="warning">未配置</Tag>
                  ),
                },
                {
                  label: '更新时间',
                  children: <AbsoluteTime value={settings.data?.updated_at} />,
                },
              ]}
            />
          </ProCard>

          {turnstileEnabled && !settings.data?.turnstile_secret_configured && !clearSecret ? (
            <Alert
              style={{ marginTop: 16 }}
              type="warning"
              showIcon
              icon={<KeyOutlined />}
              message="Turnstile 开启后必须保存验证密钥，否则注册校验会失败。"
            />
          ) : null}

          <Alert
            style={{ marginTop: 16 }}
            type="info"
            showIcon
            icon={<SafetyCertificateOutlined />}
            message="公开注册只创建个体账号，并授予托管能力；不能从公开入口创建管理员。"
          />
        </Col>
      </Row>
    </PageContainer>
  )
}
