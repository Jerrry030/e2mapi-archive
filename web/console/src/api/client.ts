// Same-origin fetch wrapper for the E2M Core API. Always calls relative URLs
// (/api/v1, /healthz); in dev these are proxied by Vite to the local core.
// Attaches the session token and bounces to /login on 401.

import { clearSession, getToken, redirectToLogin } from './auth'

const API_BASE = '/api/v1'

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
    this.name = 'ApiError'
  }
}

type Query = Record<string, string | number | boolean | undefined | null>

function buildQuery(query?: Query): string {
  if (!query) return ''
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue
    params.set(k, String(v))
  }
  const s = params.toString()
  return s ? `?${s}` : ''
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  query?: Query
  body?: unknown
  base?: string
  /** Skip the automatic login redirect on 401 (used by the login call itself). */
  noAuthRedirect?: boolean
  headers?: Record<string, string>
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const base = opts.base ?? API_BASE
  const url = `${base}${path}${buildQuery(opts.query)}`
  const headers: Record<string, string> = { ...opts.headers }
  if (opts.body) headers['Content-Type'] = 'application/json'
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  const res = await fetch(url, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  })

  const text = await res.text()
  const parsed = text ? JSON.parse(text) : null

  if (res.status === 401 && !opts.noAuthRedirect) {
    clearSession()
    redirectToLogin()
  }
  if (!res.ok) {
    const code = parsed?.code ?? 'error'
    const message = parsed?.message ?? res.statusText
    throw new ApiError(res.status, code, message)
  }
  return parsed as T
}

export const apiClient = { request }
