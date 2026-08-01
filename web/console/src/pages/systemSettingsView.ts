export type SystemSettingsView = 'auth' | 'payment'

export function systemSettingsViewFromSearch(
  search: URLSearchParams,
  paymentsEnabled: boolean,
): SystemSettingsView {
  return paymentsEnabled && search.get('view') === 'payment' ? 'payment' : 'auth'
}

export function searchForSystemSettingsView(
  search: URLSearchParams,
  view: SystemSettingsView,
): URLSearchParams {
  const next = new URLSearchParams(search)
  if (view === 'payment') next.set('view', 'payment')
  else next.delete('view')
  return next
}
