import { useCallback, useEffect } from 'react'
import { Alert, Descriptions, Form, Input, Modal, Select, Typography } from 'antd'
import type { ConnectorStatus, ConnectorTask, ConnectorTaskExecutionResolution } from '../api/types'
import { t } from '../i18n'
import {
  buildConnectorExecutionResolutionInput,
  canBuildConfirmedAppliedResult,
  ConnectorExecutionResolutionValidationError,
  type ConnectorExecutionResolutionValues,
  taskNeedsOperatorRemoteID,
  validateExecutionEvidence,
  validateExecutionNonce,
  validateExecutionRemoteID,
} from '../pages/connectorExecutionResolution'

interface ConnectorExecutionResolutionModalProps {
  task: ConnectorTask | null
  connectorStatus?: ConnectorStatus
  submitting: boolean
  onClose: () => void
  onSubmit: (
    taskId: string,
    body: ReturnType<typeof buildConnectorExecutionResolutionInput>,
  ) => Promise<void>
}

export function ConnectorExecutionResolutionModal({
  task,
  connectorStatus,
  submitting,
  onClose,
  onSubmit,
}: ConnectorExecutionResolutionModalProps) {
  const [form] = Form.useForm<ConnectorExecutionResolutionValues>()
  const resolution = Form.useWatch('resolution', form)
  const appliedAvailable = task ? canBuildConfirmedAppliedResult(task) : false
  const needsRemoteID = Boolean(
    task && resolution === 'confirmed_applied' && taskNeedsOperatorRemoteID(task),
  )

  useEffect(() => {
    if (!task) {
      form.resetFields()
      return
    }
    form.setFieldsValue({
      resolution: 'confirmed_not_applied',
      lease_nonce: '',
      evidence_note: '',
      remote_id: '',
    })
  }, [form, task])

  const close = useCallback(() => {
    if (submitting) return
    form.resetFields()
    onClose()
  }, [form, onClose, submitting])

  useEffect(() => {
    if (!task || submitting) return
    const dismissOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      // Handle Escape explicitly at the capture boundary. This keeps keyboard
      // dismissal reliable across Ant Design/rc-dialog releases and guarantees
      // that closing only clears local form state; it never submits an invalid
      // or partially entered resolution.
      event.preventDefault()
      event.stopPropagation()
      close()
    }
    document.addEventListener('keydown', dismissOnEscape, true)
    return () => document.removeEventListener('keydown', dismissOnEscape, true)
  }, [close, submitting, task])

  const submit = async (values: ConnectorExecutionResolutionValues) => {
    if (!task) return
    try {
      const body = buildConnectorExecutionResolutionInput(task, connectorStatus, values)
      await onSubmit(task.id, body)
      form.resetFields()
    } catch (error) {
      if (error instanceof ConnectorExecutionResolutionValidationError) {
        const field = error.code.startsWith('nonce')
          ? 'lease_nonce'
          : error.code.startsWith('evidence')
            ? 'evidence_note'
            : error.code.startsWith('remoteId')
              ? 'remote_id'
              : 'resolution'
        form.setFields([
          { name: field, errors: [t(`connectors.resolution.validation.${error.code}`)] },
        ])
        return
      }
      throw error
    }
  }

  const resolutionOptions = [
    {
      value: 'confirmed_applied' satisfies ConnectorTaskExecutionResolution,
      label: t('connectors.resolution.types.confirmedApplied'),
      disabled: !appliedAvailable,
    },
    {
      value: 'confirmed_not_applied' satisfies ConnectorTaskExecutionResolution,
      label: t('connectors.resolution.types.confirmedNotApplied'),
    },
    {
      value: 'connector_revoked_unverifiable' satisfies ConnectorTaskExecutionResolution,
      label: t('connectors.resolution.types.revokedUnverifiable'),
      disabled: connectorStatus !== 'revoked',
    },
  ]

  return (
    <Modal
      title={t('connectors.resolution.title')}
      open={Boolean(task)}
      width={{ xs: 'calc(100vw - 16px)', sm: 640 }}
      keyboard
      maskClosable={false}
      destroyOnHidden
      confirmLoading={submitting}
      okText={t('connectors.resolution.submit')}
      cancelText={t('common.cancel')}
      okButtonProps={{ danger: true, disabled: !task || submitting }}
      cancelButtonProps={{ disabled: submitting }}
      onOk={() => form.submit()}
      onCancel={close}
      styles={{ body: { maxHeight: '70vh', overflowY: 'auto' } }}
    >
      <Alert
        type="warning"
        showIcon
        message={t('connectors.resolution.warningTitle')}
        description={t('connectors.resolution.warningDescription')}
        style={{ marginBottom: 16 }}
      />
      {task ? (
        <Descriptions size="small" column={1} bordered style={{ marginBottom: 16 }}>
          <Descriptions.Item label={t('connectors.columns.taskId')}>{task.id}</Descriptions.Item>
          <Descriptions.Item label={t('connectors.columns.connectorId')}>
            {task.connector_id}
          </Descriptions.Item>
        </Descriptions>
      ) : null}
      <Form form={form} layout="vertical" requiredMark onFinish={submit}>
        <Form.Item
          name="resolution"
          label={t('connectors.resolution.type')}
          rules={[
            {
              required: true,
              message: t('connectors.resolution.validation.resolutionUnsupported'),
            },
          ]}
        >
          <Select options={resolutionOptions} />
        </Form.Item>

        {resolution === 'confirmed_applied' &&
        task?.type === 'gateway.account.traffic_share.set' ? (
          <Alert
            type="info"
            showIcon
            message={t('connectors.resolution.typedReceiptTitle')}
            description={t('connectors.resolution.trafficReceipt', undefined, {
              account: task.target_account_id ?? t('common.notAvailable'),
              weight: task.target_traffic_share ?? t('common.notAvailable'),
              scope: task.scheduling_fence?.scope ?? t('common.notAvailable'),
              version: task.scheduling_fence?.version ?? t('common.notAvailable'),
              sequence: task.scheduling_fence?.sequence ?? t('common.notAvailable'),
            })}
            style={{ marginBottom: 16 }}
          />
        ) : null}

        {needsRemoteID ? (
          <Form.Item
            name="remote_id"
            label={t('connectors.resolution.remoteId')}
            extra={t('connectors.resolution.remoteIdHelp')}
            validateTrigger={['onBlur', 'onSubmit']}
            rules={[
              {
                validator: async (_, value: string) => {
                  const code = validateExecutionRemoteID(value ?? '')
                  if (code) throw new Error(t(`connectors.resolution.validation.${code}`))
                },
              },
            ]}
          >
            <Input autoComplete="off" spellCheck={false} maxLength={128} />
          </Form.Item>
        ) : null}

        <Form.Item
          name="lease_nonce"
          label={t('connectors.resolution.leaseNonce')}
          extra={t('connectors.resolution.leaseNonceHelp')}
          validateTrigger={['onBlur', 'onSubmit']}
          rules={[
            {
              validator: async (_, value: string) => {
                const code = validateExecutionNonce(value ?? '')
                if (code) throw new Error(t(`connectors.resolution.validation.${code}`))
              },
            },
          ]}
        >
          <Input.Password
            visibilityToggle={false}
            autoComplete="off"
            spellCheck={false}
            maxLength={43}
            aria-describedby="connector-execution-nonce-help"
          />
        </Form.Item>
        <Typography.Paragraph
          id="connector-execution-nonce-help"
          type="secondary"
          style={{ marginTop: -16 }}
        >
          {t('connectors.resolution.leaseNonceSafety')}
        </Typography.Paragraph>

        <Form.Item
          name="evidence_note"
          label={t('connectors.resolution.evidenceNote')}
          extra={t('connectors.resolution.evidenceHelp')}
          validateTrigger={['onBlur', 'onSubmit']}
          rules={[
            {
              validator: async (_, value: string) => {
                const code = validateExecutionEvidence(value ?? '')
                if (code) throw new Error(t(`connectors.resolution.validation.${code}`))
              },
            },
          ]}
        >
          <Input.TextArea
            autoSize={{ minRows: 4, maxRows: 8 }}
            showCount
            maxLength={1000}
            placeholder={t('connectors.resolution.evidencePlaceholder')}
          />
        </Form.Item>
      </Form>
    </Modal>
  )
}
