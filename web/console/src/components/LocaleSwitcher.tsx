import { Button, Dropdown } from 'antd'
import { GlobalOutlined } from '@ant-design/icons'
import { availableLocales, getLocale, setLocale, t, type LocaleCode } from '../i18n'
import { useLocaleVersion } from '../i18n/react'

export function LocaleSwitcher() {
  useLocaleVersion()
  const current = getLocale()
  const active = availableLocales.find((locale) => locale.code === current)
  const label = active?.name ?? current.toUpperCase()

  return (
    <Dropdown
      trigger={['click']}
      menu={{
        selectedKeys: [current],
        items: availableLocales.map((locale) => ({
          key: locale.code,
          label: locale.name,
          onClick: () => setLocale(locale.code as LocaleCode),
        })),
      }}
    >
      <Button
        className="e2m-locale-switcher"
        type="text"
        icon={<GlobalOutlined />}
        aria-label={t('common.language', undefined, { language: label })}
      >
        <span className="e2m-locale-label">{label}</span>
      </Button>
    </Dropdown>
  )
}
