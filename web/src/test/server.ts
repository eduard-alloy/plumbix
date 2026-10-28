// A fake server for page tests: fetch answers from fixtures, EventSource is
// a hand-driven stub. Tests mutate `db` and call `emit` to simulate change.
import { vi } from 'vitest'
import type { Alert, Change, Diff, PipelineFile, PlumbixEvent, Repo, Run, Reviewer, ReviewerProfile, ReviewerInput, SettingsView, SecretStatus, ImageInfo, ImageBuild, RepoFolder, RepoInput, GitHubReport } from '../model'
import * as fx from './fixtures'

export interface FakeDB {
  repos: Repo[]
  changes: Change[]
  runs: Record<string, Run[]>
  alerts: Alert[]
  reviewers: Reviewer[]
  signedIn: boolean
  /** mergeResult decides what POST /merge answers: 'ok' or an error code. */
  mergeResult: 'ok' | 'source_moved' | 'target_moved' | 'push_rejected' | 'push_denied' | 'invalid_state'
  /** refreshResult decides what POST /refresh answers for a Plumbix branch: 'ok' or an error code. */
  refreshResult: 'ok' | 'already_current' | 'merge_conflict' | 'push_denied' | 'push_rejected'
  requests: string[]
  secrets: Record<string, string>
  /** liveLog is what the running step's log holds; tests append lines. */
  liveLog: string[]
  secretUpdated: Record<string, string>
  secretChecks: Record<string, { ok: boolean; expires_at?: string; message: string }>
  settings: { guidance: string; defaults: { api: string; external: string } }
  /** images is what GET /images answers; imageBuilds records builds started. */
  images: ImageInfo[]
  imageBuilds: ImageBuild[]
  /** profiles are the reviewer profiles, as the server stores them (no key). */
  profiles: Omit<ReviewerProfile, 'key_set' | 'default' | 'family'>[]
  repoGuidance: Record<string, string>
  repoTokens: Record<string, string>
  repoChecks: Record<string, { ok: boolean; message: string }>
  /** A GitHub report to answer with instead of the default (WU-061). */
  githubReports: Record<string, GitHubReport>
  pipelines: Record<string, PipelineFile>
  /** proposals records every pipeline change the page posted. */
  proposals: { repo: string; branch: string; title: string; intent: string; content: string; target_branch?: string }[]
  proposeResult: 'ok' | 'branch_exists' | 'push_denied'
  /** folders answers POST /repos/inspect by the path typed (WU-041); clones records POST /repos/{id}/clone. */
  folders: Record<string, RepoFolder>
  clones: Record<string, string>
  /** publishes records POST /repos/publish bodies (WU-042), the token included, as the server receives it. */
  publishes: Record<string, unknown>[]
}

const KNOWN_SECRETS = [
  { key: 'PLUMBIX_GITHUB_TOKEN', purpose: 'Default token for every GitHub repository: contents read and write; fetches, pushes merges, checks expiry. A fine-grained token covers only the repositories chosen for it; a repository can carry its own token on the Repositories page', host: 'github' },
  { key: 'PLUMBIX_AZURE_DEVOPS_TOKEN', purpose: 'Default personal access token for every Azure DevOps repository, code read and write; a repository can carry its own token on the Repositories page', host: 'azuredevops' },
  { key: 'PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES', purpose: 'Expiry date of the Azure DevOps token, YYYY-MM-DD, for the warning a week before' },
  { key: 'ANTHROPIC_API_KEY', purpose: 'The Anthropic key an anthropic reviewer profile may name as its key secret; also read by a pipeline step that lists it' },
  { key: 'OPENAI_API_KEY', purpose: 'The key of an OpenAI-compatible vendor an openai reviewer profile may name as its key secret; also read by a pipeline step that lists it' },
]

const reviewerNameRe = /^[a-z0-9][a-z0-9-]{0,31}$/
const modelRe = /^[a-z0-9][a-z0-9.-]{0,80}$/
const familyOf = (kind: string): 'api' | 'external' => (kind === 'claude-code' ? 'external' : 'api')

function problems(list: { field: string; message: string }[]): Response {
  return json(400, { error: { code: 'invalid_request', message: `the request has ${list.length} problem(s)`, details: { problems: list } } })
}

function settingsView(db: FakeDB): SettingsView {
  const keyOf: Record<string, string[]> = {}
  for (const p of db.profiles) (keyOf[p.key_secret] ??= []).push(p.name)
  const purposeOf = (key: string, base: string) => (keyOf[key] ? `API key of reviewer profile ${keyOf[key].join(', ')}. ${base}` : base)
  const listed = new Set<string>()
  const secrets: SecretStatus[] = KNOWN_SECRETS.map((k) => {
    listed.add(k.key)
    const set = k.key in db.secrets
    return { key: k.key, set, known: true, purpose: purposeOf(k.key, k.purpose), host: k.host, updated_at: set ? db.secretUpdated[k.key] : undefined }
  })
  for (const key of [...new Set([...Object.keys(db.secrets), ...Object.keys(keyOf)])].sort()) {
    if (!listed.has(key)) secrets.push({ key, set: key in db.secrets, known: false, purpose: purposeOf(key, keyOf[key] ? '' : 'Named by a pipeline step\'s secrets list'), host: undefined, updated_at: db.secretUpdated[key] })
  }
  const reviewers: ReviewerProfile[] = [...db.profiles].sort((a, b) => a.name.localeCompare(b.name)).map((p) => ({ ...p, family: familyOf(p.kind), key_set: p.key_secret !== '' && p.key_secret in db.secrets, default: p.name === db.settings.defaults[familyOf(p.kind)] }))
  return { guidance: db.settings.guidance, defaults: { ...db.settings.defaults }, reviewers, secrets }
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
    reviewers: [],
    requests: [],
    secrets: {},
    liveLog: ['$ go test ./... -count=1'],
    secretUpdated: {},
    secretChecks: {},
    images: [
      { ref: 'plumbix-ai-validator:dev', present: true, created: '2026-10-16T21:20:49Z', size_bytes: 20_600_000, digest: 'sha256:1111111111111111', shipped: true, own: false, purpose: 'The AI validator run inside the pipeline.', status: 'unlabelled', used_by: [{ repo_id: 'r_ledgerapi001', repo: 'ledger-api', step: 'ai-grok', validator: true }] },
      { ref: 'plumbix-reference-validator:dev', present: true, created: '2026-10-18T10:00:00Z', size_bytes: 12_100_000, digest: 'sha256:2222222222222222', shipped: true, own: false, purpose: 'The deterministic validator.', version: '9ffe6c6', status: 'current', used_by: [{ repo_id: 'r_plumbix00001', repo: 'plumbix', step: 'validate', validator: true }] },
      { ref: 'golang:1.27', present: true, created: '2026-10-01T10:00:00Z', size_bytes: 820_000_000, digest: 'sha256:3333333333333333', shipped: false, own: false, status: 'present', used_by: [{ repo_id: 'r_plumbix00001', repo: 'plumbix', step: 'build', validator: false }, { repo_id: 'r_plumbix00001', repo: 'plumbix', step: 'test', validator: false }] },
      { ref: 'node:24-alpine', present: false, shipped: false, own: false, status: 'missing', used_by: [{ repo_id: 'r_ledgerapi001', repo: 'ledger-api', step: 'test', validator: false }] },
    ],
    imageBuilds: [],
    settings: { guidance: '', defaults: { api: 'claude-api', external: 'claude-code' } },
    profiles: [
      { name: 'claude-api', kind: 'anthropic', base_url: '', model: '', tools: 'read-only', effort: '', key_secret: 'ANTHROPIC_API_KEY', updated_at: '2026-10-18T08:00:00Z' },
      { name: 'claude-code', kind: 'claude-code', base_url: '', model: '', tools: 'read-only', effort: '', key_secret: '', updated_at: '2026-10-18T08:00:00Z' },
    ],
    repoGuidance: {},
    repoTokens: {},
    repoChecks: {},
    githubReports: {},
    pipelines: structuredClone(fx.pipelineFiles),
    proposals: [],
    proposeResult: 'ok',
    folders: {},
    clones: {},
    publishes: [],
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
    if (path === '/api/v1/repos' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as RepoInput
      if (db.repos.some((r) => r.url === body.url)) return error(409, 'conflict', `conflict: repo ${body.url} is already registered`)
      const repo: Repo = { id: `r_new${String(db.repos.length).padStart(9, '0')}`, host: body.host, url: body.url, protected_branches: body.protected_branches?.length ? body.protected_branches : ['main'], merge_strategy: body.merge_strategy ?? 'squash', pull_requests: body.pull_requests, merger: body.merger ?? (body.pull_requests ? 'github' : 'plumbix'), created_at: new Date().toISOString() }
      db.repos.push(repo)
      return json(201, repo)
    }
    if (path === '/api/v1/repos') return json(200, { repos: db.repos })
    if (path === '/api/v1/repos/inspect' && method === 'POST') {
      const { path: p } = JSON.parse(String(init?.body)) as { path: string }
      const f = db.folders[p]
      if (!f) return json(400, { error: { code: 'invalid_request', message: 'the path cannot be imported', details: { problems: [{ field: 'path', message: `${p} does not exist` }] } } })
      return json(200, f)
    }
    if (path === '/api/v1/repos/publish' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as { path: string; host: 'github' | 'azuredevops'; owner?: string; project?: string; name: string }
      db.publishes.push(body)
      const url = body.host === 'github' ? `https://github.com/${body.owner || 'eduard'}/${body.name}` : `https://dev.azure.com/${body.owner}/${body.project}/_git/${body.name}`
      return json(200, { url, host: body.host, default_branch: db.folders[body.path]?.default_branch ?? 'main', pushed: ['draft', 'main'] })
    }
    const clone = path.match(/^\/api\/v1\/repos\/([^/]+)\/clone$/)
    if (clone && method === 'POST') {
      const { dest } = JSON.parse(String(init?.body)) as { dest: string }
      db.clones[clone[1]] = dest
      return json(200, { path: dest.replace(/^~/, '/Users/me') })
    }
    if (path === '/api/v1/images' && method === 'GET') return json(200, { images: db.images, version: '9ffe6c6' })
    const imageBuild = path.match(/^\/api\/v1\/images\/([^/]+)\/build$/)
    if (imageBuild && method === 'POST') {
      const name = decodeURIComponent(imageBuild[1])
      const im = db.images.find((i) => i.ref === `${name}:dev` || i.ref === `${name}:latest` || i.ref === name)
      if (!im?.shipped && !im?.own) return error(400, 'invalid_request', `not an image Plumbix ships: ${name}: only the images Plumbix ships, or own images defined on the Images page, can be built here`)
      if (db.imageBuilds.some((b) => b.status === 'running')) return error(409, 'busy', 'a build is already running')
      const b: ImageBuild = { id: `ib_${db.imageBuilds.length + 1}`, image: im.ref, status: 'running', started_at: new Date().toISOString(), log: `plumbix: building ${im.ref}\n` }
      db.imageBuilds.push(b)
      return json(202, b)
    }
    const ownImage = path.match(/^\/api\/v1\/images\/own\/([^/]+)$/)
    if (ownImage && method === 'PUT') {
      const name = decodeURIComponent(ownImage[1])
      const body = JSON.parse(String(init?.body)) as { dockerfile: string }
      const bad: { field: string; message: string }[] = []
      if (!/^[a-z0-9][a-z0-9._-]{0,62}$/.test(name)) bad.push({ field: 'name', message: 'must be lowercase letters, digits, dots, dashes and underscores, starting with a letter or digit, at most 63 characters, no slash' })
      if (!body.dockerfile.trim()) bad.push({ field: 'dockerfile', message: 'is empty' })
      else if (!/^\s*FROM /im.test(body.dockerfile)) bad.push({ field: 'dockerfile', message: 'has no FROM instruction' })
      if (bad.length) return problems(bad)
      const ref = `${name}:latest`
      const existing = db.images.find((i) => i.ref === ref)
      const dockerfile = body.dockerfile.endsWith('\n') ? body.dockerfile : body.dockerfile + '\n'
      if (existing) {
        existing.dockerfile = dockerfile
        if (existing.present) existing.status = 'stale'
      } else db.images.push({ ref, present: false, shipped: false, own: true, dockerfile, status: 'missing', used_by: [] })
      return json(200, { images: db.images, version: '9ffe6c6' })
    }
    if (ownImage && method === 'DELETE') {
      const ref = `${decodeURIComponent(ownImage[1])}:latest`
      const i = db.images.findIndex((x) => x.ref === ref && x.own)
      if (i < 0) return error(404, 'not_found', 'no such own image')
      db.images.splice(i, 1)
      return new Response(null, { status: 204 })
    }
    const oneBuild = path.match(/^\/api\/v1\/images\/builds\/([^/]+)$/)
    if (oneBuild && method === 'GET') {
      const b = db.imageBuilds.find((x) => x.id === oneBuild[1])
      if (!b) return error(404, 'not_found', 'no such build')
      return json(200, b)
    }
    if (path === '/api/v1/settings' && method === 'GET') return json(200, settingsView(db))
    if (path === '/api/v1/settings' && method === 'PUT') {
      const body = JSON.parse(String(init?.body)) as { guidance?: string; default_reviewer?: string }
      if (body.default_reviewer !== undefined) {
        const p = db.profiles.find((x) => x.name === body.default_reviewer)
        if (!p) return problems([{ field: 'default_reviewer', message: `there is no reviewer profile called "${body.default_reviewer}"` }])
        db.settings.defaults[familyOf(p.kind)] = p.name
      }
      if (body.guidance !== undefined) db.settings.guidance = body.guidance.trim()
      return json(200, settingsView(db))
    }
    const profile = path.match(/^\/api\/v1\/settings\/reviewers\/([^/]+)$/)
    if (profile && method === 'PUT') {
      const name = decodeURIComponent(profile[1])
      const body = JSON.parse(String(init?.body)) as ReviewerInput
      const bad: { field: string; message: string }[] = []
      if (!reviewerNameRe.test(name)) bad.push({ field: 'name', message: 'must be lowercase letters, digits and dashes, start with a letter or digit, at most 32 characters' })
      if (body.kind !== 'anthropic' && body.kind !== 'openai' && body.kind !== 'claude-code' && body.kind !== 'claude-code-image') bad.push({ field: 'kind', message: 'must be anthropic, openai, claude-code or claude-code-image' })
      if (body.kind === 'claude-code-image' && body.base_url) bad.push({ field: 'base_url', message: 'is not for Claude Code in the pipeline' })
      if (body.kind === 'claude-code-image' && body.effort) bad.push({ field: 'effort', message: 'is not for Claude Code in the pipeline' })
      if (body.kind === 'claude-code' && body.key_secret) bad.push({ field: 'key_secret', message: 'is not for Claude Code: it uses the subscription of the machine running plumbix review, no key' })
      if (body.kind === 'claude-code' && body.base_url) bad.push({ field: 'base_url', message: 'is not for Claude Code' })
      const baseURL = body.kind === 'openai' ? (body.base_url ?? '').trim() : ''
      if (body.kind === 'openai' && !baseURL) bad.push({ field: 'base_url', message: 'is required for an OpenAI-compatible reviewer: the chat-completions base such as https://api.openai.com/v1' })
      else if (baseURL && !/^https?:\/\/\S+$/.test(baseURL)) bad.push({ field: 'base_url', message: `"${baseURL}" must be an http(s) URL such as https://api.openai.com/v1` })
      const model = (body.model ?? '').trim()
      if (model && !modelRe.test(model)) bad.push({ field: 'model', message: 'lowercase letters, digits, dots and dashes only' })
      const tools = body.tools ?? 'read-only'
      if (tools !== 'read-only' && tools !== 'full') bad.push({ field: 'tools', message: 'must be read-only or full' })
      const effort = body.effort ?? ''
      if (!['', 'low', 'medium', 'high'].includes(effort)) bad.push({ field: 'effort', message: "must be low, medium or high, or empty for the vendor's default" })
      if (effort && body.kind === 'claude-code') bad.push({ field: 'effort', message: "is not for Claude Code: the loop runs with the machine's own settings" })
      const keySecret = body.kind === 'claude-code' ? '' : (body.key_secret ?? '').trim() || `PLUMBIX_REVIEWER_${name.toUpperCase().replace(/-/g, '_')}_KEY`
      if (keySecret && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(keySecret)) bad.push({ field: 'key_secret', message: 'must be a secret name: letters, digits and underscores, not starting with a digit' })
      if (bad.length) return problems(bad)
      const stored = { name, kind: body.kind, base_url: baseURL, model, tools, effort, key_secret: keySecret, updated_at: new Date().toISOString() }
      const i = db.profiles.findIndex((p) => p.name === name)
      if (i >= 0) db.profiles[i] = stored
      else db.profiles.push(stored)
      return json(200, settingsView(db))
    }
    if (profile && method === 'DELETE') {
      const name = decodeURIComponent(profile[1])
      const i = db.profiles.findIndex((p) => p.name === name)
      if (i < 0) return error(404, 'not_found', `reviewer profile ${name}`)
      const [gone] = db.profiles.splice(i, 1)
      if (db.settings.defaults[familyOf(gone.kind)] === name) db.settings.defaults[familyOf(gone.kind)] = ''
      return new Response(null, { status: 204 })
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
      const repo = db.repos.find((r) => r.id === id)!
      if (method === 'PUT') {
        const body = JSON.parse(String(init?.body)) as { guidance?: string; pull_requests?: boolean; merger?: 'plumbix' | 'github' | '' }
        if (body.guidance !== undefined) db.repoGuidance[id] = body.guidance.trim()
        if (body.pull_requests !== undefined || body.merger !== undefined) {
          // The server's rules (change.normalizeMerging): a field left out stays; an empty merger takes the default.
          const pull_requests = body.pull_requests ?? repo.pull_requests ?? false
          const merger = body.merger === undefined ? repo.merger : body.merger === '' ? (pull_requests ? 'github' : 'plumbix') : body.merger
          const bad: { field: string; message: string }[] = []
          if (pull_requests && repo.host !== 'github') bad.push({ field: 'pull_requests', message: 'only GitHub repositories have pull requests Plumbix can watch' })
          if (merger === 'github' && repo.host !== 'github') bad.push({ field: 'merger', message: 'github merges only GitHub repositories' })
          else if (merger !== 'plumbix' && merger !== 'github') bad.push({ field: 'merger', message: 'must be plumbix or github' })
          if (bad.length) return problems(bad)
          repo.pull_requests = pull_requests
          repo.merger = merger
        }
      }
      return json(200, { guidance: db.repoGuidance[id] ?? '', token_set: id in db.repoTokens, token_updated_at: id in db.repoTokens ? new Date().toISOString() : undefined, pull_requests: repo.pull_requests ?? false, merger: repo.merger })
    }
    const repoGitHub = path.match(/^\/api\/v1\/repos\/([^/]+)\/github$/)
    if (repoGitHub && method === 'GET') {
      const repo = db.repos.find((r) => r.id === repoGitHub[1])
      if (!repo) return error(404, 'not_found', `repo ${repoGitHub[1]}`)
      if (repo.host !== 'github') return error(404, 'not_found', `no GitHub integration for a ${repo.host} repository`)
      if (db.githubReports[repo.id]) return json(200, db.githubReports[repo.id])
      // The report the server computes live (WU-061), in the server's own words: the App installed, the gate required on each protected branch, the webhook wired and polling too.
      const delivered = new Date(Date.now() - 120000).toISOString()
      return json(200, {
        app: { configured: true, slug: 'plumbix-gate', installed: true, install_url: 'https://github.com/apps/plumbix-gate/installations/new', message: 'App plumbix-gate is installed on this repository; check runs are posted as it' },
        gate: repo.protected_branches.map((branch) => ({ branch, required: true, known: true, message: `plumbix/gate is required on ${branch}: a merge past a red or waiting gate needs a bypass, which Plumbix records as gate_bypassed` })),
        intake: { mode: 'webhook', webhook_secret_set: true, last_delivery_at: delivered, poll_interval: '15s', message: `Webhook secret set: signed deliveries are accepted at POST /api/v1/hooks/github; last delivery for this repository at ${delivered}, and polling every 15s as well` },
      } satisfies GitHubReport)
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
    if (path === '/api/v1/reviewers') return json(200, { reviewers: db.reviewers })
    if (path.startsWith('/api/v1/alerts')) return json(200, { alerts: db.alerts.filter((a) => path.includes('include_resolved') || !a.resolved_at) })
    const log = path.match(/^\/api\/v1\/runs\/([^/]+)\/steps\/([^/]+)\/log$/)
    if (log) {
      const liveRun = Object.values(db.runs).flat().find((r) => r.id === log[1] && r.steps.some((s) => s.name === log[2] && s.status === 'running'))
      const text = liveRun ? db.liveLog.map((l) => l + '\n').join('') : log[2] === 'test' ? sampleLog : `$ ${log[2]}\nok\nstep ${log[2]} succeeded in 4s (exit 0)\n`
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
        const body = JSON.parse(String(init?.body)) as { reason: string; by?: string }
        const problems: { field: string; message: string }[] = []
        if (!body.reason?.trim()) problems.push({ field: 'reason', message: 'is required' })
        if (!body.by?.trim()) problems.push({ field: 'by', message: 'is required: name the person approving' })
        if (problems.length) return json(400, { error: { code: 'invalid_request', message: `the request has ${problems.length} problem(s)`, details: { problems } } })
        if (c.state !== 'needs_human' && c.state !== 'escalated') return error(409, 'invalid_state', `change ${c.id} is ${c.state}`)
        const head = (db.runs[c.id] ?? []).slice(-1)[0]?.head ?? ''
        c.state = 'approved'
        c.human_approval = { reason: body.reason.trim(), head, at: new Date().toISOString(), by: body.by!.trim(), source: 'plumbix' }
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
