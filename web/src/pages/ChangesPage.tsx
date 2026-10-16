// The home screen: every change, Needs you first, with filters, keyboard
// navigation and live rows.
import { useEffect, useMemo, useRef, useState } from 'react'
import { CHANGE_STATES } from '../model'
import { useRoute } from '../router'
import { useNow, useStore } from '../store'
import { StatePill } from '../ui/StatePill'
import { ENCODING } from '../ui/StatePill'
import { FOCUS_SEARCH } from '../ui/Shell'
import { listGroups, repoName, type ListFilters } from '../vm/changes'

const skeletonWidths = [62, 48, 70, 55, 40, 66, 52, 45, 60, 50]

function readFilters(): ListFilters {
  const q = new URLSearchParams(window.location.search)
  return { q: q.get('q') ?? '', repo: q.get('repo') ?? 'all', state: q.get('state') ?? 'all' }
}

function writeFilters(f: ListFilters) {
  const q = new URLSearchParams()
  if (f.q) q.set('q', f.q)
  if (f.repo !== 'all') q.set('repo', f.repo)
  if (f.state !== 'all') q.set('state', f.state)
  const search = q.toString()
  window.history.replaceState(null, '', search ? `/?${search}` : '/')
}

export function ChangesPage() {
  const { state } = useStore()
  const [, go] = useRoute()
  const now = useNow()
  const [filters, setFilters] = useState<ListFilters>(readFilters)
  const searchRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  useEffect(() => writeFilters(filters), [filters])

  useEffect(() => {
    const focus = () => searchRef.current?.focus()
    window.addEventListener(FOCUS_SEARCH, focus)
    return () => window.removeEventListener(FOCUS_SEARCH, focus)
  }, [])

  // ↑↓ and j/k move between rows, also from inside the search box.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = ['input', 'textarea', 'select'].includes((e.target as HTMLElement | null)?.tagName?.toLowerCase() ?? '')
      const down = e.key === 'ArrowDown' || (!typing && e.key === 'j')
      const up = e.key === 'ArrowUp' || (!typing && e.key === 'k')
      if (!down && !up) return
      if (typing && e.target !== searchRef.current) return
      e.preventDefault()
      const rows = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[data-row]') ?? [])
      if (!rows.length) return
      const i = rows.indexOf(document.activeElement as HTMLElement)
      const n = i < 0 ? 0 : Math.max(0, Math.min(rows.length - 1, i + (down ? 1 : -1)))
      rows[n].focus()
      rows[n].scrollIntoView?.({ block: 'nearest' })
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const groups = useMemo(() => listGroups(Object.values(state.changes), state.runs, state.repos, filters, now), [state.changes, state.runs, state.repos, filters, now])
  const total = groups.reduce((a, g) => a + g.count, 0)
  const hasFilters = filters.q !== '' || filters.repo !== 'all' || filters.state !== 'all'
  const clear = () => setFilters({ q: '', repo: 'all', state: 'all' })
  const noRepos = state.loaded && state.repos.length === 0 && Object.keys(state.changes).length === 0

  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Changes</h1>
          <span className="count mono">{state.loaded ? total : ''}</span>
          <span className="spacer" />
          <span className="hints">
            <span>
              <span className="mono">↑↓</span> move
            </span>
            <span>
              <span className="mono">↵</span> open
            </span>
            <span>
              <span className="mono">/</span> filter
            </span>
          </span>
        </div>
        <div className="filters">
          <div className="search">
            <input ref={searchRef} value={filters.q} onChange={(e) => setFilters({ ...filters, q: e.target.value })} placeholder="Filter by title, id or branch" aria-label="Filter changes" />
            <span className="kbd" aria-hidden="true">
              /
            </span>
          </div>
          <div role="group" aria-label="Repository" className="segmented">
            {[{ id: 'all', label: 'All repositories' }, ...state.repos.map((r) => ({ id: r.id, label: repoName(r) }))].map((o) => (
              <button key={o.id} aria-pressed={filters.repo === o.id} onClick={() => setFilters({ ...filters, repo: o.id })}>
                {o.label}
              </button>
            ))}
          </div>
          <select value={filters.state} onChange={(e) => setFilters({ ...filters, state: e.target.value })} aria-label="State">
            <option value="all">All states</option>
            {CHANGE_STATES.map((s) => (
              <option key={s} value={s}>
                {ENCODING[s].label}
              </option>
            ))}
          </select>
          {hasFilters && (
            <button className="quiet" onClick={clear}>
              Clear
            </button>
          )}
        </div>
        <div className="cols row-grid" aria-hidden="true">
          <span>State</span>
          <span>Change</span>
          <span>Repository · branch</span>
          <span>Verdict</span>
          <span className="right">Iter</span>
          <span>By</span>
          <span className="right">Moved</span>
        </div>
      </div>

      {!state.loaded && (
        <div aria-busy="true" aria-label="Loading changes">
          {skeletonWidths.map((w, i) => (
            <div key={i} className="row-grid skeleton-row">
              <span className="bar" style={{ width: 84 }} />
              <span className="bar" style={{ width: `${w}%` }} />
              <span className="bar thin" style={{ width: '70%' }} />
              <span className="bar thin" style={{ width: 90 }} />
              <span />
              <span className="bar thin" style={{ width: 32 }} />
              <span />
            </div>
          ))}
        </div>
      )}

      {noRepos && (
        <div className="empty">
          <h2>No repositories yet</h2>
          <p>Plumbix validates changes for repositories you register from the terminal. Register one, then point your agents at it.</p>
          <pre>
            <span className="dim">$ </span>plumbix repo add --host github --url https://github.com/you/repo{'\n'}
            <span className="dim">$ </span>plumbix submit -f change.json
          </pre>
        </div>
      )}

      {state.loaded && !noRepos && total === 0 && (
        <div className="no-match">
          No changes match these filters.
          <button className="secondary small" onClick={clear}>
            Clear filters
          </button>
        </div>
      )}

      {state.loaded && total > 0 && (
        <div role="list" aria-label="Changes" className="list" ref={listRef}>
          {groups.map((g) => (
            <div key={g.title} className={g.needs ? 'group needs' : 'group'}>
              <div className="group-header">
                <h2>{g.title}</h2>
                <span className="mono count">{g.count}</span>
                {g.sub && <span className="sub">{g.sub}</span>}
              </div>
              {g.rows.map((r) => (
                <button key={r.id} role="listitem" data-row="1" data-id={r.id} className="row row-grid" onClick={() => go({ screen: 'change', id: r.id })}>
                  <span className="cell">
                    <StatePill status={r.state} />
                  </span>
                  <span className="cell title">{r.title}</span>
                  <span className="cell mono dim2">
                    {r.repo}
                    <span className="dim"> · </span>
                    {r.src}
                    <span className="dim"> → </span>
                    {r.tgt}
                  </span>
                  <span className="cell mono dim2">
                    {r.verdict}
                    {r.blockers && <span className="red"> · {r.blockers}</span>}
                  </span>
                  <span className="cell mono dim right">{r.iter}</span>
                  <span className="cell dim2">{r.by}</span>
                  <span className="cell mono dim right">{r.moved}</span>
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
    </>
  )
}
