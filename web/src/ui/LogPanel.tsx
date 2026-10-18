// The step log: a fixed right panel with numbered lines, search and wrap.
// A running step's log follows (WU-027): refetched on a timer, view kept at
// the bottom, until the step ends.
import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import type { Run } from '../model'
import { StatePill, type PillStatus } from './StatePill'
import { highlight, lineTone, splitLines } from '../vm/log'
import type { StepVM } from '../vm/change'

export const CLOSE_LOG = 'plumbix:close-log'

/** logFollow.every is how often a running step's log is refetched (ms); tests shorten it. */
export const logFollow = { every: 2000 }

export interface LogTarget {
  changeId: string
  run: Run
  step: StepVM
}

export function LogPanel({ target, onClose }: { target: LogTarget; onClose: () => void }) {
  const { changeId, run, step } = target
  const [text, setText] = useState<string | null>(null)
  const [q, setQ] = useState('')
  const [current, setCurrent] = useState(0)
  const [wrap, setWrap] = useState(true) // long lines wrap unless the reader turns it off
  const bodyRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const following = step.status === 'running'

  // Load on open; while the step runs, keep loading. When it ends the
  // effect runs once more and fetches the final log.
  useEffect(() => {
    let live = true
    const load = () =>
      api
        .stepLog(run.id, step.name)
        .then((t) => live && setText((old) => (old === t ? old : t)))
        .catch((e: unknown) => live && setText(`could not load the log: ${e instanceof Error ? e.message : String(e)}`))
    void load()
    const timer = following ? setInterval(() => void load(), logFollow.every) : undefined
    return () => {
      live = false
      if (timer) clearInterval(timer)
    }
  }, [run.id, step.name, following])

  useEffect(() => {
    setText(null)
    setQ('')
    setCurrent(0)
  }, [run.id, step.name])

  // A followed log stays at its end as lines arrive.
  useEffect(() => {
    if (!following || !bodyRef.current) return
    bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [text, following])

  useEffect(() => {
    const close = () => onClose()
    window.addEventListener(CLOSE_LOG, close)
    return () => window.removeEventListener(CLOSE_LOG, close)
  }, [onClose])

  const lines = useMemo(() => {
    const raw = splitLines(text ?? '')
    let n = 0
    return raw.map((t, i) => {
      const h = highlight(t, q, n)
      n = h.next
      return { n: i + 1, parts: h.parts, tone: lineTone(t) }
    })
  }, [text, q])
  const total = lines.reduce((a, l) => a + l.parts.filter((p) => p.match !== null).length, 0)
  const cur = total ? Math.min(current, total - 1) : 0

  useEffect(() => {
    if (!total) return
    bodyRef.current?.querySelector(`[data-m="${cur}"]`)?.scrollIntoView?.({ block: 'center' })
  }, [cur, total])

  const next = () => total && setCurrent((cur + 1) % total)
  const matchLabel = q.trim() ? (total ? `${cur + 1}/${total}` : '0/0') : ''

  return (
    <div role="region" aria-label="Step log" className="log-panel">
      <div className="log-head">
        <div className="log-title">
          <span className="log-step">{step.name}</span>
          <StatePill status={step.status as PillStatus} />
          {step.validator && <span className="tag">validator</span>}
          <span className="spacer" />
          <span className="meta">
            <span className="mono">esc</span> close
          </span>
          <button className="dismiss" aria-label="Close log" onClick={onClose}>
            ×
          </button>
        </div>
        <div className="meta-line">
          <span>{changeId}</span>
          <span>{run.id}</span>
          <span>
            <span className="k">iteration</span> {run.iteration ?? ''}
          </span>
          <span>
            <span className="k">duration</span> {step.dur}
          </span>
          <span>
            <span className="k">exit code</span> {step.exit.replace('exit ', '')}
          </span>
        </div>
      </div>
      <div className="log-tools">
        <div className="search log-search">
          <input
            ref={searchRef}
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setCurrent(0)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                next()
              }
            }}
            placeholder="Search log"
            aria-label="Search log"
          />
          <span className="match mono">{matchLabel}</span>
        </div>
        <button className="secondary small" aria-label="Next match" onClick={next}>
          Next ↓
        </button>
        <span className="spacer" />
        <button className="secondary small" aria-pressed={wrap} onClick={() => setWrap(!wrap)}>
          Wrap
        </button>
      </div>
      <div ref={bodyRef} className={wrap ? 'log-body wrap' : 'log-body'}>
        {text === null && <div className="dim log-loading">Loading…</div>}
        {lines.map((l) => (
          <div key={l.n} data-line={l.n} className={`log-line ${l.tone}`}>
            <span className="ln">{l.n}</span>
            <span className="lt">
              {l.parts.map((p, i) =>
                p.match === null ? (
                  <span key={i}>{p.text}</span>
                ) : (
                  <span key={i} data-m={p.match} className={p.match === cur ? 'hit hit-strong' : 'hit'}>
                    {p.text}
                  </span>
                ),
              )}
            </span>
          </div>
        ))}
      </div>
      <div className="log-foot">
        <span>{following ? 'Step running. The log follows until the step ends.' : `Step ${step.status.replace('_', ' ')}. Full log, fetched on open.`}</span>
        <span className="spacer" />
        <span className="mono">{lines.length} lines</span>
      </div>
    </div>
  )
}
