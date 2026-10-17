// A fake server for page tests: fetch answers from fixtures, EventSource is
// a hand-driven stub. Tests mutate `db` and call `emit` to simulate change.
import { vi } from 'vitest'
import type { Alert, Change, Diff, PipelineFile, PlumbixEvent, Repo, Run } from '../model'
import * as fx from './fixtures'

export interface FakeDB {
  repos: Repo[]
  changes: Change[]
  runs: Record<string, Run[]>
  alerts: Alert[]
  signedIn: boolean
  /** mergeResult decides what POST /merge answers: 'ok' or an error code. */
  mergeResult: 'ok' | 'source_moved' | 'target_moved' | 'push_rejected' | 'push_denied' | 'invalid_state'
  /** refreshResult decides what POST /refresh answers for a Plumbix branch: 'ok' or an error code. */
  refreshResult: 'ok' | 'already_current' | 'merge_conflict' | 'push_denied' | 'push_rejected'
  requests: string[]
  secrets: Record<string, string>
  secretUpdated: Record<string, string>
  secretChecks: Record<string, { ok: boolean; expires_at?: string; message: string }>
  settings: { ai_model: string; guidance: string }
  repoGuidance: Record<string, string>
  repoTokens: Record<string, string>
  repoChecks: Record<string, { ok: boolean; message: string }>
  pipelines: Record<string, PipelineFile>
  /** proposals records every pipeline change the page posted. */
  proposals: { repo: string; branch: string; title: string; intent: string; content: string; target_branch?: string }[]
  proposeResult: 'ok' | 'branch_exists' | 'push_denied'
}

const KNOWN_SECRETS = [
  { key: 'PLUMBIX_GITHUB_TOKEN', purpose: 'Default token for every GitHub repository: contents read and write; fetches, pushes merges, checks expiry. A fine-grained token covers only the repositories chosen for it; a repository can carry its own token on the Repositories page', host: 'github' },
  { key: 'PLUMBIX_AZURE_DEVOPS_TOKEN', purpose: 'Default personal access token for every Azure DevOps repository, code read and write; a repository can carry its own token on the Repositories page', host: 'azuredevops' },
  { key: 'PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES', purpose: 'Expiry date of the Azure DevOps token, YYYY-MM-DD, for the warning a week before' },
  { key: 'ANTHROPIC_API_KEY', purpose: 'Used by the AI validator step through the pipeline secrets list' },
]

function settingsView(db: FakeDB) {
  const listed = new Set<string>()
  const secrets = KNOWN_SECRETS.map((k) => {
    listed.add(k.key)
    const set = k.key in db.secrets
    return { key: k.key, set, known: true, purpose: k.purpose, host: k.host, updated_at: set ? db.secretUpdated[k.key] : undefined }
  })
  for (const key of Object.keys(db.secrets).sort()) {
    if (!listed.has(key)) secrets.push({ key, set: true, known: false, purpose: 'Named by a pipeline step\'s secrets list', host: undefined, updated_at: db.secretUpdated[key] })
  }
  return { ai_model: db.settings.ai_model, guidance: db.settings.guidance, secrets }
}

export function freshDB(): FakeDB {
  return {
    repos: structuredClone(fx.repos),
    changes: structuredClone(fx.allChanges),
    runs: structuredClone(fx.runsByChange),
    alerts: structuredClone(fx.alerts),
    signedIn: true,
    mergeResult: 'ok',
    refreshResult: 'ok',
    requests: [],
    secrets: {},
    secretUpdated: {},
    secretChecks: {},
    settings: { ai_model: '', guidance: '' },
    repoGuidance: {},
    repoTokens: {},
    repoChecks: {},
    pipelines: structuredClone(fx.pipelineFiles),
    proposals: [],
    proposeResult: 'ok',
  }
}

/** diffWithHunks is the approved change's diff: two files, one with a hunk
 * that holds the minor finding's line 71. */
export function diffWithHunks(): Diff {
  return {
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    head: '9f3c1e2cccccccccccccccccccccccccccccccccc',
    additions: 4,
    deletions: 1,
    truncated: false,
    files: [
      {
        path: 'internal/evidence/upload.go',
        status: 'modified',
        binary: false,
        additions: 3,
        deletions: 1,
        hunks: [
          {
            old_start: 68,
            old_lines: 6,
            new_start: 68,
            new_lines: 8,
            header: 'func (u *Uploader) put(ctx context.Context, key string, body []byte) error {',
            lines: [
              { type: 'context', old: 68, new: 68, text: '\tfor attempt := 1; ; attempt++ {' },
              { type: 'context', old: 69, new: 69, text: '\t\terr := u.try(ctx, key, body)' },
              { type: 'del', old: 70, text: '\t\tif err == nil { return nil }' },
              { type: 'add', new: 70, text: '\t\tif err == nil {' },
              { type: 'add', new: 71, text: '\t\t\tdelay = min(delay*2, 8*time.Second)' },
              { type: 'add', new: 72, text: '\t\t}' },
              { type: 'context', old: 71, new: 73, text: '\t\tif attempt == 3 {' },
              { type: 'context', old: 72, new: 74, text: '\t\t\treturn err' },
              { type: 'context', old: 73, new: 75, text: '\t\t}' },
            ],
          },
        ],
      },
      { path: 'CHANGELOG.md', status: 'modified', binary: false, additions: 1, deletions: 0, hunks: [{ old_start: 1, old_lines: 2, new_start: 1, new_lines: 3, header: '', lines: [{ type: 'context', old: 1, new: 1, text: '# Changelog' }, { type: 'add', new: 2, text: '- Retry failed artifact uploads' }, { type: 'context', old: 2, new: 3, text: '' }] }] },
    ],
  }
}

const sampleLog = ['$ go test ./... -count=1', 'ok  \tgithub.com/plumbix/plumbix/internal/config\t0.402s', '--- FAIL: TestImportRejectsUnknownCurrency (0.00s)', '    currency_test.go:27: row 4: "eur" rejected, want accepted', 'FAIL', 'step test failed in 1m 58s (exit 1)'].join('\n') + '\n'

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
    if (path === '/api/v1/settings' && method === 'GET') return json(200, settingsView(db))
    if (path === '/api/v1/settings' && method === 'PUT') {
      const body = JSON.parse(String(init?.body)) as { ai_model?: string; guidance?: string }
      if (body.ai_model !== undefined) {
        if (body.ai_model && !/^[a-z0-9][a-z0-9.-]{0,80}$/.test(body.ai_model)) return error(400, 'invalid_request', `model name "${body.ai_model}": lowercase letters, digits, dots and dashes only`)
        db.settings.ai_model = body.ai_model
      }
      if (body.guidance !== undefined) db.settings.guidance = body.guidance.trim()
      return json(200, settingsView(db))
    }
    const secret = path.match(/^\/api\/v1\/settings\/secrets\/([A-Za-z_][A-Za-z0-9_]*)(\/check)?$/)
    if (secret) {
      const [, key, check] = secret
      if (check && method === 'POST') {
        if (!(key in db.secrets)) return json(200, { ok: false, message: `${key} is not set` })
        return json(200, db.secretChecks[key] ?? { ok: false, message: 'check failed: no network in tests' })
      }
      if (method === 'PUT') {
        const body = JSON.parse(String(init?.body)) as { value: string }
        if (!body.value) return error(400, 'invalid_request', 'a value is required')
        db.secrets[key] = body.value
        db.secretUpdated[key] = new Date().toISOString()
        return new Response(null, { status: 204 })
      }
      if (method === 'DELETE') {
        delete db.secrets[key]
        delete db.secretUpdated[key]
        return new Response(null, { status: 204 })
      }
    }
    const pipe = path.match(/^\/api\/v1\/repos\/([^/]+)\/pipeline(\/changes)?(\?.*)?$/)
    if (pipe) {
      const [, id, changes] = pipe
      if (!db.repos.some((r) => r.id === id)) return error(404, 'not_found', `repo ${id}`)
      if (!changes) return db.pipelines[id] ? json(200, db.pipelines[id]) : error(404, 'not_found', 'branch')
      if (method === 'POST') {
        const body = JSON.parse(String(init?.body)) as { branch: string; title: string; intent: string; content: string; target_branch?: string }
        if (db.proposeResult === 'branch_exists') return error(409, 'branch_exists', `branch exists: ${body.branch}`)
        if (db.proposeResult === 'push_denied') return error(409, 'push_denied', 'push denied; set a token for repository ' + id + ' in Settings and try again')
        db.proposals.push({ repo: id, ...body })
        const c: Change = { ...fx.running, id: 'c_pipeline0001', repo_id: id, source_branch: body.branch, target_branch: body.target_branch ?? 'main', title: body.title, intent: body.intent, state: 'running', submitted_by: 'pipeline editor' }
        db.changes.push(c)
        return json(201, c)
      }
    }
    const repoSettings = path.match(/^\/api\/v1\/repos\/([^/]+)\/settings$/)
    if (repoSettings) {
      const id = repoSettings[1]
      if (!db.repos.some((r) => r.id === id)) return error(404, 'not_found', `repo ${id}`)
      if (method === 'PUT') db.repoGuidance[id] = (JSON.parse(String(init?.body)) as { guidance: string }).guidance.trim()
      return json(200, { guidance: db.repoGuidance[id] ?? '', token_set: id in db.repoTokens, token_updated_at: id in db.repoTokens ? new Date().toISOString() : undefined })
    }
    const repoToken = path.match(/^\/api\/v1\/repos\/([^/]+)\/token(\/check)?$/)
    if (repoToken) {
      const [, id, check] = repoToken
      if (!db.repos.some((r) => r.id === id)) return error(404, 'not_found', `repo ${id}`)
      if (check && method === 'POST') return json(200, db.repoChecks[id] ?? { ok: id in db.repoTokens, message: id in db.repoTokens ? "fetched 3 branches with the repository's own token" : 'fetch failed: authentication required' })
      if (method === 'PUT') {
        const body = JSON.parse(String(init?.body)) as { value: string }
        if (!body.value) return error(400, 'invalid_request', 'a value is required')
        db.repoTokens[id] = body.value
        return new Response(null, { status: 204 })
      }
      if (method === 'DELETE') {
        delete db.repoTokens[id]
        return new Response(null, { status: 204 })
      }
    }
    if (path === '/api/v1/changes') return json(200, { changes: db.changes })
    const resolve = path.match(/^\/api\/v1\/alerts\/([^/]+)\/resolve$/)
    if (resolve && method === 'POST') {
      const a = db.alerts.find((x) => x.id === resolve[1])
      if (!a) return error(404, 'not_found', 'alert')
      a.resolved_at ??= new Date().toISOString()
      return json(200, a)
    }
    if (path.startsWith('/api/v1/alerts')) return json(200, { alerts: db.alerts.filter((a) => path.includes('include_resolved') || !a.resolved_at) })
    const log = path.match(/^\/api\/v1\/runs\/([^/]+)\/steps\/([^/]+)\/log$/)
    if (log) {
      const text = log[2] === 'test' ? sampleLog : `$ ${log[2]}\nok\nstep ${log[2]} succeeded in 4s (exit 0)\n`
      return new Response(text, { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8' } })
    }
    const m = path.match(/^\/api\/v1\/changes\/([^/?]+)(?:\/([a-z]+))?(\?.*)?$/)
    if (m) {
      const [, id, sub, query] = m
      const c = db.changes.find((x) => x.id === id)
      if (!c) return error(404, 'not_found', `change ${id}`)
      if (!sub) return json(200, c)
      if (sub === 'runs') return json(200, { runs: db.runs[id] ?? [] })
      if (sub === 'diff') {
        const d = diffWithHunks()
        if ((query ?? '').includes('stat=true')) for (const f of d.files) delete f.hunks
        return json(200, d)
      }
      if (sub === 'merge' && method === 'POST') {
        if (c.state !== 'approved') return error(409, 'invalid_state', `change ${id} is ${c.state}`)
        if (db.mergeResult !== 'ok') return error(409, db.mergeResult, `refused: ${db.mergeResult}`)
        c.state = 'merged'
        c.merge = { commit: '5c0e7a3b9d21aaaaaaaaaaaaaaaaaaaaaaaaaaaa', previous: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', strategy: 'squash', run_id: 'run_8h2k4m1qz2bb', merged_at: new Date().toISOString() }
        c.updated_at = new Date().toISOString()
        return json(200, c)
      }
      if (sub === 'approve' && method === 'POST') {
        const body = JSON.parse(String(init?.body)) as { reason: string }
        if (!body.reason?.trim()) return json(400, { error: { code: 'invalid_request', message: 'the request has 1 problem(s)', details: { problems: [{ field: 'reason', message: 'is required' }] } } })
        if (c.state !== 'needs_human' && c.state !== 'escalated') return error(409, 'invalid_state', `change ${c.id} is ${c.state}`)
        const head = (db.runs[c.id] ?? []).slice(-1)[0]?.head ?? ''
        c.state = 'approved'
        c.human_approval = { reason: body.reason.trim(), head, at: new Date().toISOString() }
        c.updated_at = new Date().toISOString()
        return json(200, c)
      }
      if (sub === 'close' && method === 'POST') {
        if (c.state === 'merged' || c.state === 'closed') return error(409, 'invalid_state', 'final')
        c.state = 'closed'
        c.updated_at = new Date().toISOString()
        return json(200, c)
      }
      if (sub === 'refresh' && method === 'POST') {
        if (c.state === 'merged' || c.state === 'closed' || c.state === 'running') return error(409, 'invalid_state', `change ${c.id} is ${c.state}`)
        if (!c.plumbix_branch) return error(409, 'not_plumbix_branch', `${c.source_branch} was pushed by its author, who brings ${c.target_branch} in`)
        if (db.refreshResult !== 'ok') return error(409, db.refreshResult, db.refreshResult === 'merge_conflict' ? 'merge conflict: .plumbix/pipeline.yaml' : `refused: ${db.refreshResult}`)
        c.state = 'running'
        c.human_approval = undefined
        c.updated_at = new Date().toISOString()
        const run: Run = { id: 'run_refreshed001', repo_id: c.repo_id, change_id: c.id, branch: c.source_branch, iteration: (db.runs[id]?.length ?? 0) + 1, head: '', base: '', status: 'running', steps: [], started_at: new Date().toISOString() }
        db.runs[id] = [...(db.runs[id] ?? []), run]
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

/** streamOpen resolves once the app has opened its event stream. */
export async function streamOpen(): Promise<void> {
  const deadline = Date.now() + 2000
  while (!FakeEventSource.instances.some((i) => !i.closed)) {
    if (Date.now() > deadline) throw new Error('the app never opened the event stream')
    await new Promise((r) => setTimeout(r, 10))
  }
}

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
