import { useState } from 'react'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { Link } from 'react-router'
import { LockOutlined, MailOutlined } from '@ant-design/icons'
import { endpoints } from '../api/endpoints'
import { setSession } from '../api/auth'
import { friendlyErrorMessage } from '../api/errors'
import { usePublicAuthConfig } from '../api/hooks'

export default function Login() {
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const { data: publicConfig } = usePublicAuthConfig()

  const onFinish = async (values: { email: string; password: string }) => {
    setLoading(true)
    setError('')
    try {
      const res = await endpoints.login(values.email, values.password)
      setSession(res.token, res.user)
      const from = new URLSearchParams(window.location.search).get('from')
      window.location.assign(from && from.startsWith('/') ? from : '/')
    } catch (e) {
      setError(friendlyErrorMessage(e))
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
        background: 'linear-gradient(135deg, #f0f5ff 0%, #e6f4ff 100%)',
        padding: 24,
      }}
    >
      <Card style={{ width: 380, boxShadow: '0 4px 24px rgba(0,0,0,0.08)' }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Typography.Title level={3} style={{ marginBottom: 4 }}>
            E2M Ops 控制台
          </Typography.Title>
          <Typography.Text type="secondary">AI 网关运维托管平台</Typography.Text>
        </div>
        {error && <Alert type="error" message={error} style={{ marginBottom: 16 }} showIcon />}
        <Form onFinish={onFinish} layout="vertical" requiredMark={false}>
          <Form.Item
            name="email"
            rules={[
              { required: true, message: '请输入邮箱' },
              { type: 'email', message: '邮箱格式不正确' },
            ]}
          >
            <Input prefix={<MailOutlined />} placeholder="邮箱" size="large" autoFocus />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} placeholder="密码" size="large" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block size="large" loading={loading}>
            登录
          </Button>
        </Form>
        <div style={{ width: '100%', marginTop: 16 }}>
          {publicConfig?.registration_enabled ? (
            <Typography.Text>
              没有账号？<Link to="/register">去注册</Link>
            </Typography.Text>
          ) : (
            <Typography.Text type="secondary">账号自助注册未开放，请联系管理员。</Typography.Text>
          )}
        </div>
      </Card>
    </div>
  )
}
