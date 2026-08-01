import { useEffect, useRef, useState } from 'react'
import { getToken } from './auth'

export interface LiveEvent {
  type: string
  user_id?: number
  payload?: unknown
  at: string
}

// useEventStream subscribes to the Core SSE endpoint. EventSource cannot set an
// Authorization header, so the token is passed as a query param; the server
// accepts it there for this endpoint. Returns the most recent events (newest
// first, capped) and the live-connection state.
export function useEventStream(max = 50): { events: LiveEvent[]; connected: boolean } {
  const [events, setEvents] = useState<LiveEvent[]>([])
  const [connected, setConnected] = useState(false)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const token = getToken()
    if (!token) return
    const url = `/api/v1/events/stream?access_token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef.current = es

    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)

    const handler = (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data) as LiveEvent
        setEvents((prev) => [ev, ...prev].slice(0, max))
      } catch {
        // ignore malformed frames
      }
    }
    // Named events (event: <type>) and default messages both carry JSON data.
    for (const t of [
      'health.snapshot',
      'upstream.auto_switch',
      'health.auto_switch', // legacy compatibility
      'account.balance_low',
      'instance.config_drift',
      'upstream.reconcile',
      'upstream.inventory_low',
      'upstream.inventory_recovered',
      'upstream.recovery',
    ]) {
      es.addEventListener(t, handler as EventListener)
    }
    es.onmessage = handler

    return () => {
      es.close()
      esRef.current = null
      setConnected(false)
    }
  }, [max])

  return { events, connected }
}
