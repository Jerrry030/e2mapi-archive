export type HealthView = 'operations' | 'summary'

export function healthViewFromSearch(search: URLSearchParams): HealthView {
  return search.get('view') === 'summary' ? 'summary' : 'operations'
}

export function searchForHealthView(search: URLSearchParams, view: HealthView): URLSearchParams {
  const next = new URLSearchParams(search)
  if (view === 'summary') next.set('view', 'summary')
  else next.delete('view')
  return next
}
