import type {
  IntelligenceAccuracy,
  IntelligenceOverviewFilter,
  IntelligenceWindow,
} from '../api/upstreamIntelligence'

export type IntelligenceTab =
  | 'overview'
  | 'rates'
  | 'changes'
  | 'opportunities'
  | 'margin'
  | 'links'
  | 'recommendations'
  | 'execution'
  | 'rollouts'

export interface IntelligenceLocation extends IntelligenceOverviewFilter {
  tab: IntelligenceTab
  userId?: number
  evidenceId?: string
}

const tabs = new Set<IntelligenceTab>([
  'overview',
  'rates',
  'changes',
  'opportunities',
  'margin',
  'links',
  'recommendations',
  'execution',
  'rollouts',
])
const windows = new Set<IntelligenceWindow>(['24h', '7d'])
const accuracies = new Set<IntelligenceAccuracy>([
  'exact',
  'derived',
  'estimated',
  'unknown',
  'unattributed',
])

export function readIntelligenceLocation(search: URLSearchParams): IntelligenceLocation {
  const rawTab = search.get('tab') as IntelligenceTab | null
  const rawWindow = search.get('window') as IntelligenceWindow | null
  const rawAccuracy = search.get('evidence') as IntelligenceAccuracy | null
  const rawUserId = Number(search.get('user_id'))
  return {
    tab: rawTab && tabs.has(rawTab) ? rawTab : 'overview',
    userId: Number.isSafeInteger(rawUserId) && rawUserId > 0 ? rawUserId : undefined,
    source_id: search.get('source_id') || undefined,
    model: search.get('model') || undefined,
    group: search.get('group') || undefined,
    provider: search.get('provider') || undefined,
    currency: search.get('currency') || undefined,
    window: rawWindow && windows.has(rawWindow) ? rawWindow : '24h',
    accuracy: rawAccuracy && accuracies.has(rawAccuracy) ? rawAccuracy : undefined,
    evidenceId: search.get('evidence_id') || undefined,
  }
}

export function writeIntelligenceLocation(location: IntelligenceLocation): URLSearchParams {
  const search = new URLSearchParams()
  const entries: Array<[string, string | number | undefined]> = [
    ['tab', location.tab],
    ['user_id', location.userId],
    ['source_id', location.source_id],
    ['model', location.model],
    ['group', location.group],
    ['provider', location.provider],
    ['currency', location.currency],
    ['window', location.window],
    ['evidence', location.accuracy],
    ['evidence_id', location.evidenceId],
  ]
  for (const [key, value] of entries) {
    if (value !== undefined && value !== '') search.set(key, String(value))
  }
  return search
}
