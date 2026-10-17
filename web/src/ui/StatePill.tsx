// StatePill is the one badge shape for every state in the interface: a
// 14px icon (circle plus one glyph) and a label, in four modes. Colour
// never stands alone: there is always an icon and a label, visible or for
// screen readers only. The table mirrors web/design/README.md; the colours
// come from the status palette in tokens.css (web/design/status-palette.md).
import type { CSSProperties } from 'react'
import './pill.css'

/** Palette is a status in web/design/status-palette.css; each has base, fg, bg and border tokens. */
export type Palette =
  | 'open' | 'running' | 'queued' | 'pending'
  | 'approved' | 'succeeded' | 'met' | 'finished' | 'merged'
  | 'needs-human' | 'escalated' | 'errored'
  | 'fix-requested' | 'major'
  | 'rejected' | 'failed' | 'unmet' | 'timed-out' | 'blocker'
  | 'closed' | 'cancelled' | 'skipped'
  | 'unknown' | 'minor'

export type PillStatus =
  | 'open' | 'running' | 'approved' | 'fix_requested' | 'rejected' | 'needs_human' | 'escalated' | 'merged' | 'closed'
  | 'finished' | 'errored' | 'cancelled'
  | 'succeeded' | 'failed' | 'timed_out' | 'skipped' | 'queued'
  | 'met' | 'unmet' | 'unknown' | 'pending'
  | 'blocker' | 'major' | 'minor'
  | 'human_required' | 'alert' | 'resolved' | 'info'

type Glyph = 'check' | 'x' | 'bang' | 'up' | 'ret' | 'slash' | 'clock' | 'dash' | 'q' | 'info' | 'half' | 'none'

interface Encoding {
  label: string
  palette: Palette
  glyph: Glyph
  filled?: boolean
  dashed?: boolean
}

const PATHS: Record<Exclude<Glyph, 'half' | 'none'>, string> = {
  check: 'M5.4 8.3l1.8 1.8 3.4-3.8',
  x: 'M6 6l4 4M10 6l-4 4',
  bang: 'M8 4.9v3.5M8 11v.01',
  up: 'M8 11V5.4M5.8 7.5 8 5.3l2.2 2.2',
  ret: 'M10.6 10.8V9.4a2 2 0 0 0-2-2H5.6M7.3 5.6 5.5 7.4l1.8 1.8',
  slash: 'M5.5 10.5l5-5',
  clock: 'M8 5v3.2l2 1.3',
  dash: 'M5.4 8h5.2',
  q: 'M6.5 6.6a1.5 1.5 0 1 1 2.1 1.4c-.4.2-.6.5-.6 1v.3M8 11v.01',
  info: 'M8 7.4v3.3M8 5.1v.01',
}

export const ENCODING: Record<PillStatus, Encoding> = {
  open: { label: 'Open', palette: 'open', glyph: 'none' },
  running: { label: 'Running', palette: 'running', glyph: 'half' },
  approved: { label: 'Approved', palette: 'approved', glyph: 'check' },
  fix_requested: { label: 'Fix requested', palette: 'fix-requested', glyph: 'ret' },
  rejected: { label: 'Rejected', palette: 'rejected', glyph: 'x' },
  needs_human: { label: 'Needs human', palette: 'needs-human', glyph: 'bang' },
  escalated: { label: 'Escalated', palette: 'escalated', glyph: 'up' },
  merged: { label: 'Merged', palette: 'merged', glyph: 'check', filled: true },
  closed: { label: 'Closed', palette: 'closed', glyph: 'slash' },
  finished: { label: 'Finished', palette: 'finished', glyph: 'check' },
  errored: { label: 'Errored', palette: 'errored', glyph: 'bang' },
  cancelled: { label: 'Cancelled', palette: 'cancelled', glyph: 'slash' },
  succeeded: { label: 'Succeeded', palette: 'succeeded', glyph: 'check' },
  failed: { label: 'Failed', palette: 'failed', glyph: 'x' },
  timed_out: { label: 'Timed out', palette: 'timed-out', glyph: 'clock' },
  skipped: { label: 'Skipped', palette: 'skipped', glyph: 'dash' },
  queued: { label: 'Queued', palette: 'queued', glyph: 'none', dashed: true },
  met: { label: 'Met', palette: 'met', glyph: 'check' },
  unmet: { label: 'Unmet', palette: 'unmet', glyph: 'x' },
  unknown: { label: 'Unknown', palette: 'unknown', glyph: 'q' },
  pending: { label: 'Pending', palette: 'pending', glyph: 'none', dashed: true },
  blocker: { label: 'Blocker', palette: 'blocker', glyph: 'bang', filled: true },
  major: { label: 'Major', palette: 'major', glyph: 'bang' },
  minor: { label: 'Minor', palette: 'minor', glyph: 'info' },
  human_required: { label: 'Human required', palette: 'needs-human', glyph: 'bang' },
  alert: { label: 'Open', palette: 'needs-human', glyph: 'bang' },
  resolved: { label: 'Resolved', palette: 'closed', glyph: 'check' },
  info: { label: 'Next step', palette: 'minor', glyph: 'info' },
}

export type PillMode = 'pill' | 'icon' | 'plain' | 'text'

export function StatePill({ status, mode = 'pill', label }: { status: PillStatus; mode?: PillMode; label?: string }) {
  const e = ENCODING[status] ?? ENCODING.open
  const text = label ?? e.label
  const half = e.glyph === 'half'
  const style = {
    '--pill-fg': `var(--status-${e.palette}-fg)`,
    '--pill-bg': `var(--status-${e.palette}-bg)`,
    '--pill-border': `var(--status-${e.palette}-border)`,
  } as CSSProperties
  return (
    <span className="pill" data-status={e.palette} data-mode={mode} title={text} style={style}>
      {mode !== 'text' && (
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <circle cx="8" cy="8" r="5.75" stroke="currentColor" strokeWidth="1.5" fill={e.filled ? 'currentColor' : 'none'} strokeDasharray={e.dashed ? '2.1 2.1' : undefined} />
          {half ? (
            <path d="M8 2.25a5.75 5.75 0 0 1 0 11.5z" fill="currentColor" />
          ) : e.glyph !== 'none' ? (
            <path d={PATHS[e.glyph as keyof typeof PATHS]} stroke={e.filled ? 'var(--bg)' : 'currentColor'} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          ) : null}
        </svg>
      )}
      {mode === 'icon' ? <span className="sr-only">{text}</span> : <span>{text}</span>}
    </span>
  )
}
