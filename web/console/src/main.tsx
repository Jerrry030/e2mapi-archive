import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App as AntdApp, ConfigProvider } from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import { enUSIntl, ProConfigProvider, zhCNIntl } from '@ant-design/pro-components'
import { RouterProvider } from 'react-router/dom'
import { router } from './router'
import { getLocale } from './i18n'
import { useLocaleVersion } from './i18n/react'
import './index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 10_000, retry: 1, refetchOnWindowFocus: false },
  },
})

export function Root() {
  useLocaleVersion()
  const chinese = getLocale() === 'zh'
  const validateMessages = chinese
    ? {
        required: '请输入${label}',
        whitespace: '${label}不能只包含空格',
        types: {
          email: '请输入有效邮箱',
          url: '请输入有效地址',
        },
      }
    : {
        required: 'Enter ${label}',
        whitespace: '${label} cannot contain only spaces',
        types: {
          email: 'Enter a valid email address',
          url: 'Enter a valid URL',
        },
      }

  return (
    <ProConfigProvider intl={chinese ? zhCNIntl : enUSIntl}>
      <ConfigProvider
        locale={chinese ? zhCN : enUS}
        form={{ validateMessages }}
        theme={{ token: { colorPrimary: '#2f54eb' } }}
      >
        <QueryClientProvider client={queryClient}>
          <AntdApp>
            <RouterProvider router={router} />
          </AntdApp>
        </QueryClientProvider>
      </ConfigProvider>
    </ProConfigProvider>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
)
