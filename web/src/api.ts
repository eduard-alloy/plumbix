// The API client. Every call goes to the same origin with the session
// cookie; a 401 anywhere means the browser is not signed in.
import type { Alert, Change, Diff, Repo, Run } from './model'

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: Record<string, unknown>,
  ) {
    super(message)
  }
}

export class Unauthorized extends ApiError {}

type Listener = () => void
const unauthorizedListeners = new Set<Listener>()

/** onUnauthorized registers a function called whenever a call gets a 401. */
export function onUnauthorized(fn: Listener): () => void {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; details?: Record<string, unknown> } } | null)?.error
    const code = err?.code ?? `http_${res.status}`
    const message = err?.message ?? `HTTP ${res.status}`
    if (res.status === 401) {
      for (const fn of unauthorizedListeners) fn()
      throw new Unauthorized(401, code, message, err?.details)
    }
    throw new ApiError(res.status, code, message, err?.details)
  }
  return data as T
}

export interface SessionStatus {
  status: 'signed_in'
  via: 'cookie' | 'token'
  expires_at?: string
}

export const api = {
  health: () => request<{ status: string; version: string }>('GET', '/api/v1/health'),
  session: () => request<SessionStatus>('GET', '/api/v1/session'),
  exchange: (token: string) => request<SessionStatus>('POST', '/api/v1/sessions', { token }),
  signOut: () => request<void>('DELETE', '/api/v1/sessions/current'),
  repos: () => request<{ repos: Repo[] }>('GET', '/api/v1/repos').then((r) => r.repos),
  changes: () => request<{ changes: Change[] }>('GET', '/api/v1/changes').then((r) => r.changes),
  change: (id: string) => request<Change>('GET', `/api/v1/changes/${encodeURIComponent(id)}`),
  runs: (id: string) => request<{ runs: Run[] }>('GET', `/api/v1/changes/${encodeURIComponent(id)}/runs`).then((r) => r.runs),
  diff: (id: string, stat: boolean) => request<Diff>('GET', `/api/v1/changes/${encodeURIComponent(id)}/diff${stat ? '?stat=true' : ''}`),
  merge: (id: string) => request<Change>('POST', `/api/v1/changes/${encodeURIComponent(id)}/merge`),
  close: (id: string) => request<Change>('POST', `/api/v1/changes/${encodeURIComponent(id)}/close`),
  validate: (id: string) => request<Run>('POST', `/api/v1/changes/${encodeURIComponent(id)}/validate`),
  alerts: (all = false) => request<{ alerts: Alert[] }>('GET', `/api/v1/alerts${all ? '?include_resolved=true' : ''}`).then((r) => r.alerts),
  resolveAlert: (id: string) => request<Alert>('POST', `/api/v1/alerts/${encodeURIComponent(id)}/resolve`),
}

/** stepLogUrl is where a step's raw log can be opened. */
export function stepLogUrl(runId: string, step: string): string {
  return `/api/v1/runs/${encodeURIComponent(runId)}/steps/${encodeURIComponent(step)}/log`
}
