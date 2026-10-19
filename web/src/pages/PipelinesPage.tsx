// The pipeline editor (design: web/design/PIPELINE.md, layout 2a). Edits
// live in memory per repository; Save pushes a branch and opens a change,
// never touching the protected branch.
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, ApiError } from '../api'
import type { Change, PipelineFile, Repo, ReviewerProfile } from '../model'
import { useRoute } from '../router'
import { useStore } from '../store'
import {
  addStep, bands, CH, CW, toggleExternal, dependents, diffSummary, find, fromApi, groupSteps, mergeSteps, needOptions, plain, removeSteps, renameStep,
  renderYaml, saveDefaults, setField, toggleValidator, yamlDiff, fmtSeconds, type Band, type PModel, type PStep,
} from '../vm/pipeline'
import './pipeline.css'

export function repoName(r: Repo): string {
  return r.url.replace(/\/+$/, '').replace(/\.git$/, '').split('/').pop() || r.id
}

interface Loaded {
  file: PipelineFile
  base: PModel | null
}

export function PipelinesPage({ repoId }: { repoId?: string }) {
  const { state } = useStore()
  const [, go] = useRoute()
  const repos = state.repos
  const repo = repos.find((r) => r.id === repoId) ?? repos[0]
  const [loaded, setLoaded] = useState<Record<string, Loaded | { error: string }>>({})
  const [edits, setEdits] = useState<Record<string, PModel>>({})
  const [sel, setSel] = useState<string[]>([])
  // The reviewer profiles from Settings, for the validator panel's selects (WU-034).
  const [profiles, setProfiles] = useState<ReviewerProfile[]>([])
  useEffect(() => {
    api
      .settings()
      .then((v) => setProfiles(v.reviewers))
      .catch(() => setProfiles([]))
  }, [])
  const [view, setView] = useState<'visual' | 'yaml'>('visual')
  const [saveOpen, setSaveOpen] = useState(false)
  const [form, setForm] = useState<{ branch?: string; title?: string; intent?: string }>({})
  const [pushing, setPushing] = useState(false)
  const [saved, setSaved] = useState<{ change: Change } | null>(null)
  const [err, setErr] = useState('')
  const [groupName, setGroupName] = useState('checks')

  // Load each repository's file once.
  useEffect(() => {
    if (!repo || loaded[repo.id]) return
    let live = true
    api
      .pipelineFile(repo.id)
      .then((file) => live && setLoaded((m) => ({ ...m, [repo.id]: { file, base: fromApi(file) } })))
      .catch((e: unknown) => live && setLoaded((m) => ({ ...m, [repo.id]: { error: e instanceof ApiError ? e.message : 'could not load the pipeline' } })))
    return () => {
      live = false
    }
  }, [repo, loaded])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = (e.target as HTMLElement | null)?.tagName?.toLowerCase() ?? ''
      if (e.key === 'Escape' && !['input', 'textarea', 'select'].includes(t)) setSel([])
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const entry = repo ? loaded[repo.id] : undefined
  const base = entry && 'base' in entry ? entry.base : null
  const file = entry && 'file' in entry ? entry.file : null
  const cur: PModel | null = repo ? (edits[repo.id] ?? base) : null

  const mutate = useCallback(
    (fn: (p: PModel) => PModel | { model: PModel; id?: string }) => {
      if (!repo || !cur) return
      const r = fn(cur)
      const next = 'model' in r ? r.model : r
      setEdits((e) => ({ ...e, [repo.id]: next }))
      setSaved(null)
      setErr('')
      if ('model' in r && r.id) setSel([r.id])
    },
    [repo, cur],
  )

  const pick = (id: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
    const multi = !!e && (e.shiftKey || e.metaKey || e.ctrlKey)
    setSel((s) => (multi ? (s.includes(id) ? s.filter((x) => x !== id) : s.concat(id)) : s.length === 1 && s[0] === id ? [] : [id]))
  }

  const selSet = useMemo(() => new Set(sel), [sel])
  const bandList: Band[] = useMemo(() => (cur ? bands(cur, selSet) : []), [cur, selSet])
  const summary = useMemo(() => (cur && base ? diffSummary(base, cur) : []), [cur, base])
  const dirty = summary.length > 0
  const one = sel.length === 1 && cur ? find(cur, sel[0]) : undefined
  const oneStep = !!one && !one.validator
  const multiSteps = !!cur && sel.length > 1 && sel.every((id) => find(cur, id) && !find(cur, id)!.validator)

  const discard = () => {
    if (!repo) return
    setEdits((e) => {
      const n = { ...e }
      delete n[repo.id]
      return n
    })
    setSel([])
    setSaveOpen(false)
    setForm({})
    setErr('')
  }

  const defaults = repo ? saveDefaults(repoName(repo), summary) : { branch: '', title: '', intent: '' }

  async function push(e: FormEvent) {
    e.preventDefault()
    if (!repo || !cur) return
    if (!plain(cur).length) {
      setErr('A pipeline needs at least one step.')
      return
    }
    setPushing(true)
    setErr('')
    try {
      const change = await api.proposePipeline(repo.id, {
        branch: form.branch ?? defaults.branch,
        title: form.title ?? defaults.title,
        intent: form.intent ?? defaults.intent,
        content: renderYaml(cur, file?.content ?? null),
        target_branch: file?.branch,
      })
      setSaved({ change })
      setEdits((ed) => {
        const n = { ...ed }
        delete n[repo.id]
        return n
      })
      setSel([])
      setSaveOpen(false)
      setForm({})
    } catch (ex) {
      if (ex instanceof ApiError) {
        const problems = (ex.details?.problems as { field: string; message: string }[] | undefined) ?? []
        setErr(problems.length ? problems.map((p) => `${p.field} ${p.message}`).join('; ') : ex.code === 'branch_exists' ? 'That branch already exists on the host. Pick another name.' : ex.code === 'push_denied' ? 'The host refused the push. Set a token for this repository in Settings and try again.' : ex.message)
      } else setErr('The push failed.')
    } finally {
      setPushing(false)
    }
  }

  if (!state.loaded) return <div className="pipe-empty">Loading…</div>
  if (!repo) {
    return (
      <>
        <Header repos={repos} repo={undefined} edits={edits} file={null} view={view} setView={setView} go={go} />
        <div className="empty">
          <h2>No repositories yet</h2>
          <p>Register a repository from the terminal; its pipeline appears here.</p>
        </div>
      </>
    )
  }

  const hint = sel.length === 0 ? 'Click a step to edit. Shift-click to select several.' : sel.length === 1 ? `Add after: a new step that needs ${sel[0]}. In parallel: the same needs.` : 'Merge runs them in one container. Group keeps them separate.'

  return (
    <div className="pipe-page">
      <Header repos={repos} repo={repo} edits={edits} file={file} view={view} setView={setView} go={go} />
      {entry && 'error' in entry && <div className="pipe-empty">{entry.error}</div>}
      {!entry && <div className="pipe-empty">Loading the pipeline…</div>}
      {file && !base && (
        <div className="pipe-problems">
          <h2>{file.content ? 'This pipeline file does not parse' : 'No pipeline file on this branch'}</h2>
          <p>{file.content ? 'Fix it in the repository and push; the editor opens files that parse.' : `Add ${file.file} to ${file.branch} and it appears here.`}</p>
          <ul>
            {file.problems.map((p, i) => (
              <li key={i} className="mono">
                {p.line ? `line ${p.line}: ` : ''}
                {p.path ? `${p.path}: ` : ''}
                {p.message}
              </li>
            ))}
          </ul>
          {file.content && <pre>{file.content}</pre>}
        </div>
      )}
      {cur && (
        <>
          <div className="pipe-toolbar" role="toolbar" aria-label="Pipeline edits">
            <button className="secondary small" onClick={() => mutate((p) => addStep(p, [], null))}>
              + Step
            </button>
            <button className="secondary small" disabled={!oneStep} onClick={() => oneStep && mutate((p) => addStep(p, [one!.id], one!.group))}>
              Add after
            </button>
            <button className="secondary small" disabled={!oneStep} onClick={() => oneStep && mutate((p) => addStep(p, one!.needs, one!.group))}>
              Add in parallel
            </button>
            <span className="divider" />
            <button className="secondary small" disabled={!multiSteps} onClick={() => multiSteps && mutate((p) => mergeSteps(p, sel) ?? p)}>
              Merge into one step
            </button>
            <button className="secondary small" disabled={!multiSteps} onClick={() => multiSteps && mutate((p) => groupSteps(p, sel, groupName))}>
              Group as stage
            </button>
            <span className="divider" />
            <button
              className="quiet"
              disabled={sel.length === 0}
              onClick={() => {
                mutate((p) => ({ model: removeSteps(p, sel) }))
                setSel([])
              }}
            >
              Remove
            </button>
            <span className="hint">{hint}</span>
          </div>
          {view === 'visual' ? (
            <div className="pipe-canvas" onClick={() => setSel([])}>
              <div className={`bands${sel.length ? ' with-panel' : ''}`}>
                {bandList.map((b, i) => (
                  <div key={b.key} className="band-wrap">
                    {i > 0 && <div className="band-link" />}
                    <section className="band" style={{ background: b.hue.bg, ['--band-fg' as string]: b.hue.fg, ['--band-chip' as string]: b.hue.chip, ['--band-line' as string]: b.hue.line }} aria-label={`Stage ${b.n}: ${b.name}`}>
                      <header className="band-head">
                        <span className="band-n">{b.n}</span>
                        <span className="band-name mono">{b.name}</span>
                        <span className="band-sub">{b.sub}</span>
                        <span className="band-est mono">{b.est}</span>
                      </header>
                      <div className="band-grid" style={{ width: b.w, height: b.h }}>
                        <svg className="band-lines" width={b.w} height={b.h} aria-hidden="true">
                          {b.edges.map((e, j) => (
                            <g key={j}>
                              <path d={e.d} fill="none" stroke={e.hot ? 'var(--accent)' : b.hue.line} strokeWidth={e.hot ? 2 : 1.5} />
                              <circle cx={e.ex} cy={e.ey} r={3} fill={e.hot ? 'var(--accent)' : b.hue.line} />
                            </g>
                          ))}
                        </svg>
                        {b.cards.map((c) => (
                          <button key={c.id} className={`card${c.sel ? ' sel' : ''}`} style={{ left: c.x, top: c.y, width: CW, height: CH }} aria-pressed={c.sel} aria-label={c.id} title={`${c.id} · ${c.image} · ${c.last}`} onClick={(e) => pick(c.id, e)}>
                            <span className="l1">
                              <span className="name mono">{c.id}</span>
                              <span className="image mono">{c.image}</span>
                              <span className={`last mono${c.hasLast ? '' : ' none'}`}>{c.last}</span>
                            </span>
                            <span className="l2">
                              <span className="cmd mono">{c.cmd}</span>
                              <span className="chips">
                                {c.chips.map((ch, k) => (
                                  <span key={k} className="chip-sm mono" style={{ color: ch.fg, borderColor: ch.bc, background: ch.bg }} title={ch.title}>
                                    {ch.t}
                                  </span>
                                ))}
                              </span>
                            </span>
                          </button>
                        ))}
                      </div>
                    </section>
                  </div>
                ))}
              </div>
              {sel.length > 0 && (
                <Inspector cur={cur} sel={sel} mutate={mutate} close={() => setSel([])} setSel={setSel} groupName={groupName} setGroupName={setGroupName} profiles={profiles} />
              )}
            </div>
          ) : (
            <YamlView cur={cur} base={base} original={file?.content ?? null} />
          )}
          {(dirty || saved) && (
            <div className="edit-bar">
              {saved && !dirty ? (
                <div className="saved" role="status">
                  <span className="dot" aria-hidden="true" />
                  <span>
                    <b>Change opened.</b> <span className="mono">{saved.change.id}</span> on <span className="mono">{saved.change.source_branch}</span> is validating with the pipeline on {saved.change.target_branch}. The new pipeline applies when it merges.
                  </span>
                  <button className="secondary small" onClick={() => go({ screen: 'change', id: saved.change.id })}>
                    Open change
                  </button>
                  <button className="quiet" aria-label="Dismiss" onClick={() => setSaved(null)}>
                    ×
                  </button>
                </div>
              ) : (
                <>
                  <div className="edit-row">
                    <b>
                      {summary.length} edit{summary.length === 1 ? '' : 's'}
                    </b>
                    <span className="mono summary">{summary.join(' · ')}</span>
                    {err && <span className="err">{err}</span>}
                    <button className="secondary small" onClick={discard}>
                      Discard
                    </button>
                    <button className="primary" style={{ height: 28 }} onClick={() => setSaveOpen((o) => !o)} aria-expanded={saveOpen}>
                      Save as change
                    </button>
                  </div>
                  {saveOpen && (
                    <form className="save-form" onSubmit={(e) => void push(e)}>
                      <label>
                        Branch
                        <input className="mono" value={form.branch ?? defaults.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} />
                      </label>
                      <label>
                        Title
                        <input value={form.title ?? defaults.title} onChange={(e) => setForm({ ...form, title: e.target.value })} />
                      </label>
                      <label className="wide">
                        Intent
                        <textarea rows={3} value={form.intent ?? defaults.intent} onChange={(e) => setForm({ ...form, intent: e.target.value })} />
                      </label>
                      <div className="criteria">
                        <span className="label">Criteria added to the change</span>
                        <span>AC-1 The pipeline file parses and its step graph has no cycles</span>
                        <span>AC-2 Every step image can be pulled and every secret a step names exists</span>
                        <span className="dim">The change is validated with the pipeline on {file?.branch ?? 'the target branch'}. A pipeline change always waits for a person.</span>
                      </div>
                      <div className="actions">
                        <button className="primary" type="submit" disabled={pushing}>
                          {pushing ? 'Pushing…' : 'Push branch and open change'}
                        </button>
                      </div>
                    </form>
                  )}
                </>
              )}
            </div>
          )}
        </>
      )}
    </div>
  )
}

function Header({ repos, repo, edits, file, view, setView, go }: { repos: Repo[]; repo?: Repo; edits: Record<string, PModel>; file: PipelineFile | null; view: 'visual' | 'yaml'; setView: (v: 'visual' | 'yaml') => void; go: (to: string) => void }) {
  return (
    <div className="sticky">
      <div className="page-header pipe-header">
        <h1>Pipeline</h1>
        {repos.length > 0 && (
          <div className="segmented mono" role="tablist" aria-label="Repository">
            {repos.map((r) => (
              <button key={r.id} role="tab" aria-pressed={repo?.id === r.id} aria-selected={repo?.id === r.id} onClick={() => go(`/pipelines/${encodeURIComponent(r.id)}`)}>
                {repoName(r)}
                {edits[r.id] ? ' •' : ''}
              </button>
            ))}
          </div>
        )}
        {file && (
          <span className="mono ref">
            {file.file} at {file.branch} · {file.sha.slice(0, 7)}
          </span>
        )}
        <span className="spacer" />
        <div className="segmented" aria-label="View">
          <button aria-pressed={view === 'visual'} onClick={() => setView('visual')}>
            Visual
          </button>
          <button aria-pressed={view === 'yaml'} onClick={() => setView('yaml')}>
            YAML
          </button>
        </div>
      </div>
    </div>
  )
}

function YamlView({ cur, base, original }: { cur: PModel; base: PModel | null; original: string | null }) {
  const lines = yamlDiff(cur, base, original)
  return (
    <div className="yaml-view" role="region" aria-label="Pipeline YAML">
      <pre>
        {lines.map((l) => (
          <div key={l.n} className={`yl${l.added ? ' add' : ''}`}>
            <span className="ln">{l.n}</span>
            <span className="mark">{l.added ? '+' : ''}</span>
            <span>{l.t}</span>
          </div>
        ))}
      </pre>
      <p className="foot">Read-only here, and exactly what a save commits: your edits applied to the file, its comments and formatting kept. Edit visually, or change the file in the repository and submit it as a change.</p>
    </div>
  )
}

type Mutate = (fn: (p: PModel) => PModel | { model: PModel; id?: string }) => void

function Inspector({ cur, sel, mutate, close, setSel, groupName, setGroupName, profiles }: { cur: PModel; sel: string[]; mutate: Mutate; close: () => void; setSel: (s: string[]) => void; groupName: string; setGroupName: (s: string) => void; profiles: ReviewerProfile[] }) {
  const [pathDraft, setPathDraft] = useState('')
  const [nameDraft, setNameDraft] = useState<string | null>(null)
  const s = sel.length === 1 ? find(cur, sel[0]) : undefined
  useEffect(() => setNameDraft(null), [sel])
  if (!s) {
    const selSteps = sel.map((id) => find(cur, id)).filter((x): x is PStep => !!x)
    const images = new Set(selSteps.map((x) => x.image))
    return (
      <aside className="inspector" aria-label="Selection" onClick={(e) => e.stopPropagation()}>
        <button className="close" aria-label="Close" onClick={close}>
          ×
        </button>
        <header>
          <b>{sel.length} steps selected</b>
        </header>
        <div className="ids">
          {sel.map((id) => (
            <span key={id} className="chip-sm mono">
              {id}
            </span>
          ))}
        </div>
        <section>
          <h4>Merge into one step</h4>
          <p>One container runs their commands in order, with one log. Needs, env and evidence are joined; the longest timeout and the highest retries win.</p>
          {images.size > 1 && <p className="warn">Images differ; the merged step takes the first one.</p>}
          <button className="secondary small" onClick={() => mutate((p) => mergeSteps(p, sel) ?? p)}>
            Merge
          </button>
        </section>
        <section>
          <h4>Group as stage</h4>
          <p>The steps stay separate, with separate logs, and show as one band.</p>
          <div className="row">
            <input className="mono" aria-label="Stage name" value={groupName} onChange={(e) => setGroupName(e.target.value)} />
            <button className="secondary small" onClick={() => mutate((p) => groupSteps(p, sel, groupName))}>
              Group
            </button>
          </div>
        </section>
      </aside>
    )
  }
  const id = s.id
  const deps = plain(cur).filter((x) => x.needs.includes(id)).map((x) => x.id)
  const kind = s.validator ? 'Validator' : s.needs.length > 1 ? `Step · joins ${s.needs.length}` : deps.length > 1 ? `Step · forks to ${deps.length}` : 'Step'
  const set = <K extends keyof PStep>(f: K, v: PStep[K]) => mutate((p) => setField(p, id, f, v))
  const opts = needOptions(cur, id)
  const commitName = () => {
    if (nameDraft == null || nameDraft === id) return
    mutate((p) => {
      const r = renameStep(p, id, nameDraft)
      return { model: r.model, id: r.id }
    })
    setNameDraft(null)
  }
  const downstream = Array.from(dependents(cur, id)).filter((x) => x !== id)
  // A profile answers one family (A29): the loop's for external steps, the rest for pipeline steps.
  const fitting = profiles.filter((p) => (p.kind === 'claude-code') === s.external)
  return (
    <aside className="inspector" aria-label={kind} onClick={(e) => e.stopPropagation()}>
      <button className="close" aria-label="Close" onClick={close}>
        ×
      </button>
      <header>
        <b>{kind}</b>
        <span className="mono dim">{s.last != null ? `last run ${fmtSeconds(s.last)}` : 'not run yet'}</span>
      </header>
      <label>
        Name
        <input className="mono" aria-label="Name" value={nameDraft ?? id} onChange={(e) => setNameDraft(e.target.value)} onBlur={commitName} onKeyDown={(e) => e.key === 'Enter' && commitName()} />
      </label>
      {s.external && <p className="fixed">External validator: no image and no commands. A reviewer outside the pipeline, plumbix review --watch with Claude Code, answers it with the profile below.</p>}
      {!s.external && (
      <>
      <label>
        Image
        <input className="mono" aria-label="Image" value={s.image} onChange={(e) => set('image', e.target.value)} />
      </label>
      <label>
        Commands
        <textarea className="mono" aria-label="Commands" rows={Math.max(2, Math.min(6, s.run.length + 1))} value={s.run.join('\n')} onChange={(e) => set('run', e.target.value.split('\n'))} />
        <span className="dim">One per line, run in order; the step stops at the first failure.</span>
      </label>
      </>
      )}
      {s.validator ? (
        <p className="fixed">
          A validator has no commands: its image's entrypoint is the validator. It starts with the change's inputs mounted read-only at /plumbix/in and the workspace at /workspace, and must write /plumbix/out/verdict.json. Plumbix ships plumbix-reference-validator (deterministic checks) and plumbix-ai-validator (asks the reviewer profile's model); any image honouring the validator contract works. Validators run after every step, in file order, and cannot be conditional or allowed to fail.
        </p>
      ) : (
        <>
          <div className="group">
            <span className="label">Needs</span>
            <div className="chips">
              {s.needs.map((n) => (
                <span key={n} className="chip-sm mono removable">
                  {n}
                  <button aria-label={`Remove need ${n}`} onClick={() => set('needs', s.needs.filter((x) => x !== n))}>
                    ×
                  </button>
                </span>
              ))}
              {s.needs.length === 0 && <span className="dim">none · starts right away</span>}
              {opts.length > 0 && (
                <select aria-label="Add need" value="" onChange={(e) => e.target.value && set('needs', s.needs.concat(e.target.value))}>
                  <option value="">+ add</option>
                  {opts.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              )}
            </div>
          </div>
          <div className="group">
            <span className="label">Run only if these paths changed</span>
            <div className="chips">
              {s.paths.map((g) => (
                <span key={g} className="chip-sm mono removable">
                  {g}
                  <button aria-label={`Remove path ${g}`} onClick={() => set('paths', s.paths.filter((x) => x !== g))}>
                    ×
                  </button>
                </span>
              ))}
              <input
                className="mono dashed"
                aria-label="Add path glob"
                placeholder={s.paths.length ? '+ path' : 'always runs · add glob'}
                value={pathDraft}
                onChange={(e) => setPathDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && pathDraft.trim()) {
                    e.preventDefault()
                    const v = pathDraft.trim()
                    if (!s.paths.includes(v)) set('paths', s.paths.concat(v))
                    setPathDraft('')
                  }
                }}
              />
            </div>
          </div>
        </>
      )}
      <div className="two">
        <label>
          Timeout
          <input className="mono" aria-label="Timeout" placeholder="default" value={s.timeout} onChange={(e) => set('timeout', e.target.value)} />
        </label>
        <div className="stepper">
          <span className="label">Retries</span>
          <div className="row">
            <button aria-label="Fewer retries" onClick={() => set('retries', Math.max(0, s.retries - 1))}>
              −
            </button>
            <span className="mono" aria-label="Retries">
              {s.retries}
            </span>
            <button aria-label="More retries" onClick={() => set('retries', Math.min(5, s.retries + 1))}>
              +
            </button>
          </div>
        </div>
      </div>
      {!s.validator && (
        <Switch on={s.allowFail} label="Allowed to fail" sub={s.allowFail ? 'A failure is recorded as evidence but does not stop the steps that need this one.' : 'A failure stops the steps that need this one.'} toggle={() => set('allowFail', !s.allowFail)} />
      )}
      <Switch on={s.validator} label="Validator" sub={s.validator ? 'Judges the change from evidence: the image is the validator, no commands. Runs in the final phase.' : 'Turn on to make this step a validator: its image\'s entrypoint judges the change in the final phase, no commands.'} toggle={() => mutate((p) => toggleValidator(p, id))} />
      {s.validator && (
        <Switch on={s.external} label="External" sub={s.external ? 'Answered outside the pipeline: plumbix review --watch on a machine with Claude Code reads the change and posts the verdict. No image, commands or secrets.' : 'Turn on to have the review loop answer this step with Claude Code instead of running an image in the pipeline.'} toggle={() => mutate((p) => toggleExternal(p, id))} />
      )}
      {s.validator && <Switch on={s.advisory} label="Advisory" sub={s.advisory ? 'Its verdict is recorded and shown, but never counted: it cannot approve, reject or call for a person. For comparing a second model.' : 'Its verdict counts like any validator. Turn on to record it without counting it.'} toggle={() => set('advisory', !s.advisory)} />}
      {s.validator && (
        <div className="group">
          <span className="label">Reviewer</span>
          <label>
            Profile
            <select aria-label="Reviewer profile" value={s.reviewer} onChange={(e) => set('reviewer', e.target.value)}>
              <option value="">Default for this kind of step</option>
              {fitting.map((p) => (
                <option key={p.name} value={p.name}>
                  {p.name} · {p.kind === 'claude-code' ? 'Claude Code' : p.kind === 'claude-code-image' ? 'Claude Code in the pipeline' : p.kind === 'anthropic' ? 'Anthropic API' : 'OpenAI-compatible'}
                  {p.model ? ` · ${p.model}` : ''}
                  {p.effort ? ` · effort ${p.effort}` : ''}
                </option>
              ))}
              {s.reviewer && !fitting.some((p) => p.name === s.reviewer) && (
                <option value={s.reviewer}>
                  {s.reviewer} · {profiles.some((p) => p.name === s.reviewer) ? (s.external ? 'runs in the pipeline, not for an external step' : 'the review loop\'s, not for a pipeline step') : 'not defined in Settings'}
                </option>
              )}
            </select>
            <span className="dim">Which model judges, from Settings. {s.external ? 'An external step takes a Claude Code profile of the review loop.' : 'A pipeline step takes an API profile or Claude Code in the pipeline.'}</span>
          </label>
          {!s.external && (
          <label>
            Reasoning effort
            <select aria-label="Reasoning effort" value={s.effort} onChange={(e) => set('effort', e.target.value as PStep['effort'])}>
              <option value="">Profile's setting</option>
              <option value="low">Low: fastest and cheapest</option>
              <option value="medium">Medium</option>
              <option value="high">High: slow and thorough</option>
            </select>
            <span className="dim">For API reviewers; overrides the profile for this step.</span>
          </label>
          )}
        </div>
      )}
      {!s.external && (
      <div className="group">
        <span className="label">Environment and secrets</span>
        {s.env.map((e, i) => (
          <div key={i} className="env-row">
            <input className="mono" aria-label={`Variable ${i + 1} name`} value={e.k} placeholder="NAME" onChange={(ev) => set('env', s.env.map((x, j) => (j === i ? { ...x, k: ev.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, '') } : x)))} />
            {e.secret ? (
              <span className="dim secret-note">from Settings, as ${e.k || 'NAME'}</span>
            ) : (
              <input className="mono" aria-label={`Variable ${i + 1} value`} value={e.v} placeholder="value" onChange={(ev) => set('env', s.env.map((x, j) => (j === i ? { ...x, v: ev.target.value } : x)))} />
            )}
            <button className={`toggle${e.secret ? ' on' : ''}`} aria-pressed={e.secret} onClick={() => set('env', s.env.map((x, j) => (j === i ? { ...x, secret: !x.secret, v: x.secret ? x.v : '' } : x)))}>
              secret
            </button>
            <button className="quiet" aria-label={`Remove variable ${i + 1}`} onClick={() => set('env', s.env.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>
        ))}
        <button className="quiet add" onClick={() => set('env', s.env.concat({ k: '', v: '', secret: false }))}>
          + variable
        </button>
      </div>
      )}
      <div className="group">
        <span className="label">Evidence outputs</span>
        {s.evidence.map((v, i) => (
          <div key={i} className="env-row">
            <input className="mono wide" aria-label={`Evidence path ${i + 1}`} value={v} onChange={(ev) => set('evidence', s.evidence.map((x, j) => (j === i ? ev.target.value : x)))} />
            <button className="quiet" aria-label={`Remove evidence path ${i + 1}`} onClick={() => set('evidence', s.evidence.filter((_, j) => j !== i))}>
              ×
            </button>
          </div>
        ))}
        <button className="quiet add" onClick={() => set('evidence', s.evidence.concat(`evidence/${id}/`))}>
          + path
        </button>
      </div>
      {!s.validator && (
        <div className="group">
          <span className="label">Caches kept between runs</span>
          <p className="sub">Absolute paths inside the container, kept per repository: /go/pkg/mod and /root/.cache/go-build for Go, /root/.npm for npm, /root/.cache/pip for pip.</p>
          {s.cache.map((v, i) => (
            <div key={i} className="env-row">
              <input className="mono wide" aria-label={`Cache path ${i + 1}`} value={v} onChange={(ev) => set('cache', s.cache.map((x, j) => (j === i ? ev.target.value : x)))} />
              <button className="quiet" aria-label={`Remove cache path ${i + 1}`} onClick={() => set('cache', s.cache.filter((_, j) => j !== i))}>
                ×
              </button>
            </div>
          ))}
          <button className="quiet add" onClick={() => set('cache', s.cache.concat('/'))}>
            + cache path
          </button>
        </div>
      )}
      {s.group && (
        <div className="group row between">
          <span>
            In stage <span className="mono">{s.group}</span>
          </span>
          <button className="quiet" onClick={() => set('group', null)}>
            Remove from stage
          </button>
        </div>
      )}
      <div className="group">
        <button
          className="quiet danger"
          onClick={() => {
            mutate((p) => ({ model: removeSteps(p, [id]) }))
            setSel([])
          }}
        >
          Remove step
        </button>
        {downstream.length > 0 && !s.validator && (
          <span className="dim">
            {deps.join(', ')} will wait for {s.needs.length ? s.needs.join(', ') : 'nothing'} instead.
          </span>
        )}
      </div>
    </aside>
  )
}

function Switch({ on, label, sub, toggle }: { on: boolean; label: string; sub: string; toggle: () => void }) {
  return (
    <div className="switch-row">
      <button role="switch" aria-checked={on} aria-label={label} className={`switch${on ? ' on' : ''}`} onClick={toggle}>
        <span className="knob" />
      </button>
      <span>
        <b>{label}</b>
        <span className="dim">{sub}</span>
      </span>
    </div>
  )
}
