import { Empty, Tooltip } from 'antd'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import { getLocale } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

dayjs.extend(relativeTime)

export function RelativeTime({ value }: { value?: string | null }) {
  useLocaleVersion()
  if (!value) return <span style={{ color: '#bbb' }}>—</span>
  const d = dayjs(value).locale(getLocale() === 'zh' ? 'zh-cn' : 'en')
  return <Tooltip title={d.format('YYYY-MM-DD HH:mm:ss')}>{d.fromNow()}</Tooltip>
}

export function AbsoluteTime({ value }: { value?: string | null }) {
  if (!value) return <span style={{ color: '#bbb' }}>—</span>
  return <span>{dayjs(value).format('YYYY-MM-DD HH:mm:ss')}</span>
}

export function EmptyTeach({ title, action }: { title: string; action?: React.ReactNode }) {
  return (
    <Empty
      image={Empty.PRESENTED_IMAGE_SIMPLE}
      description={<span style={{ color: '#888' }}>{title}</span>}
    >
      {action}
    </Empty>
  )
}
