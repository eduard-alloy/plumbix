// Repositories: read-only in this release; registration happens in the CLI.
// Each row can open its review guidance, the one thing set here.
import { Fragment, useEffect, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
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

export function ReposPage() {
  const { state } = useStore()
  const changes = Object.values(state.changes)
  const [open, setOpen] = useState<string | null>(null)
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
                    <td className="right">
                      <button className="quiet" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? null : r.id)}>
                        Guidance
                      </button>
                    </td>
                  </tr>
                  {open === r.id && (
                    <tr className="editor-row">
                      <td colSpan={7}>
                        <GuidanceEditor repo={r} />
                      </td>
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
