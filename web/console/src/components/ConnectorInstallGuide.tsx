import { useEffect, useRef } from 'react'
import { Alert, Descriptions, Modal, Space, Tag, Typography } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import { RelativeTime } from './common'
import type { Connector, CreateConnectorEnrollmentResult, Instance } from '../api/types'
import { t } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

interface ConnectorInstallModalProps {
  result?: CreateConnectorEnrollmentResult | null
  connectors?: Connector[]
  instances?: Instance[]
  onClose: () => void
  onRefresh?: () => void | Promise<unknown>
}

function CodeBlock({ text }: { text?: string }) {
  if (!text) return null
  return (
    <Typography.Paragraph copyable={{ text, icon: <CopyOutlined /> }}>
      <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>{text}</pre>
    </Typography.Paragraph>
  )
}

function shellQuote(value: string) {
  return `'${value.split("'").join(`'"'"'`)}'`
}

function tokenFileCommand(fileName: string, token: string) {
  return `printf %s ${shellQuote(token)} > ${shellQuote(fileName)} && chmod 600 ${shellQuote(fileName)}`
}

function connectorStatus(connector?: Connector) {
  if (!connector) return { color: 'default', label: t('connectorInstall.waiting') }
  if (connector.status === 'online') return { color: 'green', label: t('connectorInstall.online') }
  if (connector.status === 'revoked') return { color: 'red', label: t('connectorInstall.revoked') }
  return { color: 'default', label: t('connectorInstall.offline') }
}

export function ConnectorInstallModal({
  result,
  connectors,
  instances,
  onClose,
  onRefresh,
}: ConnectorInstallModalProps) {
  useLocaleVersion()
  const guide = result?.install_guide
  const connectorID = guide?.connector_id || result?.enrollment.connector_id || ''
  const instanceID = guide?.instance_id || result?.enrollment.instance_id || ''
  const connector = connectors?.find((item) => item.connector_id === connectorID)
  const instance = instances?.find((item) => item.id === instanceID)
  const status = connectorStatus(connector)
  const localConfigPort = guide?.local_config_port || 18081
  const localConfigURL = guide?.local_config_url || `http://127.0.0.1:${localConfigPort}`
  const containerName = guide?.container_name || 'e2m-agent'
  const tokenFile = guide?.enrollment_token_file || 'e2m-connector-enrollment-token'
  const refreshRef = useRef(onRefresh)

  useEffect(() => {
    refreshRef.current = onRefresh
  }, [onRefresh])

  useEffect(() => {
    if (!result || !refreshRef.current) return
    void refreshRef.current()
    const timer = window.setInterval(() => void refreshRef.current?.(), 3000)
    return () => window.clearInterval(timer)
  }, [connectorID, result])

  return (
    <Modal
      title={t('connectorInstall.title')}
      open={!!result}
      onCancel={onClose}
      footer={null}
      width={900}
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="warning" showIcon message={t('connectorInstall.tokenWarning')} />

        {guide?.warnings?.length ? (
          <Alert
            type="warning"
            showIcon
            message={t('connectorInstall.warningTitle')}
            description={
              <Space direction="vertical" size={4}>
                {guide.warnings.map((warning) => (
                  <Typography.Text key={warning}>{warning}</Typography.Text>
                ))}
              </Space>
            }
          />
        ) : null}

        <Descriptions size="small" column={2} bordered>
          <Descriptions.Item label={t('connectorInstall.connector')}>
            {connectorID || t('common.notAvailable')}
          </Descriptions.Item>
          <Descriptions.Item label={t('connectorInstall.instance')}>
            {instance?.name || instanceID || t('common.notAvailable')}
          </Descriptions.Item>
          <Descriptions.Item label={t('connectorInstall.dataVolume')}>
            {guide?.data_volume_name || t('common.notAvailable')}
          </Descriptions.Item>
          <Descriptions.Item label={t('connectorInstall.localPort')}>
            {localConfigPort}
          </Descriptions.Item>
          <Descriptions.Item label={t('connectorInstall.localConfig')} span={2}>
            {localConfigURL}
          </Descriptions.Item>
        </Descriptions>

        <section>
          <Typography.Title level={5}>{t('connectorInstall.tokenFile')}</Typography.Title>
          <Typography.Paragraph>{t('connectorInstall.tokenFileIntro')}</Typography.Paragraph>
          <CodeBlock text={result?.token ? tokenFileCommand(tokenFile, result.token) : undefined} />
        </section>

        <section>
          <Typography.Title level={5}>{t('connectorInstall.composeTitle')}</Typography.Title>
          <CodeBlock text={guide?.docker_compose_yaml} />
          <Typography.Text strong>{t('connectorInstall.composeStart')}</Typography.Text>
          <CodeBlock text="docker compose -f e2m-agent.compose.yml up -d" />
        </section>

        <section>
          <details>
            <summary>{t('connectorInstall.dockerRunTitle')}</summary>
            <CodeBlock text={guide?.docker_run_command || result?.install_command} />
          </details>
        </section>

        <section>
          <Typography.Title level={5}>{t('connectorInstall.localConfig')}</Typography.Title>
          <Typography.Paragraph>{t('connectorInstall.openLocalConfig')}</Typography.Paragraph>
          <CodeBlock
            text={`LOCAL_UI_TOKEN=$(docker exec ${shellQuote(containerName)} sh -c 'cat /var/lib/e2m-agent/local-ui.token') && printf '%s/#token=%s\\n' ${shellQuote(localConfigURL.replace(/\/$/, ''))} "$LOCAL_UI_TOKEN"`}
          />
          <Typography.Text strong>{t('connectorInstall.logsTitle')}</Typography.Text>
          <CodeBlock text={`docker logs ${containerName}`} />
        </section>

        <Alert
          type="info"
          showIcon
          message={t('connectorInstall.remoteTunnelTitle')}
          description={
            <Space direction="vertical" size={8} style={{ width: '100%' }}>
              <Typography.Text>{t('connectorInstall.remoteTunnelDescription')}</Typography.Text>
              <Typography.Text strong>{t('connectorInstall.remoteTunnelCommand')}</Typography.Text>
              <CodeBlock
                text={`ssh -N -L ${localConfigPort}:127.0.0.1:${localConfigPort} <user>@<server>`}
              />
            </Space>
          }
        />

        <Descriptions size="small" column={2} bordered>
          <Descriptions.Item label={t('connectors.columns.status')}>
            <Tag color={status.color}>{status.label}</Tag>
          </Descriptions.Item>
          <Descriptions.Item label={t('connectorInstall.lastSeen')}>
            {connector?.last_seen_at ? (
              <RelativeTime value={connector.last_seen_at} />
            ) : (
              t('connectorInstall.notSeen')
            )}
          </Descriptions.Item>
        </Descriptions>
      </Space>
    </Modal>
  )
}
