// Importing a repository from the Repositories page (WU-041, A35): from a
// local folder that has a remote, or by registering a remote and cloning it
// to a local folder. Each step reports on its own line; the token is sent
// once and never shown again.
import { useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import type { Repo, RepoFolder } from '../model'
import { repoName } from '../vm/changes'
import { detectRemote, remoteName } from '../vm/remote'

const HOSTS: { value: Repo['host']; label: string }[] = [
  { value: 'github', label: 'GitHub' },
  { value: 'azuredevops', label: 'Azure DevOps' },
  { value: 'git', label: 'Plain git (your SSH keys)' },
]

interface Step {
  text: string
  ok: boolean
}

/** errorText is what a failed request says, problems first. */
function errorText(err: unknown): string {
  if (err instanceof ApiError) {
    const problems = (err.details?.problems as { field: string; message: string }[] | undefined) ?? []
    return problems.length ? problems.map((p) => p.message).join('; ') : err.message
  }
  return err instanceof Error ? err.message : 'the request failed'
}

/** Registration is the form both tabs share: where the repository lives, how it merges, and its token. */
interface Registration {
  host: Repo['host']
  url: string
  branch: string
  merge: Repo['merge_strategy']
  ownToken: boolean
  token: string
}

function RegistrationFields({ reg, set, fixedRemote }: { reg: Registration; set: (r: Partial<Registration>) => void; fixedRemote: boolean }) {
  return (
    <>
      <div className="import-grid">
        {!fixedRemote && (
          <>
            <label>
              Host
              <select aria-label="Host" value={reg.host} onChange={(e) => set({ host: e.target.value as Repo['host'] })}>
                {HOSTS.map((h) => (
                  <option key={h.value} value={h.value}>
                    {h.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="wide">
              URL
              <input aria-label="URL" className="mono" value={reg.url} onChange={(e) => set({ url: e.target.value })} />
            </label>
          </>
        )}
        <label>
          Protected branch
          <input aria-label="Protected branch" className="mono" value={reg.branch} onChange={(e) => set({ branch: e.target.value })} />
        </label>
        <label>
          Merge
          <select aria-label="Merge" value={reg.merge} onChange={(e) => set({ merge: e.target.value as Repo['merge_strategy'] })}>
            <option value="squash">Squash</option>
            <option value="merge">Merge commit</option>
            <option value="rebase">Rebase</option>
          </select>
        </label>
      </div>
      {reg.host !== 'git' && (
        <fieldset className="import-token">
          <legend>Token</legend>
          <label>
            <input type="radio" checked={!reg.ownToken} onChange={() => set({ ownToken: false })} /> Use the host token
          </label>
          <label>
            <input type="radio" checked={reg.ownToken} onChange={() => set({ ownToken: true })} /> Its own token
          </label>
          {reg.ownToken && <input aria-label="Token value" type="password" autoComplete="off" value={reg.token} onChange={(e) => set({ token: e.target.value })} placeholder="a token that reads and writes this repository's code" />}
        </fieldset>
      )}
    </>
  )
}

/** run registers, stores the token, checks access and clones when asked, reporting each step. */
async function run(reg: Registration, dest: string | null, report: (s: Step) => void): Promise<boolean> {
  let repo: Repo
  try {
    repo = await api.registerRepo({ host: reg.host, url: reg.url.trim(), protected_branches: reg.branch.trim() ? [reg.branch.trim()] : undefined, merge_strategy: reg.merge })
    report({ text: `Registered ${repoName(repo)}`, ok: true })
  } catch (err) {
    report({ text: `Not registered: ${errorText(err)}`, ok: false })
    return false
  }
  const name = repoName(repo)
  try {
    if (reg.host !== 'git' && reg.ownToken && reg.token) {
      await api.setRepoToken(repo.id, reg.token)
      report({ text: 'Token stored', ok: true })
    }
    if (reg.host !== 'git') {
      const check = await api.checkRepoToken(repo.id)
      report({ text: `Access checked: ${check.message}`, ok: check.ok })
    }
    if (dest !== null) {
      const c = await api.cloneRepo(repo.id, dest.trim())
      report({ text: `Cloned to ${c.path}`, ok: true })
    }
  } catch (err) {
    report({ text: `Stopped: ${errorText(err)}. ${name} is registered; set its token on its card and try what is left.`, ok: false })
  }
  return true
}

const blank = (): Registration => ({ host: 'github', url: '', branch: 'main', merge: 'squash', ownToken: false, token: '' })

export function ImportPanel({ onImported }: { onImported: () => Promise<void> }) {
  const [tab, setTab] = useState<'folder' | 'clone'>('folder')
  const [path, setPath] = useState('')
  const [folder, setFolder] = useState<RepoFolder | null>(null)
  const [inspectError, setInspectError] = useState('')
  const [reg, setReg] = useState<Registration>(blank)
  const [remote, setRemote] = useState('')
  const [dest, setDest] = useState('')
  const [destEdited, setDestEdited] = useState(false)
  const [steps, setSteps] = useState<Step[]>([])
  const [busy, setBusy] = useState(false)
  const set = (r: Partial<Registration>) => setReg((cur) => ({ ...cur, ...r }))
  const detected = detectRemote(remote)

  async function inspect(e: FormEvent) {
    e.preventDefault()
    setInspectError('')
    setFolder(null)
    setSteps([])
    try {
      const f = await api.inspectFolder(path)
      setFolder(f)
      if (!f.problem && f.host) setReg({ ...blank(), host: f.host, url: f.url, branch: f.default_branch || 'main' })
    } catch (err) {
      setInspectError(errorText(err))
    }
  }

  async function submit(cloneTo: string | null, r: Registration) {
    setBusy(true)
    setSteps([])
    const add = (s: Step) => setSteps((cur) => [...cur, s])
    const registered = await run(r, cloneTo, add)
    setReg((cur) => ({ ...cur, token: '' }))
    if (registered) await onImported()
    setBusy(false)
  }

  const changeRemote = (v: string) => {
    setRemote(v)
    const d = detectRemote(v)
    if (d) set({ host: d.host, url: d.url })
    if (!destEdited) setDest(remoteName(v) ? `~/dev/${remoteName(v)}` : '')
  }

  return (
    <section className="import-panel" aria-label="Import a repository">
      <div className="segmented" role="tablist" aria-label="Import from">
        <button role="tab" aria-selected={tab === 'folder'} aria-pressed={tab === 'folder'} onClick={() => { setTab('folder'); setSteps([]) }}>
          Local folder
        </button>
        <button role="tab" aria-selected={tab === 'clone'} aria-pressed={tab === 'clone'} onClick={() => { setTab('clone'); setSteps([]); setReg(blank()) }}>
          Clone a remote
        </button>
      </div>
      {tab === 'folder' && (
        <>
          <p className="dim">A folder on this machine that already has a GitHub, Azure DevOps or SSH remote. Plumbix reads its git settings once and then works with the remote; it never changes the folder.</p>
          <form className="import-row" onSubmit={(e) => void inspect(e)}>
            <label className="wide">
              Folder
              <input aria-label="Folder" className="mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder="~/dev/myapp" />
            </label>
            <button className="secondary small" type="submit" disabled={!path.trim()}>
              Inspect
            </button>
          </form>
          {inspectError && <p className="status-text error">{inspectError}</p>}
          {folder && (
            <div className="import-found">
              <p className="mono dim2">
                {folder.path} · remote {folder.remote || 'none'}
              </p>
              {folder.problem ? (
                <p className="status-text error">{folder.problem}</p>
              ) : (
                <>
                  <p className="dim">{folder.has_pipeline ? 'Has .plumbix/pipeline.yaml.' : 'No .plumbix/pipeline.yaml yet: after importing, start one on the Pipelines page.'}</p>
                  <RegistrationFields reg={reg} set={set} fixedRemote={false} />
                  <div className="form-row">
                    <button className="primary" disabled={busy || !reg.url.trim() || (reg.ownToken && !reg.token)} onClick={() => void submit(null, reg)}>
                      Import
                    </button>
                  </div>
                </>
              )}
            </div>
          )}
        </>
      )}
      {tab === 'clone' && (
        <>
          <p className="dim">Registers a remote and clones it into a new or empty folder in your home directory.</p>
          <div className="import-grid">
            <label className="wide">
              Remote URL
              <input aria-label="Remote URL" className="mono" value={remote} onChange={(e) => changeRemote(e.target.value)} placeholder="https://github.com/you/repo" />
            </label>
            <span className="dim detected">
              {remote.trim() ? (detected ? `${HOSTS.find((h) => h.value === detected.host)!.label}${detected.url !== remote.trim() ? ` · registers ${detected.url}` : ''}` : 'not a GitHub, Azure DevOps or SSH remote') : ''}
            </span>
            <label className="wide">
              Destination folder
              <input aria-label="Destination folder" className="mono" value={dest} onChange={(e) => { setDest(e.target.value); setDestEdited(true) }} placeholder="~/dev/repo" />
            </label>
          </div>
          {detected && <RegistrationFields reg={reg} set={set} fixedRemote />}
          <div className="form-row">
            <button className="primary" disabled={busy || !detected || !dest.trim() || (reg.ownToken && !reg.token)} onClick={() => void submit(dest, { ...reg, host: detected!.host, url: detected!.url })}>
              Register and clone
            </button>
          </div>
        </>
      )}
      {steps.length > 0 && (
        <ul className="import-steps" aria-label="Import steps">
          {steps.map((s, i) => (
            <li key={i} className={s.ok ? '' : 'error'}>
              {s.text}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
