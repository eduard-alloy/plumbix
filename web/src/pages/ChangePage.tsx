// The core screen: intent and verdict first, evidence linked everywhere,
// the diff on demand. Layout and copy follow web/design/README.md.
import { useEffect, useRef, useState } from 'react'
import { api, ApiError } from '../api'
import type { Diff } from '../model'
import { useRoute } from '../router'
import { useNow, useStore } from '../store'
import { StatePill, type PillStatus } from '../ui/StatePill'
import { LogPanel, type LogTarget } from '../ui/LogPanel'
import { DiffView, type Highlight } from '../ui/DiffView'
import { changeView, refusalNotice, type NoticeVM, type StepVM } from '../vm/change'

type Busy = 'merge' | 'close' | 'validate' | null

export function ChangePage({ id }: { id: string }) {
  const { state, actions } = useStore()
  const [, go] = useRoute()
  const now = useNow()
  const c = state.changes[id]
  const runs = state.runs[id] ?? []
  const repo = c ? state.repos.find((r) => r.id === c.repo_id) : undefined
  const [selected, setSelected] = useState<number | undefined>(undefined)
  const [busy, setBusy] = useState<Busy>(null)
  const [confirmClose, setConfirmClose] = useState(false)
  const [notice, setNotice] = useState<NoticeVM | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [flash, setFlash] = useState<string | null>(null)
  const [diffStat, setDiffStat] = useState<Diff | null>(null)
  const [diff, setDiff] = useState<{ state: 'closed' | 'loading' | 'open'; data?: Diff }>({ state: 'closed' })
  const [highlight, setHighlight] = useState<Highlight | null>(null)
  const [log, setLog] = useState<LogTarget | null>(null)
  const iterationsRef = useRef<HTMLElement>(null)
  const diffRef = useRef<HTMLElement>(null)

  // A change the list did not have yet (deep link): fetch it once.
  useEffect(() => {
    if (!c && state.loaded) void actions.refreshChange(id)
  }, [c, state.loaded, id, actions])

  // The diff summary for the header of the Diff section.
  const latestHead = [...runs].reverse().find((r) => r.head)?.head
  useEffect(() => {
    setDiffStat(null)
    setDiff({ state: 'closed' })
    if (!latestHead) return
    let live = true
    api
      .diff(id, true)
      .then((d) => live && setDiffStat(d))
      .catch(() => live && setDiffStat(null))
    return () => {
      live = false
    }
  }, [id, latestHead])

  if (!c) {
    return (
      <>
        <div className="change-header">
          <button className="back" onClick={() => go('/')}>
            Changes
          </button>
          <span className="dim">/</span>
          <span className="mono dim2">{id}</span>
        </div>
        <div className="note">{state.loaded ? `No change ${id}` : ''}</div>
      </>
    )
  }

  const v = changeView(c, runs, repo, selected, now)
  const ctx = { src: c.source_branch, tgt: c.target_branch, head: v.header.head, base: v.header.base }

  async function merge() {
    setBusy('merge')
    setNotice(null)
    try {
      const merged = await api.merge(c!.id)
      actions.setChange(merged)
      void actions.refreshChange(c!.id)
    } catch (e) {
      if (e instanceof ApiError) {
        setNotice(refusalNotice(e.code, ctx))
        void actions.refreshChange(c!.id)
      }
    } finally {
      setBusy(null)
    }
  }

  async function close() {
    setBusy('close')
    setConfirmClose(false)
    try {
      actions.setChange(await api.close(c!.id))
    } catch (e) {
      if (e instanceof ApiError) void actions.refreshChange(c!.id)
    } finally {
      setBusy(null)
    }
  }

  async function validate() {
    setBusy('validate')
    try {
      await api.validate(c!.id)
      setNotice(refusalNotice('validating', ctx))
      await actions.refreshChange(c!.id)
    } catch (e) {
      if (e instanceof ApiError) setNotice({ title: 'Not started', body: e.message, cmd: '', action: null, actionLabel: '' })
    } finally {
      setBusy(null)
    }
  }

  function noticeAction(n: NoticeVM) {
    switch (n.action) {
      case 'validate':
        void validate()
        break
      case 'merge':
        void merge()
        break
      case 'copy':
        void navigator.clipboard?.writeText(n.cmd)
        break
      case 'iterations':
        setNotice(null)
        iterationsRef.current?.scrollIntoView?.({ block: 'start' })
        break
    }
  }

  function goCriterion(acid: string) {
    setFlash(acid)
    document.getElementById(`crit-${acid}`)?.scrollIntoView?.({ block: 'center' })
    setTimeout(() => setFlash(null), 1600)
  }

  async function openDiff(): Promise<boolean> {
    if (diff.state === 'open') return true
    if (diff.state === 'loading') return false
    setDiff({ state: 'loading' })
    try {
      setDiff({ state: 'open', data: await api.diff(c!.id, false) })
      return true
    } catch {
      setDiff({ state: 'closed' })
      return false
    }
  }

  async function toggleDiff() {
    if (diff.state === 'open') {
      setDiff({ state: 'closed' })
      setHighlight(null)
      return
    }
    await openDiff()
  }

  async function goLocation(file: string, line: number) {
    setHighlight({ file, line })
    if (await openDiff()) {
      setTimeout(() => {
        const el = document.querySelector('[data-diff-hl="1"]') ?? document.querySelector('[data-diff-file-hl="1"]') ?? diffRef.current
        el?.scrollIntoView?.({ block: 'center' })
      }, 0)
    }
  }

  // Logs open for the run the evidence section shows; evidence paths name
  // a step of the latest run.
  const latestRun = runs[runs.length - 1]
  const evidenceRun = runs.find((r) => r.id === v.evidence.runId)
  function openLog(run: typeof latestRun | undefined, s: StepVM) {
    if (run) setLog({ changeId: c!.id, run, step: s })
  }
  function openPathLog(path: string) {
    const stepName = path.split('/')[1] ?? ''
    const run = latestRun
    const step = run?.steps.find((s) => s.name === stepName)
    if (run && step) openLog(run, { name: step.name, validator: step.validator, status: step.status, exit: `exit ${step.exit_code}`, dur: '', hasLog: true })
  }

  const stepRow = (s: StepVM, key: string, className: string) => {
    const inner = (
      <>
        <StatePill status={s.status as PillStatus} mode="icon" />
        <span className="name">{s.name}</span>
        <span>{s.validator && <span className="tag">validator</span>}</span>
        <span className="status-cell">
          <StatePill status={s.status as PillStatus} mode="text" />
          <Decision step={s} />
        </span>
        <span className="mono dim">{s.exit}</span>
        <span className="mono dim2 right">{s.dur}</span>
        <span className="log">{s.hasLog ? 'Log' : ''}</span>
      </>
    )
    return (
      <button key={key} className={className} disabled={!s.hasLog} onClick={() => openLog(evidenceRun, s)}>
        {inner}
      </button>
    )
  }
  const diffSummary = diffStat ? `${diffStat.files.length} files · +${diffStat.additions} −${diffStat.deletions} · ${v.diffRange}` : v.diffRange

  return (
    <>
      <div className="change-header">
        <button className="back" onClick={() => go('/')}>
          Changes
        </button>
        <span className="dim">/</span>
        <span className="mono dim2">{c.id}</span>
        <span className="spacer" />
        <span className="meta">{v.header.updated}</span>
        <span className="meta" style={{ marginLeft: 12 }}>
          <span className="mono">esc</span> back
        </span>
      </div>
      <div className="change-body">
        <div className="title-block">
          <div className="title-main">
            <div className="title-line">
              <StatePill status={c.state} />
              <h1>{c.title}</h1>
            </div>
            <div className="meta-line">
              <span>
                {v.header.repo} <span className="k">{v.header.host}</span>
              </span>
              <span>
                {v.header.src} <span className="dim">→</span> {v.header.tgt}
              </span>
              <span>
                <span className="k">head</span> {v.header.head}
              </span>
              <span>
                <span className="k">base</span> {v.header.base}
              </span>
              <span>
                <span className="k">iteration</span> {v.header.iter}
              </span>
              <span>
                <span className="k">by</span> {v.header.by}
              </span>
              <span>
                <span className="k">submitted</span> {v.header.submitted}
              </span>
            </div>
          </div>
          <div className="actions">
            {v.actions.close &&
              (confirmClose ? (
                <span className="confirm">
                  Close this change?
                  <button className="secondary" onClick={() => void close()} disabled={busy !== null}>
                    Confirm
                  </button>
                  <button className="quiet" onClick={() => setConfirmClose(false)}>
                    Keep
                  </button>
                </span>
              ) : (
                <button className="secondary" onClick={() => setConfirmClose(true)} disabled={busy !== null}>
                  Close
                </button>
              ))}
            {v.actions.primary === 'merge' && (
              <button className="primary" onClick={() => void merge()} disabled={busy !== null}>
                {busy === 'merge' ? 'Merging…' : 'Merge'}
              </button>
            )}
            {v.actions.primary === 'validate' && (
              <button className="primary" onClick={() => void validate()} disabled={busy !== null}>
                Validate again
              </button>
            )}
          </div>
        </div>

        {v.newerCommits && (
          <div role="note" className="notice">
            <span className="icon">
              <StatePill status="info" mode="icon" />
            </span>
            <div className="text">
              <span className="title">Newer commits on {c.source_branch}</span>
              <span className="body">
                The branch now points at {v.newerCommits}; the last verdict was for {v.header.head}.{' '}
                {v.needsPerson ? 'A push does not restart validation for a change that needs a person. Validate again when you want a verdict on the new head.' : 'Validation starts on the next poll.'}
              </span>
            </div>
            <div className="buttons">
              {v.needsPerson && (
                <button className="secondary small" onClick={() => void validate()} disabled={busy !== null}>
                  Validate again
                </button>
              )}
            </div>
          </div>
        )}

        {notice && (
          <div role="status" className="notice">
            <span className="icon">
              <StatePill status="info" mode="icon" />
            </span>
            <div className="text">
              <span className="title">{notice.title}</span>
              <span className="body">{notice.body}</span>
              {notice.cmd && <code className="cmd">{notice.cmd}</code>}
            </div>
            <div className="buttons">
              {notice.action && (
                <button className="secondary small" onClick={() => noticeAction(notice)}>
                  {notice.actionLabel}
                </button>
              )}
              <button className="dismiss" aria-label="Dismiss" onClick={() => setNotice(null)}>
                ×
              </button>
            </div>
          </div>
        )}

        <div className="two-col">
          <div className="left">
            <section>
              <h2 className="label">Intent</h2>
              <div className="intent">
                {v.intent.map((p, i) => (
                  <p key={i}>{p}</p>
                ))}
              </div>
              {v.constraints.length > 0 && (
                <div className="constraints">
                  <span className="dim" style={{ fontSize: 12 }}>
                    Constraints
                  </span>
                  {v.constraints.map((k) => (
                    <span key={k} className="chip">
                      {k}
                    </span>
                  ))}
                </div>
              )}
            </section>
            <section>
              <div className="label-row">
                <h2 className="label">Acceptance criteria</h2>
                <span className="mono dim" style={{ fontSize: 12 }}>
                  {v.critCount}
                </span>
              </div>
              <div className="criteria">
                {v.criteria.map((cr) => {
                  const key = `${c.id}:${cr.id}`
                  const open = !!expanded[key]
                  return (
                    <div key={cr.id} id={`crit-${cr.id}`} className={flash === cr.id ? 'criterion flash' : 'criterion'}>
                      <span className="icon">
                        <StatePill status={cr.status} mode="icon" />
                      </span>
                      <span className="acid">{cr.id}</span>
                      <div className="body">
                        <div className="head">
                          <span className="text">{cr.text}</span>
                          <span className="check">{cr.check}</span>
                        </div>
                        <div className="assessment">
                          <StatePill status={cr.status} mode="text" />
                          <span>{cr.reason}</span>
                          {cr.by && <span className="by">{cr.by}</span>}
                          {cr.evidence.map((p) => (
                            <button key={p} className="link mono" onClick={() => openPathLog(p)}>
                              {p}
                            </button>
                          ))}
                          {cr.more.length > 0 && (
                            <button className="toggle" aria-expanded={open} onClick={() => setExpanded({ ...expanded, [key]: !open })}>
                              {open ? 'hide' : `+${cr.more.length} validator${cr.more.length === 1 ? '' : 's'}`}
                            </button>
                          )}
                        </div>
                        {open &&
                          cr.more.map((m) => (
                            <div key={m.validator} className="assessment">
                              <StatePill status={m.status} mode="plain" />
                              <span>{m.reason}</span>
                              <span className="by">{m.validator}</span>
                              {m.evidence.map((p) => (
                                <button key={p} className="link mono" onClick={() => openPathLog(p)}>
                                  {p}
                                </button>
                              ))}
                            </div>
                          ))}
                      </div>
                    </div>
                  )
                })}
              </div>
            </section>
          </div>

          <aside aria-label="Verdict" className="verdict">
            <div className="block">
              <div className="top">
                <h2 className="label">Verdict</h2>
                <span className="runid">{v.verdict.runId}</span>
              </div>
              <div className="state">
                <StatePill status={v.verdict.state} />
                <span className="iter">{v.verdict.iterLabel}</span>
              </div>
              <p className="sentence">{v.verdict.sentence}</p>
              {v.verdict.counts && <div className="counts">{v.verdict.counts}</div>}
            </div>
            {v.verdict.reasons.length > 0 && (
              <div className="block">
                <div className="label">Reasons</div>
                <ul>
                  {v.verdict.reasons.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </ul>
              </div>
            )}
            {v.verdict.steps.length > 0 && (
              <div className="block">
                <div className="label">{v.verdict.stepsLabel}</div>
                <div className="steps">
                  {v.verdict.steps.map((s) => (
                    <button key={s.name} className="vstep" disabled={!s.hasLog} onClick={() => openLog(latestRun, s)} title={s.summary}>
                      <StatePill status={s.status as PillStatus} mode="icon" />
                      <span className="name">{s.name}</span>
                      <Decision step={s} />
                      <span className="dur">{s.dur}</span>
                    </button>
                  ))}
                </div>
              </div>
            )}
            {v.verdict.mergeNote && <div className="block mergenote">{v.verdict.mergeNote}</div>}
            {v.verdict.record && (
              <div className="block">
                <div className="label">Merge</div>
                <div className="record">
                  <span className="k">commit</span>
                  <span className="v">{v.verdict.record.commit}</span>
                  <span className="k">previous</span>
                  <span className="v">{v.verdict.record.previous}</span>
                  <span className="k">strategy</span>
                  <span className="v">{v.verdict.record.strategy}</span>
                  <span className="k">merged at</span>
                  <span className="v">{v.verdict.record.mergedAt}</span>
                </div>
              </div>
            )}
          </aside>
        </div>

        <section className="section first">
          <div className="section-header">
            <h2>Findings</h2>
            <span className="count">{v.findings.reduce((a, g) => a + g.items.length, 0)}</span>
          </div>
          {v.findings.length === 0 && <div className="none">{v.judged ? 'No findings.' : 'Findings appear with the verdict.'}</div>}
          {v.findings.map((g) => (
            <div key={g.severity}>
              <div className="sublabel">
                {g.label} <span className="mono">{g.items.length}</span>
              </div>
              {g.items.map((f, i) => (
                <div key={i} className="finding">
                  <span className="icon">
                    <StatePill status={f.severity} mode="icon" />
                  </span>
                  <div className="body">
                    <span>{f.message}</span>
                    <span className="meta">
                      <StatePill status={f.severity} mode="text" />
                      <span className="mono">{f.validator}</span>
                      {f.criterion && (
                        <button className="link mono" onClick={() => goCriterion(f.criterion)}>
                          {f.criterion}
                        </button>
                      )}
                    </span>
                  </div>
                  {f.loc && (
                    <button className="link mono loc" onClick={() => void goLocation(f.file, f.line)}>
                      {f.loc}
                    </button>
                  )}
                </div>
              ))}
            </div>
          ))}
        </section>

        <section className="section">
          <div className="section-header">
            <h2>Risks</h2>
            <span className="count">{v.risks.length}</span>
          </div>
          {v.risks.length === 0 && <div className="none">{v.judged ? 'No risks flagged.' : 'Risks appear with the verdict.'}</div>}
          {v.risks.map((r, i) => (
            <div key={i} className="risk">
              <span className="icon">
                <StatePill status={r.humanRequired ? 'human_required' : 'info'} mode="icon" />
              </span>
              <span className="area">{r.area}</span>
              <span className="body">
                <span>{r.message}</span>
                <span className="by">{r.validator}</span>
              </span>
              {r.humanRequired ? <StatePill status="human_required" /> : <span className="nohr">No human needed</span>}
            </div>
          ))}
        </section>

        <section className="section" ref={iterationsRef}>
          <div className="section-header">
            <h2>Iterations</h2>
            <span className="count">{v.iterations.length}</span>
          </div>
          {v.iterations.length === 0 && <div className="none">Not validated yet. Validation starts when a runner is free.</div>}
          {v.iterations.map((it, j, arr) => (
            <button key={it.n} className={`iteration${j === 0 ? ' first' : ''}${j === arr.length - 1 ? ' last' : ''}`} aria-pressed={it.selected} onClick={() => setSelected(it.index)}>
              <span className="line" />
              <span className="dot">
                <StatePill status={it.status as PillStatus} mode="icon" />
              </span>
              <span>Iteration {it.n}</span>
              <StatePill status={it.status as PillStatus} mode="text" />
              <span className="headline">{it.headline}</span>
              <span className="mono dim">{it.head}</span>
              <span className="mono dim2 right">{it.dur}</span>
              <span className="mono dim right">{it.ago}</span>
            </button>
          ))}
        </section>

        <section className="section">
          <div className="section-header">
            <h2>Evidence</h2>
            <span className="count">{v.evidence.label}</span>
          </div>
          {v.evidence.error && <div className="run-error">error: {v.evidence.error}</div>}
          {v.evidence.steps.map((s) => stepRow(s, s.name, 'step'))}
        </section>

        <section className="section" ref={diffRef}>
          <div className="section-header">
            <h2>Diff</h2>
            <span className="count">{diffSummary}</span>
            {v.diffRange && (
              <button className="secondary small btn" aria-expanded={diff.state === 'open'} onClick={() => void toggleDiff()}>
                {diff.state === 'open' ? 'Hide diff' : diff.state === 'loading' ? 'Loading…' : 'Load diff'}
              </button>
            )}
          </div>
          {diff.state === 'loading' && (
            <div className="diff-loading" aria-busy="true">
              <span className="bar thin" style={{ width: '40%' }} />
              <span className="bar thin" style={{ width: '70%' }} />
              <span className="bar thin" style={{ width: '55%' }} />
            </div>
          )}
          {diff.state === 'open' && diff.data && <DiffView diff={diff.data} findings={v.findings.flatMap((g) => g.items.filter((f) => f.file).map((f) => ({ validator: f.validator, severity: f.severity, message: f.message, criterion: f.criterion || undefined, file: f.file, line: f.line })))} highlight={highlight} />}
        </section>
      </div>
      {log && <LogPanel target={log} onClose={() => setLog(null)} />}
    </>
  )
}

/** Decision shows what a validator decided, apart from how its step ran. */
function Decision({ step }: { step: StepVM }) {
  if (!step.outcome) return null
  const status: PillStatus = step.outcome === 'approve' ? 'approved' : step.outcome === 'fix' ? 'fix_requested' : step.outcome === 'reject' ? 'rejected' : 'errored'
  return <StatePill status={status} label={step.outcome === 'none' ? 'No verdict' : undefined} />
}
