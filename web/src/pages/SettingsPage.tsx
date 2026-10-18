// Settings: reviewer profiles (WU-028), the review guidance, git host
// tokens and other secrets (write-only). Values never come back from the
// server.
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { PipelineStep, Repo, Reviewer, ReviewerKind, ReviewerProfile, ReviewerTools, SecretStatus, SettingsView } from '../model'
import { useNow } from '../store'

/** errorText renders a refusal: the problems the server named, else its message. */
function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const problems = (e.details?.problems as { field: string; message: string }[] | undefined) ?? []
    if (problems.length) return problems.map((p) => `${p.field} ${p.message}`).join('; ')
    return e.message
  }
  return e instanceof Error ? e.message : String(e)
}

/** Usage is which validator steps of which repositories a profile judges. */
interface Usage {
  repo: string
  steps: string[]
}

/** usageOf lists the validator steps naming the profile and, for the
 * default, the external steps naming none (those certainly use it; a
 * container validator's image does not say whether it is an AI one). */
function usageOf(profile: ReviewerProfile, pipelines: { repo: Repo; steps: PipelineStep[] }[]): Usage[] {
  const out: Usage[] = []
  for (const p of pipelines) {
    const steps = p.steps.filter((s) => s.validator && (s.reviewer === profile.name || (profile.default && !s.reviewer && s.external))).map((s) => s.name)
    if (steps.length) out.push({ repo: repoName(p.repo), steps })
  }
  return out
}

function repoName(r: Repo): string {
  return r.url.replace(/\.git$/, '').split('/').pop() ?? r.id
}

const vendorHints = 'OpenAI https://api.openai.com/v1 · Gemini https://generativelanguage.googleapis.com/v1beta/openai · xAI https://api.x.ai/v1 · Moonshot https://api.moonshot.ai/v1 · DeepSeek https://api.deepseek.com · Groq https://api.groq.com/openai/v1 · Ollama http://localhost:11434/v1'

function ProfileCard({ profile, secret, usage, onChanged }: { profile: ReviewerProfile; secret: SecretStatus; usage: Usage[]; onChanged: () => Promise<void> }) {
  const [kind, setKind] = useState<ReviewerKind>(profile.kind)
  const [baseURL, setBaseURL] = useState(profile.base_url)
  const [model, setModel] = useState(profile.model)
  const [tools, setTools] = useState<ReviewerTools>(profile.tools)
  const [keySecret, setKeySecret] = useState(profile.key_secret)
  const [status, setStatus] = useState<{ text: string; error: boolean } | null>(null)
  const [busy, setBusy] = useState(false)
  const dirty = kind !== profile.kind || baseURL !== profile.base_url || model !== profile.model || tools !== profile.tools || keySecret !== profile.key_secret

  async function run(label: string, fn: () => Promise<unknown>) {
    setBusy(true)
    setStatus(null)
    try {
      await fn()
      await onChanged()
      setStatus({ text: label, error: false })
    } catch (err) {
      setStatus({ text: errorText(err), error: true })
    } finally {
      setBusy(false)
    }
  }
  const id = (f: string) => `rp-${profile.name}-${f}`
  return (
    <div className="profile" aria-label={`Reviewer ${profile.name}`}>
      <div className="head">
        <span className="name">{profile.name}</span>
        <span className="kind">{kind === 'anthropic' ? 'Anthropic' : 'OpenAI-compatible'}</span>
        {profile.default && <span className="default-tag">default</span>}
      </div>
      <p className="usage">
        {usage.length ? `Judges ${usage.map((u) => `${u.repo}: ${u.steps.join(', ')}`).join(' · ')}${profile.default ? ', and every AI validator step that names no reviewer.' : '.'}` : profile.default ? 'Judges every AI validator step that names no reviewer.' : `No pipeline step names it. Add reviewer: ${profile.name} to a validator step.`}
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void run('Saved', () => api.putReviewer(profile.name, { kind, base_url: kind === 'openai' ? baseURL : '', model, tools, key_secret: keySecret }))
        }}
      >
        <div className="grid">
          <div className="field">
            <label htmlFor={id('kind')}>Kind</label>
            <select id={id('kind')} value={kind} onChange={(e) => setKind(e.target.value as ReviewerKind)}>
              <option value="anthropic">Anthropic (Claude)</option>
              <option value="openai">OpenAI-compatible endpoint</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor={id('model')}>Model</label>
            <input id={id('model')} value={model} onChange={(e) => setModel(e.target.value)} placeholder={kind === 'anthropic' ? 'claude-opus-5-5 in the container; Claude Code\'s own default outside' : 'the model the endpoint serves'} />
          </div>
          {kind === 'openai' && (
            <div className="field" style={{ gridColumn: '1 / -1' }}>
              <label htmlFor={id('base')}>Base URL</label>
              <input id={id('base')} value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.openai.com/v1" />
              <p className="hint">{vendorHints}</p>
            </div>
          )}
          <div className="field">
            <label htmlFor={id('key')}>Key secret</label>
            <input id={id('key')} value={keySecret} onChange={(e) => setKeySecret(e.target.value)} pattern="[A-Za-z_][A-Za-z0-9_]*" />
            <p className="hint">The secret that holds this reviewer's API key. Set its value below; it is never shown again.</p>
          </div>
        </div>
        <fieldset className="tools">
          <legend>Tools, where the reviewer has them (Claude Code)</legend>
          <label>
            <input type="radio" name={id('tools')} value="read-only" checked={tools === 'read-only'} onChange={() => setTools('read-only')} /> Read only
          </label>
          <label>
            <input type="radio" name={id('tools')} value="full" checked={tools === 'full'} onChange={() => setTools('full')} /> Full, including commands
          </label>
        </fieldset>
        <div className="actions">
          <button className="secondary small" type="submit" disabled={busy || !dirty}>
            Save reviewer
          </button>
          {!profile.default && (
            <button className="secondary small" type="button" disabled={busy} onClick={() => void run('Now the default', () => api.updateSettings({ default_reviewer: profile.name }))}>
              Make default
            </button>
          )}
          <span className="spacer" />
          <button className="quiet" type="button" disabled={busy} onClick={() => void run('Removed', () => api.deleteReviewer(profile.name))}>
            Delete
          </button>
          {status && <span className={status.error ? 'status-text error' : 'status-text'}>{status.text}</span>}
        </div>
      </form>
      <SecretRow secret={secret} onChanged={onChanged} />
    </div>
  )
}

function AddProfile({ onAdded }: { onAdded: () => Promise<void> }) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<ReviewerKind>('openai')
  const [baseURL, setBaseURL] = useState('')
  const [model, setModel] = useState('')
  const [status, setStatus] = useState('')
  async function add(e: FormEvent) {
    e.preventDefault()
    setStatus('')
    try {
      await api.putReviewer(name.trim(), { kind, base_url: kind === 'openai' ? baseURL : '', model })
      setName('')
      setBaseURL('')
      setModel('')
      await onAdded()
    } catch (err) {
      setStatus(errorText(err))
    }
  }
  return (
    <form className="add-profile" aria-label="Add a reviewer" onSubmit={(e) => void add(e)}>
      <h3>Add a reviewer</h3>
      <div className="grid">
        <div className="field">
          <label htmlFor="new-rp-name">Name</label>
          <input id="new-rp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="grok-4" pattern="[a-z0-9][a-z0-9-]{0,31}" />
        </div>
        <div className="field">
          <label htmlFor="new-rp-kind">Kind</label>
          <select id="new-rp-kind" value={kind} onChange={(e) => setKind(e.target.value as ReviewerKind)}>
            <option value="anthropic">Anthropic (Claude)</option>
            <option value="openai">OpenAI-compatible endpoint</option>
          </select>
        </div>
        {kind === 'openai' && (
          <div className="field">
            <label htmlFor="new-rp-base">Base URL</label>
            <input id="new-rp-base" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.x.ai/v1" />
          </div>
        )}
        <div className="field">
          <label htmlFor="new-rp-model">Model</label>
          <input id="new-rp-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder={kind === 'openai' ? 'grok-4' : 'claude-sonnet-5-5'} />
        </div>
      </div>
      {kind === 'openai' && <p className="hint">{vendorHints}</p>}
      <div className="form-row">
        <button className="secondary small" type="submit" disabled={!name.trim() || (kind === 'openai' && !baseURL.trim())}>
          Add reviewer
        </button>
        {status && <span className="status-text error">{status}</span>}
      </div>
    </form>
  )
}

function SecretRow({ secret, onChanged }: { secret: SecretStatus; onChanged: () => Promise<void> }) {
  const now = useNow()
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)

  async function save(e: FormEvent) {
    e.preventDefault()
    if (!value) return
    setBusy(true)
    setResult(null)
    try {
      await api.setSecret(secret.key, value)
      setValue('')
      setEditing(false)
      await onChanged()
    } catch (err) {
      setResult({ ok: false, text: errorText(err) })
    } finally {
      setBusy(false)
    }
  }
  async function remove() {
    setBusy(true)
    setResult(null)
    try {
      await api.removeSecret(secret.key)
      await onChanged()
    } catch (err) {
      setResult({ ok: false, text: errorText(err) })
    } finally {
      setBusy(false)
    }
  }
  async function check() {
    setBusy(true)
    setResult(null)
    try {
      const r = await api.checkSecret(secret.key)
      setResult({ ok: r.ok, text: r.message })
    } catch (err) {
      setResult({ ok: false, text: errorText(err) })
    } finally {
      setBusy(false)
    }
  }
  const when = secret.updated_at ? ` · ${agoLong(minutesBetween(secret.updated_at, now))}` : ''
  return (
    <div className="secret">
      <div>
        <div className="key">{secret.key}</div>
        <div className="purpose">{secret.purpose}</div>
        <div className="state" data-set={secret.set}>
          {secret.set ? `Set${when}` : 'Not set'}
        </div>
      </div>
      <div className="buttons">
        {secret.host && secret.set && (
          <button className="secondary small" onClick={() => void check()} disabled={busy}>
            Check
          </button>
        )}
        {!editing && (
          <button className="secondary small" onClick={() => setEditing(true)} disabled={busy}>
            {secret.set ? 'Replace' : 'Set'}
          </button>
        )}
        {secret.set && (
          <button className="quiet" onClick={() => void remove()} disabled={busy}>
            Remove
          </button>
        )}
      </div>
      {editing && (
        <form className="edit" onSubmit={(e) => void save(e)}>
          <input type="password" autoComplete="off" aria-label={`${secret.key} value`} value={value} onChange={(e) => setValue(e.target.value)} placeholder="Paste the value; it is stored on the server and never shown again" autoFocus />
          <button className="primary" type="submit" disabled={busy || !value} style={{ height: 28 }}>
            Save
          </button>
          <button
            className="quiet"
            type="button"
            onClick={() => {
              setEditing(false)
              setValue('')
            }}
          >
            Cancel
          </button>
        </form>
      )}
      {result && (
        <div className="result" data-ok={result.ok}>
          {result.text}
        </div>
      )}
    </div>
  )
}

export function SettingsPage() {
  const now = useNow()
  const [view, setView] = useState<SettingsView | null>(null)
  const [pipelines, setPipelines] = useState<{ repo: Repo; steps: PipelineStep[] }[]>([])
  const [guidance, setGuidance] = useState('')
  const [status, setStatus] = useState<{ text: string; error: boolean } | null>(null)
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [addStatus, setAddStatus] = useState('')

  const reload = useCallback(async () => {
    const v = await api.settings()
    setView(v)
    setGuidance(v.guidance)
  }, [])

  useEffect(() => {
    reload().catch(() => setView(null))
    // Which steps use which profile comes from each repository's pipeline file.
    api
      .repos()
      .then((repos) => Promise.all(repos.map((repo) => api.pipelineFile(repo.id).then((f) => ({ repo, steps: f.pipeline?.steps ?? [] })).catch(() => ({ repo, steps: [] as PipelineStep[] })))))
      .then(setPipelines)
      .catch(() => setPipelines([]))
  }, [reload])

  async function saveGuidance(e: FormEvent) {
    e.preventDefault()
    setStatus(null)
    try {
      const v = await api.updateSettings({ guidance })
      setView(v)
      setGuidance(v.guidance)
      setStatus({ text: 'Saved', error: false })
    } catch (err) {
      setStatus({ text: errorText(err), error: true })
    }
  }

  async function addSecret(e: FormEvent) {
    e.preventDefault()
    setAddStatus('')
    try {
      await api.setSecret(newKey.trim(), newValue)
      setNewKey('')
      setNewValue('')
      await reload()
    } catch (err) {
      setAddStatus(errorText(err))
    }
  }

  const secrets = view?.secrets ?? []
  const profiles = view?.reviewers ?? []
  const claimed = new Set(profiles.map((p) => p.key_secret))
  const hostKeys = secrets.filter((s) => s.host)
  const custom = secrets.filter((s) => !s.host && !claimed.has(s.key))
  const secretFor = (key: string): SecretStatus => secrets.find((s) => s.key === key) ?? { key, set: false, known: false, purpose: '' }

  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Settings</h1>
          <span className="count mono">{view ? `${profiles.length} reviewers · ${secrets.filter((s) => s.set).length} secrets set` : ''}</span>
        </div>
      </div>
      <div className="settings">
        <h2>AI reviewers</h2>
        <p className="lead">
          A reviewer profile is a model and how to reach it. An AI review runs one of two ways: a validator step with the plumbix-ai-validator image asks the profile's provider from inside the pipeline, with the profile's key; an external step (external: true) is answered by plumbix review --watch running Claude Code on a machine of yours, which takes the profile's model and tool policy. A validator step picks its profile with reviewer: &lt;name&gt; in the pipeline; steps that name none get the default. Two steps with two profiles run side by side.
        </p>
        {view && profiles.length === 0 && <p className="hint">No reviewer profiles yet. Add one below; validator steps that name none will use it once it is the default.</p>}
        <div className="profiles">
          {profiles.map((p) => (
            <ProfileCard key={p.name} profile={p} secret={secretFor(p.key_secret)} usage={usageOf(p, pipelines)} onChanged={reload} />
          ))}
        </div>
        {view && <AddProfile onAdded={reload} />}

        <h2>Review guidance</h2>
        <p className="lead">Handed to every validator as instructions from you, kept apart from repository content. A repository can add its own on the Repositories page.</p>
        <form onSubmit={(e) => void saveGuidance(e)}>
          <div className="field">
            <label htmlFor="guidance">Review guidance</label>
            <textarea id="guidance" value={guidance} onChange={(e) => setGuidance(e.target.value)} rows={5} maxLength={4000} placeholder="What validators should always pay attention to, across repositories." disabled={!view} />
          </div>
          <div className="form-row">
            <button className="secondary small" type="submit" disabled={!view}>
              Save guidance
            </button>
            {status && <span className={status.error ? 'status-text error' : 'status-text'}>{status.text}</span>}
          </div>
        </form>

        <h2>Git hosts</h2>
        <p className="lead">Tokens let Plumbix fetch private repositories, push merges and warn before a token expires. They are written to the server's secrets file, readable only by its owner, and used from the next fetch on. Nothing here shows a value again.</p>
        {hostKeys.map((s) => (
          <SecretRow key={s.key} secret={s} onChanged={reload} />
        ))}

        <h2>External reviewers</h2>
        <p className="lead">Reviews for external validator steps are answered by plumbix review --watch running wherever Claude Code is signed in. Every poll is a heartbeat; this is who the server has heard from since it started.</p>
        <ReviewersList now={now} />
        <h2>Other secrets</h2>
        <p className="lead">Any name a pipeline step lists under secrets, and keys no reviewer profile claims. Steps see them as environment variables; validators and other steps do not.</p>
        {custom.map((s) => (
          <SecretRow key={s.key} secret={s} onChanged={reload} />
        ))}
        <form className="add-secret" onSubmit={(e) => void addSecret(e)}>
          <input aria-label="New secret name" value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="NAME" pattern="[A-Za-z_][A-Za-z0-9_]*" />
          <input aria-label="New secret value" type="password" autoComplete="off" value={newValue} onChange={(e) => setNewValue(e.target.value)} placeholder="value" />
          <button className="secondary small" type="submit" disabled={!newKey || !newValue}>
            Add secret
          </button>
          {addStatus && <span className="status-text error">{addStatus}</span>}
        </form>
      </div>
    </>
  )
}

function ReviewersList({ now }: { now: Date }) {
  const [list, setList] = useState<Reviewer[] | null>(null)
  const [repoNames, setRepoNames] = useState<Record<string, string>>({})
  const load = useCallback(async () => {
    const [r, repos] = await Promise.all([api.reviewers(), api.repos().catch(() => [])])
    setList(r.reviewers)
    setRepoNames(Object.fromEntries(repos.map((x) => [x.id, x.url.replace(/\.git$/, '').split('/').pop() ?? x.id])))
  }, [])
  useEffect(() => {
    load().catch(() => setList([]))
  }, [load])
  return (
    <div className="reviewers">
      {list && list.length === 0 && (
        <p className="hint">
          No reviewer has polled since the server started. Run <code>plumbix review --watch</code> on a machine with Claude Code, or <code>plumbix review install</code> to keep one running there. An alert is raised when a review is published and nobody is listening.
        </p>
      )}
      {list && list.length > 0 && (
        <ul className="reviewer-list">
          {list.map((r) => (
            <li key={r.name}>
              <span className="mono">{r.name}</span>
              <span className="dim">{r.repo_id ? `${repoNames[r.repo_id] ?? r.repo_id} only` : 'every repository'}</span>
              <span className="dim">{agoLong(minutesBetween(r.last_seen, now))}</span>
            </li>
          ))}
        </ul>
      )}
      <div className="form-row">
        <button className="secondary small" type="button" onClick={() => void load()}>
          Refresh
        </button>
      </div>
    </div>
  )
}
