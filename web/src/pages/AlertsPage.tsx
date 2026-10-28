// Alerts: what a person should look at. Open ones first, resolved ones
// behind a toggle.
import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import { agoLong, dateTime, minutesBetween } from '../format'
import type { Alert } from '../model'
import { useNow, useStore } from '../store'
import { StatePill } from '../ui/StatePill'
import { repoName } from '../vm/changes'

interface Commit {
  sha: string
  author: string
  time: string
  subject: string
}

function kindLabel(a: Alert): string {
  if (a.kind === 'bypass_commit') return 'Commits without a verdict'
  if (a.kind === 'no_reviewer') return 'No reviewer is answering external reviews'
  if (a.kind === 'pipeline_unapproved') return 'Pipeline file not approved: runs get no secrets'
  if (a.kind === 'gate_bypassed') return 'Pull request merged past the gate'
  return /check failed/i.test(a.message) ? 'Token check failed' : 'Token expires soon'
}

function hint(a: Alert): string {
  if (a.kind === 'bypass_commit') return 'If this was intended, resolve the alert. Otherwise revert the commit and submit it as a change.'
  if (a.kind === 'no_reviewer') return 'Start plumbix review --watch on a machine with Claude Code, or plumbix review install to keep one running. The alert resolves by itself when a reviewer polls.'
  if (a.kind === 'pipeline_unapproved') return 'The file reached a protected branch without a person approving it. Review it, then submit the file as a change: once that merges, its runs get their secrets and the alert resolves by itself.'
  if (a.kind === 'gate_bypassed') return 'GitHub merged the pull request while its change was not approved, or while plumbix/gate was not a required check on the target. Make plumbix/gate required in branch protection or a ruleset; who overrode it is in GitHub\'s audit log.'
  if (/check failed/i.test(a.message)) return 'Check the token in the secrets file. Plumbix repeats the check daily and resolves this alert when it passes.'
  return 'Create a new token on the git host and put it in the secrets file before then.'
}

function commitsOf(a: Alert): Commit[] {
  const raw = a.details?.commits
  return Array.isArray(raw) ? (raw as Commit[]) : []
}

export function AlertsPage() {
  const { state, actions } = useStore()
  const now = useNow()
  const [showResolved, setShowResolved] = useState(false)
  const [resolved, setResolved] = useState<Alert[]>([])

  const loadResolved = useCallback(async () => {
    try {
      const all = await api.alerts(true)
      setResolved(all.filter((a) => a.resolved_at))
    } catch {
      // the store's unauthorized handling takes over
    }
  }, [])

  useEffect(() => {
    if (state.loaded) void loadResolved()
  }, [state.loaded, state.alerts, loadResolved])

  const open = state.alerts.filter((a) => !a.resolved_at)

  async function resolve(id: string) {
    try {
      await api.resolveAlert(id)
      await actions.refreshAlerts()
      await loadResolved()
    } catch {
      await actions.refreshAlerts()
    }
  }

  const item = (a: Alert) => {
    const repo = state.repos.find((r) => r.id === a.repo_id)
    const done = !!a.resolved_at
    const commits = commitsOf(a)
    return (
      <div key={a.id} className={done ? 'alert resolved' : 'alert'}>
        <span className="icon">
          <StatePill status={done ? 'resolved' : 'alert'} mode="icon" />
        </span>
        <div className="body">
          <div className="head">
            <span className="kind">{kindLabel(a)}</span>
            <span className="mono dim">{a.kind}</span>
            {repo && <span className="mono dim2">{repoName(repo)}</span>}
            <span className="dim when">{agoLong(minutesBetween(a.created_at, now))}</span>
          </div>
          <span className="message">{a.message}</span>
          {commits.length > 0 && (
            <div className="commits">
              {commits.map((c) => (
                <div key={c.sha} className="commit">
                  <span>{c.sha.slice(0, 12)}</span>
                  <span className="dim2">{c.author}</span>
                  <span className="dim">{dateTime(c.time)}</span>
                  <span className="subject">{c.subject}</span>
                </div>
              ))}
            </div>
          )}
          {!done && <span className="hint">{hint(a)}</span>}
        </div>
        {done ? (
          <span className="dim small">Resolved {agoLong(minutesBetween(a.resolved_at!, now))}</span>
        ) : (
          <button className="quiet" onClick={() => void resolve(a.id)}>
            Resolve
          </button>
        )}
      </div>
    )
  }

  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Alerts</h1>
          <span className="count mono">{state.loaded ? `${open.length} open` : ''}</span>
          <span className="spacer" />
          <button className="secondary small" aria-pressed={showResolved} onClick={() => setShowResolved(!showResolved)}>
            {showResolved ? 'Hide resolved' : 'Show resolved'} ({resolved.length})
          </button>
        </div>
      </div>
      <div className="alerts">
        {state.loaded && open.length === 0 && <div className="none wide">No open alerts. Plumbix raises one when commits reach a protected branch without a verdict, or when a git host token is about to expire.</div>}
        {open.map(item)}
        {showResolved && resolved.length > 0 && (
          <>
            <div className="resolved-label">Resolved</div>
            {resolved.map(item)}
          </>
        )}
      </div>
    </>
  )
}
