// Images: the container images the registered pipelines run, what the
// server knows about each, and builds of the two Plumbix ships (WU-032).
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { ImageBuild, ImageInfo, ImagesView } from '../model'
import { useNow } from '../store'

/** buildPoll is how often a running build's log is refetched (ms); tests shorten it. */
export const buildPoll = { every: 1500 }

/** errorText renders a refusal: the problems the server named, else its message. */
function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const problems = (e.details?.problems as { field: string; message: string }[] | undefined) ?? []
    if (problems.length) return problems.map((p) => `${p.field} ${p.message}`).join('; ')
    return e.message
  }
  return e instanceof Error ? e.message : String(e)
}

function size(bytes?: number): string {
  if (!bytes) return ''
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`
  if (bytes >= 1e6) return `${Math.round(bytes / 1e6)} MB`
  return `${Math.round(bytes / 1e3)} kB`
}

function statusText(im: ImageInfo, serverVersion: string, now: Date): { text: string; tone: 'ok' | 'warn' | 'bad' } {
  const built = im.created ? `built ${agoLong(minutesBetween(im.created, now))} ago` : ''
  const sz = size(im.size_bytes)
  const tail = [built, sz].filter(Boolean).join(' · ')
  switch (im.status) {
    case 'missing':
      if (im.own) return { text: 'Not built yet. Build it here before a step names it.', tone: 'bad' }
      return { text: im.shipped ? 'Not built on this machine. A step naming it fails until it is.' : 'Not present locally; Docker pulls it on first use.', tone: im.shipped ? 'bad' : 'warn' }
    case 'current':
      if (im.own) return { text: `Built from this Dockerfile · ${tail}`, tone: 'ok' }
      return { text: `Built by this server (${im.version}) · ${tail}`, tone: 'ok' }
    case 'stale':
      if (im.own) return { text: `The Dockerfile changed since the last build. Rebuild to apply it. · ${tail}`, tone: 'warn' }
      return { text: `Built by server ${im.version}, this one is ${serverVersion}. Rebuild to match. · ${tail}`, tone: 'warn' }
    case 'unlabelled':
      return { text: `Built outside Plumbix (docker build by hand); its version is unknown. Rebuild to label it. · ${tail}`, tone: 'warn' }
    default:
      return { text: `Present · ${tail}`, tone: 'ok' }
  }
}

/** starterDockerfile is what a new own image begins with. */
export const starterDockerfile = `# A step image: the commands of the step run inside it.
# For a validator, the ENTRYPOINT is the validator: it reads /plumbix/in
# and must write /plumbix/out/verdict.json (ARCHITECTURE.md, section 4.3).
FROM alpine:3.20
RUN apk add --no-cache git
`

/** withFinalNewline is how the server stores a Dockerfile, so edits compare like for like. */
const withFinalNewline = (s: string) => (s === '' || s.endsWith('\n') ? s : s + '\n')

function OwnEditor({ im, onChanged }: { im: ImageInfo; onChanged: () => Promise<void> }) {
  const [text, setText] = useState(im.dockerfile ?? '')
  const [status, setStatus] = useState<{ text: string; error: boolean } | null>(null)
  const [confirm, setConfirm] = useState(false)
  const name = im.ref.replace(/:latest$/, '')
  // The stored file is what the editor shows after a save or a reload.
  useEffect(() => setText(im.dockerfile ?? ''), [im.dockerfile])
  const dirty = withFinalNewline(text) !== (im.dockerfile ?? '')
  async function save() {
    setStatus(null)
    try {
      await api.putOwnImage(name, text)
      await onChanged()
      setStatus({ text: 'Saved. Build to apply it.', error: false })
    } catch (e) {
      setStatus({ text: errorText(e), error: true })
    }
  }
  async function remove() {
    setStatus(null)
    try {
      await api.deleteOwnImage(name)
      await onChanged()
    } catch (e) {
      setStatus({ text: errorText(e), error: true })
    }
  }
  const users = im.used_by.map((u) => `${u.repo}: ${u.step}`).join(', ')
  return (
    <div className="own-editor">
      <label>
        <span className="label">Dockerfile</span>
        <textarea className="mono dockerfile" aria-label={`Dockerfile for ${name}`} value={text} onChange={(e) => setText(e.target.value)} rows={Math.max(6, Math.min(24, text.split('\n').length + 1))} spellCheck={false} />
      </label>
      <div className="actions">
        <button className="secondary small" type="button" disabled={!dirty} onClick={() => void save()}>
          Save Dockerfile
        </button>
        <span className="spacer" />
        {!confirm ? (
          <button className="quiet" type="button" onClick={() => (im.used_by.length ? setConfirm(true) : void remove())}>
            Delete
          </button>
        ) : (
          <>
            <span className="status-text error">Still used by {users}. Delete anyway?</span>
            <button className="quiet" type="button" onClick={() => void remove()}>
              Delete {name}
            </button>
            <button className="quiet" type="button" onClick={() => setConfirm(false)}>
              Keep
            </button>
          </>
        )}
        {status && <span className={status.error ? 'status-text error' : 'status-text'}>{status.text}</span>}
      </div>
    </div>
  )
}

function AddImage({ onAdded }: { onAdded: () => Promise<void> }) {
  const [name, setName] = useState('')
  const [text, setText] = useState(starterDockerfile)
  const [status, setStatus] = useState('')
  async function add(e: FormEvent) {
    e.preventDefault()
    setStatus('')
    try {
      await api.putOwnImage(name.trim(), text)
      setName('')
      setText(starterDockerfile)
      await onAdded()
    } catch (err) {
      setStatus(errorText(err))
    }
  }
  return (
    <form className="add-profile add-image" aria-label="Add an image" onSubmit={(e) => void add(e)}>
      <h3>Add an image</h3>
      <p className="hint">Write its Dockerfile here; the server builds it and your pipelines name it by this name. The Dockerfile is the whole build context: no files beside it yet.</p>
      <div className="field">
        <label htmlFor="new-image-name">Name</label>
        <input id="new-image-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="lint-tools" pattern="[a-z0-9][a-z0-9._-]{0,62}" />
      </div>
      <label>
        <span className="label">Dockerfile</span>
        <textarea className="mono dockerfile" aria-label="New image Dockerfile" value={text} onChange={(e) => setText(e.target.value)} rows={8} spellCheck={false} />
      </label>
      <div className="form-row">
        <button className="secondary small" type="submit" disabled={!name.trim() || !text.trim()}>
          Add image
        </button>
        {status && <span className="status-text error">{status}</span>}
      </div>
    </form>
  )
}

function ImageCard({ im, serverVersion, onBuilt }: { im: ImageInfo; serverVersion: string; onBuilt: () => Promise<void> }) {
  const now = useNow()
  const [build, setBuild] = useState<ImageBuild | null>(null)
  const [error, setError] = useState('')
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  const stopPolling = () => {
    if (timer.current) clearInterval(timer.current)
    timer.current = null
  }
  useEffect(() => stopPolling, [])

  async function start() {
    setError('')
    try {
      const b = await api.buildImage(im.ref.replace(/:(dev|latest)$/, ''))
      setBuild(b)
      stopPolling()
      timer.current = setInterval(() => {
        api
          .imageBuild(b.id)
          .then(async (cur) => {
            setBuild(cur)
            if (cur.status !== 'running') {
              stopPolling()
              await onBuilt()
            }
          })
          .catch((e: unknown) => {
            setError(errorText(e))
            stopPolling()
          })
      }, buildPoll.every)
    } catch (e) {
      setError(errorText(e))
    }
  }

  const st = statusText(im, serverVersion, now)
  return (
    <div className="image-card" aria-label={`Image ${im.ref}`}>
      <div className="head">
        <span className="name mono">{im.ref}</span>
        {im.shipped && <span className="tag">shipped by Plumbix</span>}
        {im.own && <span className="tag">own image</span>}
        {im.used_by.some((u) => u.validator) && <span className="tag">validator</span>}
      </div>
      {im.purpose && <p className="purpose">{im.purpose}</p>}
      <p className={`status ${st.tone}`}>{st.text}</p>
      {im.digest && <p className="digest mono">{im.digest.slice(0, 19)}</p>}
      <p className="usage">{im.used_by.length ? `Used by ${im.used_by.map((u) => `${u.repo}: ${u.step}`).join(' · ')}` : 'No registered pipeline uses it.'}</p>
      {im.own && <OwnEditor im={im} onChanged={onBuilt} />}
      {(im.shipped || im.own) && (
        <div className="actions">
          <button className="secondary small" type="button" disabled={build?.status === 'running'} onClick={() => void start()}>
            {build?.status === 'running' ? 'Building…' : im.present ? 'Rebuild' : 'Build'}
          </button>
          {build && <span className={`status-text${build.status === 'failed' ? ' error' : ''}`}>{build.status === 'running' ? 'running' : build.status === 'succeeded' ? 'Built' : `Failed: ${build.error ?? ''}`}</span>}
          {error && <span className="status-text error">{error}</span>}
        </div>
      )}
      {build && build.log && (
        <pre className="build-log" aria-label={`Build log for ${im.ref}`}>
          {build.log}
        </pre>
      )}
    </div>
  )
}

export function ImagesPage() {
  const [view, setView] = useState<ImagesView | null>(null)
  const [error, setError] = useState('')
  const reload = useCallback(async () => {
    try {
      setView(await api.images())
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [])
  useEffect(() => {
    void reload()
  }, [reload])

  const list = view?.images ?? []
  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Images</h1>
          <span className="count mono">{view ? `${list.length} images · ${list.filter((i) => !i.present).length} missing` : ''}</span>
        </div>
      </div>
      <div className="settings images">
        <p className="lead">
          Every container image the registered pipelines run, as this machine's Docker knows it. A validator step's image is the validator. Plumbix ships two and can rebuild them here from the sources inside the server: a build runs on the server's Docker daemon, outside the step sandbox, so it is as trusted as editing a pipeline and only a signed-in person can start one. Other images are pulled by Docker when a step first needs them.
        </p>
        {error && <p className="status-text error">{error}</p>}
        {view && list.length === 0 && <p className="hint">No registered pipeline names an image yet.</p>}
        <div className="image-list">
          {list.map((im) => (
            <ImageCard key={im.ref} im={im} serverVersion={view?.version ?? ''} onBuilt={reload} />
          ))}
        </div>
        {view && <AddImage onAdded={reload} />}
      </div>
    </>
  )
}
