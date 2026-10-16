// A fake server for page tests: fetch answers from fixtures, EventSource is
// a hand-driven stub. Tests mutate `db` and call `emit` to simulate change.
import { vi } from 'vitest'
import type { Alert, Change, PlumbixEvent, Repo, Run } from '../model'
import * as fx from './fixtures'

export interface FakeDB {
  repos: Repo[]
  changes: Change[]
  runs: Record<string, Run[]>
  alerts: Alert[]
  signedIn: boolean
  /** mergeResult decides what POST /merge answers: 'ok' or an error code. */
  mergeResult: 'ok' | 'source_moved' | 'target_moved' | 'push_rejected' | 'invalid_state'
  requests: string[]
}

export function freshDB(): FakeDB {
  return {
    repos: structuredClone(fx.repos),
    changes: structuredClone(fx.allChanges),
    runs: structuredClone(fx.runsByChange),
    alerts: structuredClone(fx.alerts),
    signedIn: true,
    mergeResult: 'ok',
    requests: [],
  }
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function error(status: number, code: string, message: string): Response {
  return json(status, { error: { code, message } })
}

export function fakeFetch(db: FakeDB): typeof fetch {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    const method = init?.method ?? 'GET'
    const path = url.replace(/^https?:\/\/[^/]+/, '')
    db.requests.push(`${method} ${path}`)
    if (path === '/api/v1/health') return json(200, { status: 'ok', version: 'test' })
    if (path === '/api/v1/sessions' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as { token: string }
      if (body.token === 'good') {
        db.signedIn = true
        return json(200, { status: 'signed_in', via: 'cookie', expires_at: '2026-11-15T09:30:00Z' })
      }
      if (body.token === 'used') return error(401, 'link_used', 'used')
      if (body.token === 'old') return error(401, 'link_expired', 'expired')
      return error(401, 'link_invalid', 'unknown')
    }
    if (!db.signedIn) return error(401, 'unauthorized', 'a valid API token or browser session is required')
    if (path === '/api/v1/session') return json(200, { status: 'signed_in', via: 'cookie', expires_at: '2026-11-15T09:30:00Z' })
    if (path === '/api/v1/sessions/current' && method === 'DELETE') {
      db.signedIn = false
      return new Response(null, { status: 204 })
    }
    if (path === '/api/v1/repos') return json(200, { repos: db.repos })
    if (path === '/api/v1/changes') return json(200, { changes: db.changes })
    if (path.startsWith('/api/v1/alerts')) return json(200, { alerts: db.alerts.filter((a) => path.includes('include_resolved') || !a.resolved_at) })
    const m = path.match(/^\/api\/v1\/changes\/([^/?]+)(?:\/([a-z]+))?(\?.*)?$/)
    if (m) {
      const [, id, sub, query] = m
      const c = db.changes.find((x) => x.id === id)
      if (!c) return error(404, 'not_found', `change ${id}`)
      if (!sub) return json(200, c)
      if (sub === 'runs') return json(200, { runs: db.runs[id] ?? [] })
      if (sub === 'diff') {
        const stat = (query ?? '').includes('stat=true')
        const files = [
          { path: 'internal/evidence/upload.go', status: 'modified', binary: false, additions: 24, deletions: 6, hunks: stat ? undefined : [] },
          { path: 'CHANGELOG.md', status: 'modified', binary: false, additions: 1, deletions: 0, hunks: stat ? undefined : [] },
        ]
        return json(200, { base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', head: '9f3c1e2cccccccccccccccccccccccccccccccccc', additions: 25, deletions: 6, truncated: false, files })
      }
      if (sub === 'merge' && method === 'POST') {
        if (c.state !== 'approved') return error(409, 'invalid_state', `change ${id} is ${c.state}`)
        if (db.mergeResult !== 'ok') return error(409, db.mergeResult, `refused: ${db.mergeResult}`)
        c.state = 'merged'
        c.merge = { commit: '5c0e7a3b9d21aaaaaaaaaaaaaaaaaaaaaaaaaaaa', previous: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', strategy: 'squash', run_id: 'run_8h2k4m1qz2bb', merged_at: new Date().toISOString() }
        c.updated_at = new Date().toISOString()
        return json(200, c)
      }
      if (sub === 'close' && method === 'POST') {
        if (c.state === 'merged' || c.state === 'closed') return error(409, 'invalid_state', 'final')
        c.state = 'closed'
        c.updated_at = new Date().toISOString()
        return json(200, c)
      }
      if (sub === 'validate' && method === 'POST') {
        c.state = 'running'
        c.updated_at = new Date().toISOString()
        const run: Run = { id: 'run_newvalidate1', repo_id: c.repo_id, change_id: c.id, branch: c.source_branch, iteration: (db.runs[id]?.length ?? 0) + 1, head: '', base: '', status: 'running', steps: [], started_at: new Date().toISOString() }
        db.runs[id] = [...(db.runs[id] ?? []), run]
        return json(202, run)
      }
    }
    return error(404, 'not_found', `no such endpoint ${path}`)
  }
}

/** FakeEventSource records listeners; tests push events with `emit`. */
export class FakeEventSource {
  static instances: FakeEventSource[] = []
  listeners: Record<string, ((ev: MessageEvent) => void)[]> = {}
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  constructor(public url: string) {
    FakeEventSource.instances.push(this)
    queueMicrotask(() => this.onopen?.())
  }
  addEventListener(type: string, fn: (ev: MessageEvent) => void) {
    ;(this.listeners[type] ??= []).push(fn)
  }
  close() {
    this.closed = true
  }
  emit(e: PlumbixEvent) {
    for (const fn of this.listeners[e.type] ?? []) fn(new MessageEvent(e.type, { data: JSON.stringify(e) }))
  }
}

let nextEventID = 1000

/** emit sends an event through every open stream. */
export function emit(partial: Partial<PlumbixEvent> & { type: PlumbixEvent['type'] }): void {
  const e: PlumbixEvent = { id: nextEventID++, time: new Date().toISOString(), payload: {}, ...partial }
  for (const es of FakeEventSource.instances) if (!es.closed) es.emit(e)
}

/** install stubs fetch and EventSource for one test and returns the db. */
export function install(): FakeDB {
  const db = freshDB()
  FakeEventSource.instances = []
  vi.stubGlobal('fetch', fakeFetch(db))
  vi.stubGlobal('EventSource', FakeEventSource)
  return db
}
