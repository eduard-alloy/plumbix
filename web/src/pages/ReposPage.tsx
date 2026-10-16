// Repositories: read-only in this release; registration happens in the CLI.
// Each row can open its review guidance, the one thing set here.
import { Fragment, useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { Repo } from '../model'
import { useStore } from '../store'
import { repoName } from '../vm/changes'

function GuidanceEditor({ repo }: { repo: Repo }) {
  const [text, setText] = useState<string | null>(null)
  const [status, setStatus] = useState('')
  useEffect(() => {
    api
      .repoSettings(repo.id)
      .then((s) => setText(s.guidance))
      .catch(() => setText(''))
  }, [repo.id])
  async function save(e: FormEvent) {
    e.preventDefault()
    setStatus('')
    try {
      const s = await api.updateRepoSettings(repo.id, text ?? '')
      setText(s.guidance)
      setStatus('Saved')
    } catch (err) {
      setStatus(err instanceof ApiError ? err.message : String(err))
    }
  }
  return (
    <form className="guidance-editor" onSubmit={(e) => void save(e)}>
      <label className="label" htmlFor={`guidance-${repo.id}`}>
        Guidance for {repoName(repo)}
      </label>
      <textarea id={`guidance-${repo.id}`} value={text ?? ''} onChange={(e) => setText(e.target.value)} rows={4} maxLength={4000} placeholder="What validators should pay attention to in this repository." disabled={text === null} />
      <div className="form-row">
        <button className="secondary small" type="submit" disabled={text === null}>
          Save
        </button>
        <span className="status-text">{status}</span>
      </div>
    </form>
  )
}

const HOST = { github: 'GitHub', azuredevops: 'Azure DevOps', git: 'git' }

function TokenEditor({ repo, onChanged }: { repo: Repo; onChanged: (set: boolean) => void }) {
  const name = repoName(repo)
  const [state, setState] = useState<{ set: boolean; updatedAt?: string } | null>(null)
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const load = async () => {
    try {
      const s = await api.repoSettings(repo.id)
      setState({ set: !!s.token_set, updatedAt: s.token_updated_at })
      onChanged(!!s.token_set)
    } catch {
      setState({ set: false })
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repo.id])
  const act = async (fn: () => Promise<void>) => {
    setBusy(true)
    setResult(null)
    try {
      await fn()
    } catch (err) {
      setResult({ ok: false, text: err instanceof ApiError ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }
  async function save(e: FormEvent) {
    e.preventDefault()
    if (!value) return
    await act(async () => {
      await api.setRepoToken(repo.id, value)
      setValue('')
      setEditing(false)
      await load()
    })
  }
  const hostLabel = HOST[repo.host]
  return (
    <div className="token-editor" aria-label={`Token for ${name}`} role="group">
      <div className="token-state">
        {state === null ? '' : state.set ? `Own token${state.updatedAt ? ` · set ${agoLong(minutesBetween(state.updatedAt, new Date()))}` : ''}` : `Uses the ${hostLabel} token from Settings`}
        <span className="dim"> · a fine-grained token covers only the repositories chosen for it; give this one its own if the host token does not cover it.</span>
      </div>
      <div className="form-row">
        {!editing && (
          <button className="secondary small" onClick={() => setEditing(true)} disabled={busy}>
            {state?.set ? 'Replace' : 'Set'}
          </button>
        )}
        <button className="secondary small" onClick={() => void act(async () => setResult(await api.checkRepoToken(repo.id).then((c) => ({ ok: c.ok, text: c.message }))))} disabled={busy}>
          Check
        </button>
        {state?.set && (
          <button className="quiet" onClick={() => void act(async () => { await api.removeRepoToken(repo.id); await load() })} disabled={busy}>
            Remove
          </button>
        )}
        {result && (
          <span className={result.ok ? 'status-text' : 'status-text error'}>{result.text}</span>
        )}
      </div>
      {editing && (
        <form className="edit" onSubmit={(e) => void save(e)}>
          <input type="password" autoComplete="off" aria-label={`Token value for ${name}`} value={value} onChange={(e) => setValue(e.target.value)} placeholder="Paste the token; it is stored on the server and never shown again" autoFocus />
          <button className="primary" type="submit" disabled={busy || !value} style={{ height: 28 }}>
            Save
          </button>
          <button className="quiet" type="button" onClick={() => { setEditing(false); setValue('') }}>
            Cancel
          </button>
        </form>
      )}
    </div>
  )
}

export function ReposPage() {
  const { state } = useStore()
  const changes = Object.values(state.changes)
  const [open, setOpen] = useState<{ id: string; what: 'guidance' | 'token' } | null>(null)
  const [ownToken, setOwnToken] = useState<Record<string, boolean>>({})
  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Repositories</h1>
          <span className="count mono">{state.loaded ? state.repos.length : ''}</span>
        </div>
      </div>
      {state.loaded && state.repos.length === 0 && (
        <div className="empty">
          <h2>No repositories yet</h2>
          <p>In this release, repositories are registered from the terminal. They appear here as soon as you add one.</p>
          <pre>
            <span className="dim">$ </span>plumbix repo add --host github --url https://github.com/you/repo
          </pre>
        </div>
      )}
      {state.loaded && state.repos.length > 0 && (
        <div className="repos">
          <table className="repo-table">
            <thead>
              <tr>
                <th>Repository</th>
                <th>Host</th>
                <th>URL</th>
                <th>Protected branches</th>
                <th>Merge</th>
                <th className="right">Changes</th>
                <th>Token</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {state.repos.map((r) => (
                <Fragment key={r.id}>
                  <tr>
                    <td className="mono name" title={r.id}>
                      {repoName(r)}
                    </td>
                    <td className="dim2">{HOST[r.host]}</td>
                    <td className="mono dim2 url">{r.url}</td>
                    <td className="mono">{r.protected_branches.join(', ')}</td>
                    <td className="mono dim2">{r.merge_strategy}</td>
                    <td className="mono dim right">{changes.filter((c) => c.repo_id === r.id).length}</td>
                    <td className="dim2">{r.host === 'git' ? '—' : ownToken[r.id] ? 'Own token' : 'Host token'}</td>
                    <td className="right">
                      {r.host !== 'git' && (
                        <button className="quiet" aria-expanded={open?.id === r.id && open.what === 'token'} onClick={() => setOpen(open?.id === r.id && open.what === 'token' ? null : { id: r.id, what: 'token' })}>
                          Token
                        </button>
                      )}
                      <button className="quiet" aria-expanded={open?.id === r.id && open.what === 'guidance'} onClick={() => setOpen(open?.id === r.id && open.what === 'guidance' ? null : { id: r.id, what: 'guidance' })}>
                        Guidance
                      </button>
                    </td>
                  </tr>
                  {open?.id === r.id && (
                    <tr className="editor-row">
                      <td colSpan={8}>{open.what === 'guidance' ? <GuidanceEditor repo={r} /> : <TokenEditor repo={r} onChanged={(set) => setOwnToken((m) => ({ ...m, [r.id]: set }))} />}</td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
          <p className="footnote">
            Read-only. Register and edit repositories from the terminal with <span className="mono dim2">plumbix repo</span>.
          </p>
        </div>
      )}
    </>
  )
}
