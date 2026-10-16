// Settings: git host tokens and other secrets (write-only), the AI model
// and the global review guidance. Values never come back from the server.
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { SecretStatus, SettingsView } from '../model'
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
  const [view, setView] = useState<SettingsView | null>(null)
  const [model, setModel] = useState('')
  const [guidance, setGuidance] = useState('')
  const [status, setStatus] = useState<{ text: string; error: boolean } | null>(null)
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [addStatus, setAddStatus] = useState('')

  const reload = useCallback(async () => {
    const v = await api.settings()
    setView(v)
    setModel(v.ai_model)
    setGuidance(v.guidance)
  }, [])

  useEffect(() => {
    reload().catch(() => setView(null))
  }, [reload])

  async function saveGuidance(e: FormEvent) {
    e.preventDefault()
    setStatus(null)
    try {
      const v = await api.updateSettings({ ai_model: model, guidance })
      setView(v)
      setModel(v.ai_model)
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
        <p className="lead">The AI validator runs as a pipeline step with the key below. Guidance is handed to every validator as instructions from you, kept apart from repository content. The model reaches validator steps as PLUMBIX_AI_MODEL; leave it empty for the validator's default.</p>
        {aiKey && <SecretRow secret={aiKey} onChanged={reload} />}
        <form onSubmit={(e) => void saveGuidance(e)}>
          <div className="field">
            <label htmlFor="ai-model">Model</label>
            <input id="ai-model" value={model} onChange={(e) => setModel(e.target.value)} placeholder="claude-opus-5-5" disabled={!view} />
          </div>
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
