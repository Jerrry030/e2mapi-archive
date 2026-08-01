import { Select } from 'antd'
import { useEffect } from 'react'
import { useUsers } from '../api/hooks'
import { currentUserId, getStoredUser, isPlatformAdmin } from '../api/auth'

// UserSelect is only visible to platform admins. Regular accounts are already
// scoped by the session, so showing a disabled self-selector adds noise.
export function UserSelect({
  value,
  onChange,
  placeholder = '全部账号',
  allowClear = true,
  style,
}: {
  value?: number
  onChange?: (v?: number) => void
  placeholder?: string
  allowClear?: boolean
  style?: React.CSSProperties
}) {
  const user = getStoredUser()
  const platform = isPlatformAdmin(user)
  const { data, isLoading } = useUsers(platform)
  const scopedUserId = !platform ? currentUserId(user) : undefined
  const effectiveValue = value ?? scopedUserId

  useEffect(() => {
    if (!value && scopedUserId) onChange?.(scopedUserId)
  }, [onChange, scopedUserId, value])

  const options = scopedUserId
    ? [
        {
          value: scopedUserId,
          label: user?.display_name || user?.email || scopedUserId,
        },
      ]
    : (data ?? []).map((item) => ({
        value: item.id,
        label: item.display_name || item.email,
      }))

  if (scopedUserId) return null

  return (
    <Select
      style={{ minWidth: 200, ...style }}
      loading={platform && isLoading}
      value={effectiveValue}
      onChange={onChange}
      allowClear={allowClear && !scopedUserId}
      disabled={Boolean(scopedUserId)}
      placeholder={placeholder}
      showSearch
      optionFilterProp="label"
      options={options}
    />
  )
}
