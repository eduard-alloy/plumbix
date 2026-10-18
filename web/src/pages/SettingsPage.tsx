// Settings: git host tokens and other secrets (write-only), the AI model
// and the global review guidance. Values never come back from the server.
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { Reviewer, SecretStatus, SettingsView } from '../model'
import { useNow } from '../store'

function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
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
  const [model, setModel] = useState('')
  const [provider, setProvider] = useState<'anthropic' | 'openai'>('anthropic')
  const [baseURL, setBaseURL] = useState('')
  const [tools, setTools] = useState<'read-only' | 'full'>('read-only')
  const [guidance, setGuidance] = useState('')
  const [status, setStatus] = useState<{ text: string; error: boolean } | null>(null)
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [addStatus, setAddStatus] = useState('')

  const reload = useCallback(async () => {
    const v = await api.settings()
    setView(v)
    setModel(v.ai_model)
    setProvider(v.ai_provider)
    setBaseURL(v.ai_base_url)
    setTools(v.ai_tools)
    setGuidance(v.guidance)
  }, [])

  useEffect(() => {
    reload().catch(() => setView(null))
  }, [reload])

  async function saveGuidance(e: FormEvent) {
    e.preventDefault()
    setStatus(null)
    try {
      const v = await api.updateSettings({ ai_model: model, ai_provider: provider, ai_base_url: provider === 'openai' ? baseURL : '', ai_tools: tools, guidance })
      setView(v)
      setModel(v.ai_model)
      setProvider(v.ai_provider)
      setBaseURL(v.ai_base_url)
      setTools(v.ai_tools)
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
  const hostKeys = secrets.filter((s) => s.known && s.key !== 'ANTHROPIC_API_KEY')
  const aiKey = secrets.find((s) => s.key === 'ANTHROPIC_API_KEY')
  const custom = secrets.filter((s) => !s.known)

  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Settings</h1>
          <span className="count mono">{view ? `${secrets.filter((s) => s.set).length} secrets set` : ''}</span>
        </div>
      </div>
      <div className="settings">
        <h2>Git hosts</h2>
        <p className="lead">Tokens let Plumbix fetch private repositories, push merges and warn before a token expires. They are written to the server's secrets file, readable only by its owner, and used from the next fetch on. Nothing here shows a value again.</p>
        {hostKeys.map((s) => (
          <SecretRow key={s.key} secret={s} onChanged={reload} />
        ))}

        <h2>AI review</h2>
        <p className="lead">Guidance is handed to every validator as instructions from you, kept apart from repository content. The model applies to both kinds of AI review: the container validator reads it as PLUMBIX_AI_MODEL, and external reviewers (plumbix review --watch) pass it to Claude Code as --model. Leave it empty for each one's default: claude-opus-5-5 in the container, and whatever Claude Code defaults to on the reviewer's machine, which may be a larger model than this task needs.</p>
        {aiKey && <SecretRow secret={aiKey} onChanged={reload} />}
        <form onSubmit={(e) => void saveGuidance(e)}>
          <div className="field">
            <label htmlFor="ai-provider">Provider</label>
            <select id="ai-provider" value={provider} onChange={(e) => setProvider(e.target.value as 'anthropic' | 'openai')} disabled={!view}>
              <option value="anthropic">Anthropic (Claude, ANTHROPIC_API_KEY)</option>
              <option value="openai">OpenAI-compatible endpoint (OPENAI_API_KEY)</option>
            </select>
            <p className="hint">What the container AI validator asks. External reviewers use Claude Code either way.</p>
          </div>
          {provider === 'openai' && (
            <div className="field">
              <label htmlFor="ai-base-url">Base URL</label>
              <input id="ai-base-url" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.openai.com/v1" disabled={!view} />
              <p className="hint">
                The chat-completions base of the vendor whose key is in OPENAI_API_KEY. OpenAI https://api.openai.com/v1 · Gemini https://generativelanguage.googleapis.com/v1beta/openai · xAI https://api.x.ai/v1 · Moonshot https://api.moonshot.ai/v1 · DeepSeek https://api.deepseek.com · Groq https://api.groq.com/openai/v1 · Ollama http://localhost:11434/v1. The model below must be one that endpoint serves.
              </p>
            </div>
          )}
          <div className="field">
            <label htmlFor="ai-model">Model</label>
            <input id="ai-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder="claude-sonnet-5-5" disabled={!view} />
          </div>
          <fieldset className="field tools" disabled={!view}>
            <legend>Reviewer tools</legend>
            <p className="hint">For reviews done outside the pipeline (plumbix review --watch and the Claude Code validator). Read only lets the reviewer open files beyond the diff; full also lets it run commands in the checked-out code, which text in the repository could try to steer.</p>
            <label>
              <input type="radio" name="ai-tools" value="read-only" checked={tools === 'read-only'} onChange={() => setTools('read-only')} /> Read only
            </label>
            <label>
              <input type="radio" name="ai-tools" value="full" checked={tools === 'full'} onChange={() => setTools('full')} /> Full, including commands
            </label>
          </fieldset>
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

        <h2>External reviewers</h2>
        <p className="lead">Reviews for external validator steps are answered by plumbix review --watch running wherever Claude Code is signed in. Every poll is a heartbeat; this is who the server has heard from since it started.</p>
        <ReviewersList now={now} />
        <h2>Other secrets</h2>
        <p className="lead">Any name a pipeline step lists under secrets. Steps see them as environment variables; validators and other steps do not.</p>
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
