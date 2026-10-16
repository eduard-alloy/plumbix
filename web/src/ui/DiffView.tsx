// The diff the validators judged: files, hunks and numbered lines, with
// findings shown inline under the line they point at.
import type { Diff, DiffFile, Finding } from '../model'
import { StatePill } from './StatePill'

export interface Highlight {
  file: string
  line: number
}

function fileHasLine(f: DiffFile, line: number): boolean {
  return !!f.hunks?.some((h) => h.lines.some((l) => l.type !== 'del' && l.new === line))
}

export function DiffView({ diff, findings, highlight }: { diff: Diff; findings: Finding[]; highlight: Highlight | null }) {
  return (
    <div className="diff">
      {diff.truncated && <div className="none">The diff is too large to show inline; counts only.</div>}
      {diff.files.map((f) => {
        const fileHl = !!highlight && highlight.file === f.path && !fileHasLine(f, highlight.line)
        return (
          <div key={f.path} className="diff-block">
            <div className="diff-file" data-diff-file-hl={fileHl ? '1' : '0'}>
              <span className="path">{f.path}</span>
              <span className="add">+{f.additions}</span>
              <span className="del">−{f.deletions}</span>
            </div>
            {f.binary && <div className="none mono">Binary file</div>}
            {f.hunks?.map((h, hi) => (
              <div key={hi} className="hunk">
                <div className="hunk-header">
                  @@ -{h.old_start},{h.old_lines} +{h.new_start},{h.new_lines} @@ {h.header}
                </div>
                {h.lines.map((l, li) => {
                  const hl = !!highlight && highlight.file === f.path && l.type !== 'del' && l.new === highlight.line
                  const notes = l.type === 'del' ? [] : findings.filter((x) => x.file === f.path && x.line === l.new)
                  return (
                    <div key={li}>
                      <div className="diff-line" data-type={l.type} data-diff-hl={hl ? '1' : '0'}>
                        <span className="old">{l.old ?? ''}</span>
                        <span className="new">{l.new ?? ''}</span>
                        <span className="sign">{l.type === 'add' ? '+' : l.type === 'del' ? '−' : ''}</span>
                        <span className="code">{l.text}</span>
                      </div>
                      {notes.map((n, ni) => (
                        <div key={ni} className="diff-note">
                          <StatePill status={n.severity} mode="plain" />
                          <span>{n.message}</span>
                          <span className="mono dim">{n.validator}</span>
                        </div>
                      ))}
                    </div>
                  )
                })}
              </div>
            ))}
          </div>
        )
      })}
    </div>
  )
}
