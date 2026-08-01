import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Form, Input, Space, Typography } from 'antd'
import { Link } from 'react-router'
import { LockOutlined, MailOutlined, UserOutlined } from '@ant-design/icons'
import { endpoints } from '../api/endpoints'
import { setSession } from '../api/auth'
import { friendlyErrorMessage } from '../api/errors'
import { usePublicAuthConfig } from '../api/hooks'

declare global {
  interface Window {
    turnstile?: {
      render: (
        el: HTMLElement,
        options: {
          sitekey: string
          callback: (token: string) => void
          'expired-callback': () => void
          'error-callback': () => void
        },
      ) => string
      remove?: (widgetId: string) => void
    }
  }
}

const turnstileScript = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'

function TurnstileBox({ siteKey, onToken }: { siteKey: string; onToken: (token: string) => void }) {
  const ref = useRef<HTMLDivElement | null>(null)
  const widgetId = useRef<string | null>(null)

  useEffect(() => {
    if (!siteKey) return

    const renderWidget = () => {
      if (!ref.current || !window.turnstile || widgetId.current) return
      widgetId.current = window.turnstile.render(ref.current, {
        sitekey: siteKey,
        callback: onToken,
        'expired-callback': () => onToken(''),
        'error-callback': () => onToken(''),
      })
    }

    const existing = document.querySelector<HTMLScriptElement>(`script[src="${turnstileScript}"]`)
    const script = existing ?? document.createElement('script')
    if (window.turnstile) {
      renderWidget()
    } else {
      script.addEventListener('load', renderWidget)
      if (!existing) {
        script.src = turnstileScript
        script.async = true
        script.defer = true
        document.head.appendChild(script)
      }
    }

    return () => {
      script.removeEventListener('load', renderWidget)
      if (widgetId.current && window.turnstile?.remove) window.turnstile.remove(widgetId.current)
      widgetId.current = null
    }
  }, [onToken, siteKey])

  return <div ref={ref} style={{ minHeight: 70 }} />
}

export default function Register() {
  const [form] = Form.useForm()
  const { data: publicConfig, isLoading } = usePublicAuthConfig()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [turnstileToken, setTurnstileToken] = useState('')

  const turnstileRequired = Boolean(publicConfig?.turnstile_enabled)
  const turnstileMisconfigured = Boolean(
    publicConfig?.turnstile_enabled && !publicConfig.turnstile_site_key,
  )

  const onFinish = async (values: { email: string; password: string; display_name?: string }) => {
    if (turnstileRequired && !turnstileToken) {
      setError('请先完成人机验证')
      return
    }
    setLoading(true)
    setError('')
    try {
      const res = await endpoints.register({
        email: values.email,
        password: values.password,
        display_name: values.display_name,
        turnstile_token: turnstileToken,
      })
      setSession(res.token, res.user)
      window.location.assign('/onboarding')
    } catch (e) {
      setError(friendlyErrorMessage(e))
      setTurnstileToken('')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'linear-gradient(135deg, #f6ffed 0%, #e6f4ff 100%)',
        padding: 24,
      }}
    >
      <Card style={{ width: 440, boxShadow: '0 4px 24px rgba(0,0,0,0.08)' }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            注册
          </Typography.Title>
          <Typography.Text type="secondary">创建账号并进入控制台</Typography.Text>
        </div>
        {error && <Alert type="error" message={error} style={{ marginBottom: 16 }} showIcon />}
        {turnstileMisconfigured && (
          <Alert
            type="error"
            message="Turnstile 站点密钥未配置，请联系管理员。"
            style={{ marginBottom: 16 }}
            showIcon
          />
        )}
        {!isLoading && publicConfig && !publicConfig.registration_enabled ? (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            <Alert type="info" showIcon message="当前未开放自助注册，请联系管理员开通账号。" />
            <Button block size="large">
              <Link to="/login">返回登录</Link>
            </Button>
          </Space>
        ) : (
          <Form form={form} onFinish={onFinish} layout="vertical" requiredMark={false}>
            <Form.Item
              name="email"
              label="登录邮箱"
              rules={[
                { required: true, message: '请输入邮箱' },
                { type: 'email', message: '邮箱格式不正确' },
              ]}
            >
              <Input
                prefix={<MailOutlined />}
                placeholder="client@example.com"
                size="large"
                autoFocus
              />
            </Form.Item>
            <Form.Item
              name="display_name"
              label="用户名 / 昵称"
              rules={[{ max: 64, message: '最多 64 个字符' }]}
            >
              <Input prefix={<UserOutlined />} placeholder="用户名" size="large" />
            </Form.Item>
            <Form.Item
              name="password"
              label="密码"
              rules={[
                { required: true, message: '请输入密码' },
                { min: 8, message: '至少 8 个字符' },
              ]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="至少 8 个字符" size="large" />
            </Form.Item>
            {publicConfig?.registration_email_suffix_whitelist?.length ? (
              <Typography.Paragraph type="secondary" style={{ marginTop: -4, fontSize: 12 }}>
                允许注册邮箱后缀：{publicConfig.registration_email_suffix_whitelist.join(', ')}
              </Typography.Paragraph>
            ) : null}
            {turnstileRequired && publicConfig?.turnstile_site_key ? (
              <Form.Item>
                <TurnstileBox
                  siteKey={publicConfig.turnstile_site_key}
                  onToken={setTurnstileToken}
                />
              </Form.Item>
            ) : null}
            <Button
              type="primary"
              htmlType="submit"
              block
              size="large"
              loading={loading || isLoading}
              disabled={turnstileMisconfigured}
            >
              注册并进入控制台
            </Button>
            <Typography.Paragraph style={{ textAlign: 'center', marginTop: 16, marginBottom: 0 }}>
              已有账号？<Link to="/login">返回登录</Link>
            </Typography.Paragraph>
          </Form>
        )}
      </Card>
    </div>
  )
}
