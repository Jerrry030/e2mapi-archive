import { useEffect, useState } from 'react'
import { getLocale, onLocaleChange } from '.'

export function useLocaleVersion(): number {
  const [, setLocale] = useState(getLocale())
  const [version, setVersion] = useState(0)

  useEffect(
    () =>
      onLocaleChange(() => {
        setLocale(getLocale())
        setVersion((current) => current + 1)
      }),
    [],
  )

  return version
}
