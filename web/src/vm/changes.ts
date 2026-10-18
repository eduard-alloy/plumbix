// The changes list: filtering, grouping, sorting and the row columns, as
// pure functions over API data.
import type { Change, ChangeState, Repo, Run } from '../model'
import { NEEDS_YOU } from '../model'
import { ago, duration, elapsedSeconds, minutesBetween, plural } from '../format'

export interface ListFilters {
  q: string
  repo: string // 'all' or a repo id
  state: string // 'all' or a change state
}

export interface RowVM {
  id: string
  state: ChangeState
  title: string
  repo: string
  src: string
  tgt: string
  verdict: string
  blockers: string
  iter: string
  by: string
  moved: string
  needs: boolean
}

export interface GroupVM {
  title: string
  count: number
  sub: string
  needs: boolean
  rows: RowVM[]
}

/** repoName is the last path segment of the remote URL, without .git. */
export function repoName(repo: Repo | undefined, fallback = ''): string {
  if (!repo) return fallback
  const trimmed = repo.url.replace(/\/+$/, '').replace(/\.git$/, '')
  const seg = trimmed.split(/[/:]/).pop()
  return seg || repo.id
}

/** latestRun is the newest run, by iteration order. */
export function latestRun(runs: Run[]): Run | undefined {
  return runs.length ? runs[runs.length - 1] : undefined
}

/** latestJudged is the newest run that produced a result. */
export function latestJudged(runs: Run[]): Run | undefined {
  for (let i = runs.length - 1; i >= 0; i--) {
    if (runs[i].result) return runs[i]
  }
  return undefined
}

export function matchesFilters(c: Change, f: ListFilters): boolean {
  const q = f.q.trim().toLowerCase()
  if (q && !`${c.title} ${c.id} ${c.source_branch}`.toLowerCase().includes(q)) return false
  if (f.repo !== 'all' && c.repo_id !== f.repo) return false
  if (f.state !== 'all' && c.state !== f.state) return false
  return true
}

/** verdictColumn is the few mono words a list row shows for its verdict. */
export function verdictColumn(c: Change, runs: Run[], now: Date): { text: string; blockers: string } {
  const run = latestRun(runs)
  switch (c.state) {
    case 'running':
      return { text: run ? `iteration ${run.iteration ?? 1} · ${duration(elapsedSeconds(run.started_at, now))}` : 'starting', blockers: '' }
    case 'open':
      return { text: 'waiting to validate', blockers: '' }
    case 'needs_human':
      return { text: run?.result?.reasons[0] ?? run?.error ?? 'needs a person', blockers: '' }
  }
  const judged = latestJudged(runs)?.result
  if (!judged) return { text: '', blockers: '' }
  const met = judged.criteria.filter((x) => x.status === 'met').length
  const blockers = judged.findings.filter((f) => f.severity === 'blocker' && !f.advisory).length // advisory findings are shown, not counted (WU-030)
  let text = `${met}/${judged.criteria.length} met`
  if (blockers) return { text, blockers: plural(blockers, 'blocker') }
  if (c.state !== 'closed') text += ' · 0 blockers'
  return { text, blockers: '' }
}

function row(c: Change, runs: Run[], repos: Repo[], now: Date): RowVM {
  const v = verdictColumn(c, runs, now)
  const run = latestRun(runs)
  return {
    id: c.id,
    state: c.state,
    title: c.title,
    repo: repoName(
      repos.find((r) => r.id === c.repo_id),
      c.repo_id,
    ),
    src: c.source_branch,
    tgt: c.target_branch,
    verdict: v.text,
    blockers: v.blockers,
    iter: run?.iteration ? String(run.iteration) : '—',
    by: c.submitted_by ?? '',
    moved: ago(minutesBetween(c.updated_at, now)),
    needs: NEEDS_YOU.includes(c.state),
  }
}

/** listGroups builds the list: Needs you first, then everything else. Within
 * a group, running changes come first, then newest activity first. */
export function listGroups(changes: Change[], runs: Record<string, Run[]>, repos: Repo[], f: ListFilters, now: Date): GroupVM[] {
  const kept = changes.filter((c) => matchesFilters(c, f))
  const order = (a: Change, b: Change) => {
    const ra = a.state === 'running' ? 0 : 1
    const rb = b.state === 'running' ? 0 : 1
    if (ra !== rb) return ra - rb
    return Date.parse(b.updated_at) - Date.parse(a.updated_at)
  }
  const needs = kept.filter((c) => NEEDS_YOU.includes(c.state)).sort(order)
  const rest = kept.filter((c) => !NEEDS_YOU.includes(c.state)).sort(order)
  const groups: GroupVM[] = []
  if (needs.length) {
    groups.push({ title: 'Needs you', count: needs.length, sub: 'Ready to merge, or waiting for a person', needs: true, rows: needs.map((c) => row(c, runs[c.id] ?? [], repos, now)) })
  }
  if (rest.length) {
    groups.push({ title: needs.length ? 'Everything else' : 'Changes', count: rest.length, sub: '', needs: false, rows: rest.map((c) => row(c, runs[c.id] ?? [], repos, now)) })
  }
  return groups
}
