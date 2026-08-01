import { useEffect, useState } from 'react'
import {
  ModalForm,
  ProCard,
  ProForm,
  ProFormCheckbox,
  ProFormDigit,
  ProFormGroup,
  ProFormSelect,
  ProFormSwitch,
  ProFormText,
  ProFormTextArea,
} from '@ant-design/pro-components'
import type { ProColumns, ProFormInstance } from '@ant-design/pro-components'
import {
  Alert,
  Button,
  Descriptions,
  Divider,
  Popconfirm,
  Space,
  Switch,
  Tag,
  Typography,
} from 'antd'
import { PlusOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import {
  useCreatePaymentProvider,
  useDeletePaymentProvider,
  usePaymentConfig,
  usePaymentProviders,
  useUpdatePaymentConfig,
  useUpdatePaymentProvider,
} from '../api/hooks'
import type {
  PaymentConfig,
  PaymentProvider,
  PaymentProviderKey,
  UpdatePaymentConfigInput,
} from '../api/types'
import { LocalizedProTable as ProTable } from '../components/LocalizedProTable'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'
import {
  localizedProviderDefinition,
  paymentProviderKeys,
  providerCreateInput,
  providerToForm,
  providerTypeDefaults,
  providerUpdateInput,
  type PaymentProviderFormValues,
} from './paymentProviderConfig'

const defaultConfig: UpdatePaymentConfigInput = {
  enabled: false,
  min_amount: 1,
  max_amount: 0,
  daily_limit: 0,
  order_timeout_minutes: 30,
  max_pending_orders: 3,
  enabled_payment_types: [],
  load_balance_strategy: 'round-robin',
  product_name_prefix: '',
  product_name_suffix: '',
  help_image_url: '',
  help_text: '',
  visible_method_alipay_source: '',
  visible_method_wxpay_source: '',
  visible_method_alipay_enabled: false,
  visible_method_wxpay_enabled: false,
}

function configToForm(config: PaymentConfig): UpdatePaymentConfigInput {
  return {
    enabled: config.enabled,
    min_amount: config.min_amount,
    max_amount: config.max_amount,
    daily_limit: config.daily_limit,
    order_timeout_minutes: config.order_timeout_minutes,
    max_pending_orders: config.max_pending_orders,
    enabled_payment_types: config.enabled_payment_types,
    load_balance_strategy: config.load_balance_strategy,
    product_name_prefix: config.product_name_prefix,
    product_name_suffix: config.product_name_suffix,
    help_image_url: config.help_image_url,
    help_text: config.help_text,
    visible_method_alipay_source: config.visible_method_alipay_source,
    visible_method_wxpay_source: config.visible_method_wxpay_source,
    visible_method_alipay_enabled: config.visible_method_alipay_enabled,
    visible_method_wxpay_enabled: config.visible_method_wxpay_enabled,
  }
}

export default function PaymentSettings() {
  useLocaleVersion()
  const [configForm] = ProForm.useForm<UpdatePaymentConfigInput>()
  const [providerForm] = ProForm.useForm<PaymentProviderFormValues>()
  const config = usePaymentConfig()
  const providers = usePaymentProviders()
  const updateConfig = useUpdatePaymentConfig()
  const createProvider = useCreatePaymentProvider()
  const updateProvider = useUpdatePaymentProvider()
  const deleteProvider = useDeletePaymentProvider()
  const [editing, setEditing] = useState<PaymentProvider>()
  const [modalOpen, setModalOpen] = useState(false)
  const providerKey = ProForm.useWatch('provider_key', providerForm) ?? 'easypay'
  const refundEnabled = ProForm.useWatch('refund_enabled', providerForm)
  const providerEnabled = ProForm.useWatch('enabled', providerForm)
  const alipayVisible = ProForm.useWatch('visible_method_alipay_enabled', configForm)
  const wxpayVisible = ProForm.useWatch('visible_method_wxpay_enabled', configForm)
  const supportedTypes = ProForm.useWatch('supported_types', providerForm) ?? []
  const clearSecrets = ProForm.useWatch('clear_secrets', providerForm) ?? []

  const providerDefinitionsForLocale = Object.fromEntries(
    paymentProviderKeys.map((key) => [key, localizedProviderDefinition(key)]),
  ) as Record<PaymentProviderKey, ReturnType<typeof localizedProviderDefinition>>
  const providerOptions = paymentProviderKeys.map((value) => ({
    value,
    label: providerDefinitionsForLocale[value].label,
  }))
  const definition = providerDefinitionsForLocale[providerKey]
  const configUnavailable = config.isLoading || config.isError || !config.data
  const providersUnavailable = providers.isLoading || providers.isError || !providers.data
  const writesDisabled = configUnavailable || providersUnavailable

  useEffect(() => {
    if (config.data) configForm.setFieldsValue(configToForm(config.data))
  }, [config.data, configForm])

  useEffect(() => {
    if (providerEnabled && clearSecrets.length > 0) {
      providerForm.setFieldValue('clear_secrets', [])
    }
  }, [clearSecrets.length, providerEnabled, providerForm])

  const openCreate = () => {
    if (writesDisabled) return
    setEditing(undefined)
    providerForm.resetFields()
    providerForm.setFieldsValue({
      provider_key: 'easypay',
      enabled: false,
      supported_types: ['alipay', 'wxpay'],
      payment_mode: 'qrcode',
      sort_order: (providers.data?.length ?? 0) * 10,
      refund_enabled: false,
      allow_user_refund: false,
      config: {},
      secrets: {},
      clear_secrets: [],
      limits: {},
    })
    setModalOpen(true)
  }

  const openEdit = (provider: PaymentProvider) => {
    if (writesDisabled) return
    setEditing(provider)
    providerForm.resetFields()
    providerForm.setFieldsValue(providerToForm(provider))
    setModalOpen(true)
  }

  const columns: ProColumns<PaymentProvider>[] = [
    {
      title: t('payment.providers.columns.channel'),
      dataIndex: 'name',
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{row.name}</Typography.Text>
          <Typography.Text type="secondary">
            {providerDefinitionsForLocale[row.provider_key].label}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('payment.providers.columns.methods'),
      dataIndex: 'supported_types',
      render: (_, row) => (
        <Space wrap>
          {row.supported_types.map((method) => (
            <Tag key={method}>{t(`payment.methods.${method}`, method)}</Tag>
          ))}
        </Space>
      ),
    },
    {
      title: t('payment.providers.columns.secrets'),
      dataIndex: 'secret_configured',
      render: (_, row) => {
        const total = providerDefinitionsForLocale[row.provider_key].secrets.length
        const ready = Object.values(row.secret_configured ?? {}).filter(Boolean).length
        return ready === total ? (
          <Tag color="success">{t('payment.providers.configured')}</Tag>
        ) : (
          <Tag color="warning">
            {t('payment.providers.configuredCount', undefined, { ready, total })}
          </Tag>
        )
      },
    },
    {
      title: t('payment.providers.columns.enabled'),
      dataIndex: 'enabled',
      width: 90,
      render: (_, row) => (
        <Switch
          checked={row.enabled}
          loading={updateProvider.isPending}
          disabled={writesDisabled}
          aria-label={t('payment.providers.toggleAria', undefined, { name: row.name })}
          onChange={(enabled) => updateProvider.mutate({ id: row.id, body: { enabled } })}
        />
      ),
    },
    { title: t('payment.providers.columns.sort'), dataIndex: 'sort_order', width: 80 },
    {
      title: t('payment.providers.columns.actions'),
      valueType: 'option',
      render: (_, row) => [
        <Button type="link" key="edit" disabled={writesDisabled} onClick={() => openEdit(row)}>
          {t('payment.providers.edit')}
        </Button>,
        <Popconfirm
          key="delete"
          title={t('payment.providers.deleteConfirm')}
          description={t('payment.providers.deleteDescription')}
          onConfirm={() => deleteProvider.mutate(row.id)}
        >
          <Button type="link" danger disabled={writesDisabled}>
            {t('payment.providers.delete')}
          </Button>
        </Popconfirm>,
      ],
    },
  ]

  const saveProvider = async (values: PaymentProviderFormValues) => {
    if (writesDisabled) return false
    if (editing) {
      await updateProvider.mutateAsync({ id: editing.id, body: providerUpdateInput(values) })
    } else {
      await createProvider.mutateAsync(providerCreateInput(values))
    }
    setModalOpen(false)
    return true
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message={t('payment.intro.message')}
        description={t('payment.intro.description')}
      />

      <ProCard title={t('payment.config.title')} bordered loading={config.isLoading}>
        {config.isError ? (
          <Alert
            type="error"
            showIcon
            message={t('payment.config.loadError')}
            action={
              <Button aria-label={t('common.retry')} size="small" onClick={() => config.refetch()}>
                {t('common.retry')}
              </Button>
            }
            style={{ marginBottom: 16 }}
          />
        ) : null}
        <ProForm<UpdatePaymentConfigInput>
          form={configForm as unknown as ProFormInstance<UpdatePaymentConfigInput>}
          initialValues={defaultConfig}
          layout="vertical"
          disabled={writesDisabled}
          onFinish={async (values) => {
            if (writesDisabled || !config.data) return false
            await updateConfig.mutateAsync({ ...configToForm(config.data), ...values })
            return true
          }}
          submitter={{
            render: ({ submit }) => [
              <Button
                key="reset"
                aria-label={t('common.reset')}
                disabled={writesDisabled}
                onClick={() => {
                  if (!config.data) return
                  configForm.setFieldsValue(configToForm(config.data))
                }}
              >
                {t('common.reset')}
              </Button>,
              <Button
                key="submit"
                type="primary"
                loading={updateConfig.isPending}
                disabled={writesDisabled}
                onClick={submit}
              >
                {t('payment.config.save')}
              </Button>,
            ],
          }}
        >
          <ProFormSwitch name="enabled" label={t('payment.config.enabled')} />
          <ProFormGroup>
            <ProFormDigit
              name="min_amount"
              label={t('payment.config.minAmount')}
              min={0}
              fieldProps={{ precision: 2 }}
            />
            <ProFormDigit
              name="max_amount"
              label={t('payment.config.maxAmount')}
              min={0}
              fieldProps={{ precision: 2 }}
              extra={t('payment.config.unlimited')}
            />
            <ProFormDigit
              name="daily_limit"
              label={t('payment.config.dailyLimit')}
              min={0}
              fieldProps={{ precision: 2 }}
              extra={t('payment.config.unlimited')}
            />
            <ProFormDigit
              name="order_timeout_minutes"
              label={t('payment.config.orderTimeout')}
              min={1}
              rules={[{ required: true }]}
            />
            <ProFormDigit
              name="max_pending_orders"
              label={t('payment.config.maxPending')}
              min={1}
              rules={[{ required: true }]}
            />
          </ProFormGroup>
          <ProFormSelect
            name="enabled_payment_types"
            label={t('payment.config.enabledTypes')}
            mode="multiple"
            options={[
              { value: 'alipay', label: t('payment.methods.alipay') },
              { value: 'wxpay', label: t('payment.methods.wxpay') },
              { value: 'stripe', label: t('payment.methods.stripe') },
              { value: 'airwallex', label: t('payment.methods.airwallex') },
            ]}
            extra={t('payment.config.enabledTypesExtra')}
          />
          <ProFormGroup>
            <ProFormSelect
              name="load_balance_strategy"
              label={t('payment.config.loadBalance')}
              options={[
                { value: 'round-robin', label: t('payment.config.roundRobin') },
                { value: 'least-amount', label: t('payment.config.leastAmount') },
              ]}
            />
            <ProFormText name="product_name_prefix" label={t('payment.config.productPrefix')} />
            <ProFormText name="product_name_suffix" label={t('payment.config.productSuffix')} />
          </ProFormGroup>

          <Divider orientation="left">{t('payment.config.visibleRoutes')}</Divider>
          <ProFormGroup>
            <ProFormSwitch
              name="visible_method_alipay_enabled"
              label={t('payment.config.showAlipay')}
            />
            <ProFormSelect
              name="visible_method_alipay_source"
              label={t('payment.config.alipaySource')}
              disabled={!alipayVisible}
              rules={
                alipayVisible
                  ? [{ required: true, message: t('payment.config.selectAlipaySource') }]
                  : undefined
              }
              options={[
                { value: '', label: t('payment.config.notSelected') },
                { value: 'official_alipay', label: t('payment.config.officialAlipay') },
                { value: 'easypay_alipay', label: t('payment.config.easyPayAlipay') },
              ]}
            />
            <ProFormSwitch
              name="visible_method_wxpay_enabled"
              label={t('payment.config.showWxpay')}
            />
            <ProFormSelect
              name="visible_method_wxpay_source"
              label={t('payment.config.wxpaySource')}
              disabled={!wxpayVisible}
              rules={
                wxpayVisible
                  ? [{ required: true, message: t('payment.config.selectWxpaySource') }]
                  : undefined
              }
              options={[
                { value: '', label: t('payment.config.notSelected') },
                { value: 'official_wxpay', label: t('payment.config.officialWxpay') },
                { value: 'easypay_wxpay', label: t('payment.config.easyPayWxpay') },
              ]}
            />
          </ProFormGroup>
          <ProFormText name="help_image_url" label={t('payment.config.helpImage')} />
          <ProFormTextArea
            name="help_text"
            label={t('payment.config.helpText')}
            fieldProps={{ rows: 3 }}
          />
        </ProForm>
      </ProCard>

      <ProCard bordered>
        {providers.isError ? (
          <Alert
            type="error"
            showIcon
            message={t('payment.providers.loadError')}
            action={
              <Button
                aria-label={t('common.retry')}
                size="small"
                onClick={() => providers.refetch()}
              >
                {t('common.retry')}
              </Button>
            }
            style={{ marginBottom: 16 }}
          />
        ) : null}
        <ProTable<PaymentProvider>
          rowKey="id"
          loading={providers.isLoading}
          dataSource={providers.data ?? []}
          locale={{ emptyText: t('payment.providers.empty') }}
          columns={columns}
          search={false}
          pagination={false}
          options={{ reload: () => providers.refetch(), density: false, setting: false }}
          headerTitle={t('payment.providers.title')}
          toolBarRender={() => [
            <Button
              key="new"
              type="primary"
              icon={<PlusOutlined />}
              aria-label={t('payment.providers.add')}
              disabled={writesDisabled}
              onClick={openCreate}
            >
              {t('payment.providers.add')}
            </Button>,
          ]}
        />
      </ProCard>

      <ModalForm<PaymentProviderFormValues>
        title={
          editing
            ? t('payment.providers.form.editTitle', undefined, { name: editing.name })
            : t('payment.providers.form.createTitle')
        }
        open={modalOpen}
        form={providerForm as unknown as ProFormInstance<PaymentProviderFormValues>}
        modalProps={{ destroyOnHidden: true, onCancel: () => setModalOpen(false) }}
        submitter={{
          submitButtonProps: {
            loading: createProvider.isPending || updateProvider.isPending,
            disabled: writesDisabled,
          },
        }}
        onFinish={saveProvider}
      >
        <Alert type="info" showIcon message={definition.description} style={{ marginBottom: 16 }} />
        <ProFormSelect
          name="provider_key"
          label={t('payment.providers.form.providerType')}
          options={providerOptions}
          disabled={!!editing}
          rules={[{ required: true }]}
          fieldProps={{
            onChange: (key: PaymentProviderKey) => {
              if (editing) return
              // rc-field-form deep-merges setFieldsValue objects, so reset the
              // provider-specific branches before applying the new defaults.
              providerForm.resetFields(['config', 'secrets', 'limits', 'clear_secrets'])
              providerForm.setFieldsValue(providerTypeDefaults(key))
            },
          }}
        />
        <ProFormText
          name="name"
          label={t('payment.providers.form.name')}
          rules={[{ required: true }]}
        />
        <ProFormGroup>
          <ProFormSwitch name="enabled" label={t('payment.providers.form.enabled')} />
          <ProFormDigit name="sort_order" label={t('payment.providers.form.sort')} min={0} />
          <ProFormSwitch name="refund_enabled" label={t('payment.providers.form.refundEnabled')} />
          <ProFormSwitch
            name="allow_user_refund"
            label={t('payment.providers.form.userRefund')}
            disabled={!refundEnabled}
          />
        </ProFormGroup>
        <ProFormSelect
          name="supported_types"
          label={t('payment.providers.form.supportedTypes')}
          mode="multiple"
          options={definition.methods}
          rules={[{ required: true }]}
        />
        {definition.modes ? (
          <ProFormSelect
            name="payment_mode"
            label={t('payment.providers.form.paymentMode')}
            options={definition.modes}
          />
        ) : null}

        <Divider orientation="left">{t('payment.providers.form.limits')}</Divider>
        <Typography.Paragraph type="secondary">
          {t('payment.providers.form.limitsHelp')}
        </Typography.Paragraph>
        {(providerKey === 'stripe' ? ['stripe'] : supportedTypes).map((method) => (
          <ProFormGroup key={method} title={t(`payment.methods.${method}`, method)}>
            <ProFormDigit
              name={['limits', method, 'singleMin']}
              label={t('payment.providers.form.singleMin')}
              min={0}
              fieldProps={{ precision: 2 }}
            />
            <ProFormDigit
              name={['limits', method, 'singleMax']}
              label={t('payment.providers.form.singleMax')}
              min={0}
              fieldProps={{ precision: 2 }}
            />
            <ProFormDigit
              name={['limits', method, 'dailyLimit']}
              label={t('payment.providers.form.dailyLimit')}
              min={0}
              fieldProps={{ precision: 2 }}
            />
          </ProFormGroup>
        ))}

        <Divider orientation="left">{t('payment.providers.form.publicConfig')}</Divider>
        {definition.config.map((field) => (
          <ProFormText
            key={field.key}
            name={['config', field.key]}
            label={field.label}
            placeholder={field.placeholder}
            rules={!providerEnabled || field.optional ? undefined : [{ required: true }]}
          />
        ))}

        <Divider orientation="left">{t('payment.providers.form.protectedCredentials')}</Divider>
        <Alert
          type="success"
          showIcon
          icon={<SafetyCertificateOutlined />}
          message={t('payment.providers.form.credentialNotice')}
          style={{ marginBottom: 16 }}
        />
        {definition.secrets.map((field) => {
          const configured =
            !!editing?.secret_configured?.[field.key] && !clearSecrets.includes(field.key)
          const props = {
            name: ['secrets', field.key],
            label: field.label,
            placeholder: configured
              ? t('payment.providers.form.credentialConfigured')
              : t('payment.providers.form.credentialMissing'),
            rules: providerEnabled && !configured ? [{ required: true }] : undefined,
          }
          return field.multiline ? (
            <ProFormTextArea key={field.key} {...props} fieldProps={{ rows: 4 }} />
          ) : (
            <ProFormText.Password key={field.key} {...props} />
          )
        })}
        {editing ? (
          <ProFormCheckbox.Group
            name="clear_secrets"
            label={t('payment.providers.form.clearCredentials')}
            disabled={providerEnabled}
            extra={providerEnabled ? t('payment.providers.form.disableBeforeClear') : undefined}
            options={definition.secrets
              .filter((field) => editing.secret_configured?.[field.key])
              .map((field) => ({
                value: field.key,
                label: t('payment.providers.form.clearCredential', undefined, {
                  name: field.label,
                }),
              }))}
          />
        ) : null}
        <Descriptions
          size="small"
          column={1}
          items={[
            { label: 'Webhook', children: webhookHint(providerKey) },
            {
              label: t('payment.providers.form.security'),
              children: t('payment.providers.form.securityDescription'),
            },
          ]}
        />
      </ModalForm>
    </Space>
  )
}

function webhookHint(key: PaymentProviderKey) {
  if (key === 'stripe' || key === 'airwallex') return t('payment.webhook.consoleHint')
  const path = key === 'wxpay' ? '/api/v1/payment/webhook/wxpay' : `/api/v1/payment/webhook/${key}`
  return t('payment.webhook.futureHint', undefined, { url: `${window.location.origin}${path}` })
}
