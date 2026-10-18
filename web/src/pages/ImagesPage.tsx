// Images: the container images the registered pipelines run, what the
// server knows about each, and builds of the two Plumbix ships (WU-032).
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError } from '../api'
import { agoLong, minutesBetween } from '../format'
import type { ImageBuild, ImageInfo, ImagesView } from '../model'
import { useNow } from '../store'

/** buildPoll is how often a running build's log is refetched (ms); tests shorten it. */
export const buildPoll = { every: 1500 }

function errorText(e: unknown): string {
  return e instanceof ApiError || e instanceof Error ? e.message : String(e)
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
      return { text: im.shipped ? 'Not built on this machine. A step naming it fails until it is.' : 'Not present locally; Docker pulls it on first use.', tone: im.shipped ? 'bad' : 'warn' }
    case 'current':
      return { text: `Built by this server (${im.version}) · ${tail}`, tone: 'ok' }
    case 'stale':
      return { text: `Built by server ${im.version}, this one is ${serverVersion}. Rebuild to match. · ${tail}`, tone: 'warn' }
    case 'unlabelled':
      return { text: `Built outside Plumbix (docker build by hand); its version is unknown. Rebuild to label it. · ${tail}`, tone: 'warn' }
    default:
      return { text: `Present · ${tail}`, tone: 'ok' }
  }
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
      const b = await api.buildImage(im.ref.replace(/:dev$/, ''))
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
        {im.used_by.some((u) => u.validator) && <span className="tag">validator</span>}
      </div>
      {im.purpose && <p className="purpose">{im.purpose}</p>}
      <p className={`status ${st.tone}`}>{st.text}</p>
      {im.digest && <p className="digest mono">{im.digest.slice(0, 19)}</p>}
      <p className="usage">{im.used_by.length ? `Used by ${im.used_by.map((u) => `${u.repo}: ${u.step}`).join(' · ')}` : 'No registered pipeline uses it.'}</p>
      {im.shipped && (
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
      </div>
    </>
  )
}
