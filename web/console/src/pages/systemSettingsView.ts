export type SystemSettingsView = 'auth' | 'commerce' | 'payment'

export function systemSettingsViewFromSearch(
  search: URLSearchParams,
  paymentsEnabled: boolean,
): SystemSettingsView {
  const view = search.get('view')
  if (view === 'commerce') return 'commerce'
  if (paymentsEnabled && view === 'payment') return 'payment'
  return 'auth'
}

export function searchForSystemSettingsView(
  search: URLSearchParams,
  view: SystemSettingsView,
): URLSearchParams {
  const next = new URLSearchParams(search)
  if (view === 'auth') next.delete('view')
  else next.set('view', view)
  return next
}
