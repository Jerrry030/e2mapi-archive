import { useEffect } from 'react'
import { Alert, Button, Drawer, Form, Select, Space, Spin, Switch } from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { useInstanceMonitorPolicy, useUpdateInstanceMonitorPolicy } from '../api/hooks'
import type { Instance, UpdateInstanceMonitorPolicyInput } from '../api/types'
import { t } from '../i18n'

const defaultPolicy: UpdateInstanceMonitorPolicyInput = {
  enabled: true,
  check_interval_seconds: 60,
  fail_streak: 2,
  auto_switch: false,
  cooldown_seconds: 300,
  drift_detection: true,
}

function editablePolicy(
  policy: UpdateInstanceMonitorPolicyInput,
): UpdateInstanceMonitorPolicyInput {
  return {
    enabled: policy.enabled,
    check_interval_seconds: policy.check_interval_seconds,
    fail_streak: policy.fail_streak,
    auto_switch: policy.auto_switch,
    cooldown_seconds: policy.cooldown_seconds,
    drift_detection: policy.drift_detection,
  }
}

export function InstanceMonitorPolicyDrawer({
  instance,
  onClose,
}: {
  instance: Instance | null
  onClose: () => void
}) {
  const [form] = Form.useForm<UpdateInstanceMonitorPolicyInput>()
  const policy = useInstanceMonitorPolicy(instance?.id, !!instance)
  const updatePolicy = useUpdateInstanceMonitorPolicy(instance?.id)
  const autoSwitch = Form.useWatch('auto_switch', form)

  useEffect(() => {
    if (!instance) return
    form.setFieldsValue(defaultPolicy)
  }, [form, instance])

  useEffect(() => {
    if (policy.data) form.setFieldsValue(editablePolicy(policy.data))
  }, [form, policy.data])

  const submit = async (values: UpdateInstanceMonitorPolicyInput) => {
    await updatePolicy.mutateAsync(values)
    onClose()
  }

  return (
    <Drawer
      title={t('monitorPolicy.title', undefined, { name: instance?.name ?? '' })}
      open={!!instance}
      width={420}
      onClose={onClose}
      destroyOnHidden
      footer={
        <Space style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={updatePolicy.isPending}
            disabled={policy.isLoading || policy.isError}
            onClick={() => form.submit()}
          >
            {t('common.save')}
          </Button>
        </Space>
      }
    >
      <Spin spinning={policy.isLoading}>
        {policy.isError ? (
          <Alert
            type="error"
            showIcon
            message={t('monitorPolicy.loadError')}
            action={
              <Button size="small" onClick={() => policy.refetch()}>
                {t('common.retry')}
              </Button>
            }
            style={{ marginBottom: 16 }}
          />
        ) : null}
        <Form
          form={form}
          layout="vertical"
          initialValues={defaultPolicy}
          onFinish={submit}
          disabled={policy.isLoading || policy.isError}
          requiredMark={false}
        >
          <Form.Item name="enabled" label={t('monitorPolicy.enabled')} valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="check_interval_seconds" label={t('monitorPolicy.checkInterval')}>
            <Select
              options={[
                { value: 30, label: t('monitorPolicy.intervals.30') },
                { value: 60, label: t('monitorPolicy.intervals.60') },
                { value: 300, label: t('monitorPolicy.intervals.300') },
              ]}
            />
          </Form.Item>
          <Form.Item name="fail_streak" label={t('monitorPolicy.failStreak')}>
            <Select
              options={[1, 2, 3, 4, 5].map((value) => ({
                value,
                label: t('monitorPolicy.failStreakOption', undefined, { count: value }),
              }))}
            />
          </Form.Item>
          <Form.Item
            name="auto_switch"
            label={t('monitorPolicy.autoSwitch')}
            valuePropName="checked"
          >
            <Switch />
          </Form.Item>
          {autoSwitch ? (
            <Alert
              type="warning"
              showIcon
              message={t('monitorPolicy.autoSwitchWarningTitle')}
              description={t('monitorPolicy.autoSwitchWarning')}
              style={{ marginBottom: 24 }}
            />
          ) : null}
          <Form.Item name="cooldown_seconds" label={t('monitorPolicy.cooldown')}>
            <Select
              options={[
                { value: 300, label: t('monitorPolicy.cooldowns.300') },
                { value: 900, label: t('monitorPolicy.cooldowns.900') },
                { value: 1800, label: t('monitorPolicy.cooldowns.1800') },
              ]}
            />
          </Form.Item>
          <Form.Item
            name="drift_detection"
            label={t('monitorPolicy.driftDetection')}
            valuePropName="checked"
          >
            <Switch />
          </Form.Item>
        </Form>
      </Spin>
    </Drawer>
  )
}
