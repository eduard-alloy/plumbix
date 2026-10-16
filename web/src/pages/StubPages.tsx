// Alerts and Repositories arrive with WU-017. Until then the navigation
// works and the pages say so, with the counts they will show.
import { useStore } from '../store'

export function AlertsPage() {
  const { state } = useStore()
  const open = state.alerts.filter((a) => !a.resolved_at).length
  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Alerts</h1>
          <span className="count mono">{state.loaded ? `${open} open` : ''}</span>
        </div>
      </div>
      <div className="note">The alerts page arrives with the next work unit. Until then, run plumbix alert list in your terminal.</div>
    </>
  )
}

export function ReposPage() {
  const { state } = useStore()
  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Repositories</h1>
          <span className="count mono">{state.loaded ? state.repos.length : ''}</span>
        </div>
      </div>
      <div className="note">The repositories page arrives with the next work unit. Until then, run plumbix repo list in your terminal.</div>
    </>
  )
}
