// Repositories: read-only in this release; registration happens in the CLI.
import { useStore } from '../store'
import { repoName } from '../vm/changes'

const HOST = { github: 'GitHub', azuredevops: 'Azure DevOps', git: 'git' }

export function ReposPage() {
  const { state } = useStore()
  const changes = Object.values(state.changes)
  return (
    <>
      <div className="sticky">
        <div className="page-header">
          <h1>Repositories</h1>
          <span className="count mono">{state.loaded ? state.repos.length : ''}</span>
        </div>
      </div>
      {state.loaded && state.repos.length === 0 && (
        <div className="empty">
          <h2>No repositories yet</h2>
          <p>In this release, repositories are registered from the terminal. They appear here as soon as you add one.</p>
          <pre>
            <span className="dim">$ </span>plumbix repo add --host github --url https://github.com/you/repo
          </pre>
        </div>
      )}
      {state.loaded && state.repos.length > 0 && (
        <div className="repos">
          <table className="repo-table">
            <thead>
              <tr>
                <th>Repository</th>
                <th>Host</th>
                <th>URL</th>
                <th>Protected branches</th>
                <th>Merge</th>
                <th className="right">Changes</th>
              </tr>
            </thead>
            <tbody>
              {state.repos.map((r) => (
                <tr key={r.id}>
                  <td className="mono name" title={r.id}>
                    {repoName(r)}
                  </td>
                  <td className="dim2">{HOST[r.host]}</td>
                  <td className="mono dim2 url">{r.url}</td>
                  <td className="mono">{r.protected_branches.join(', ')}</td>
                  <td className="mono dim2">{r.merge_strategy}</td>
                  <td className="mono dim right">{changes.filter((c) => c.repo_id === r.id).length}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="footnote">
            Read-only. Register and edit repositories from the terminal with <span className="mono dim2">plumbix repo</span>.
          </p>
        </div>
      )}
    </>
  )
}
