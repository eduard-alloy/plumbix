// StatePill is the one badge shape for every state in the interface: a
// 14px icon (circle plus one glyph) and a label, in four modes. Colour
// never stands alone: there is always an icon and a label, visible or for
// screen readers only. The table mirrors web/design/README.md.
import './pill.css'

export type Tone = 'neutral' | 'blue' | 'green' | 'amber' | 'red' | 'violet' | 'ink' | 'teal' | 'plum'

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
  tone: Tone
  glyph: Glyph
  filled?: boolean
  /** outline pills show their tone on the border and glyph only; the label stays ink and the background stays the page. */
  outline?: boolean
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
  open: { label: 'Open', tone: 'blue', glyph: 'none', outline: true },
  running: { label: 'Running', tone: 'blue', glyph: 'half' },
  approved: { label: 'Approved', tone: 'green', glyph: 'check' },
  fix_requested: { label: 'Fix requested', tone: 'amber', glyph: 'ret' },
  rejected: { label: 'Rejected', tone: 'red', glyph: 'x' },
  needs_human: { label: 'Needs human', tone: 'violet', glyph: 'bang' },
  escalated: { label: 'Escalated', tone: 'violet', glyph: 'up' },
  merged: { label: 'Merged', tone: 'teal', glyph: 'check', outline: true },
  closed: { label: 'Closed', tone: 'plum', glyph: 'slash', outline: true },
  finished: { label: 'Finished', tone: 'neutral', glyph: 'check' },
  errored: { label: 'Errored', tone: 'violet', glyph: 'bang' },
  cancelled: { label: 'Cancelled', tone: 'neutral', glyph: 'slash' },
  succeeded: { label: 'Succeeded', tone: 'green', glyph: 'check' },
  failed: { label: 'Failed', tone: 'red', glyph: 'x' },
  timed_out: { label: 'Timed out', tone: 'red', glyph: 'clock' },
  skipped: { label: 'Skipped', tone: 'neutral', glyph: 'dash' },
  queued: { label: 'Queued', tone: 'neutral', glyph: 'none', dashed: true },
  met: { label: 'Met', tone: 'green', glyph: 'check' },
  unmet: { label: 'Unmet', tone: 'red', glyph: 'x' },
  unknown: { label: 'Unknown', tone: 'neutral', glyph: 'q' },
  pending: { label: 'Pending', tone: 'neutral', glyph: 'none', dashed: true },
  blocker: { label: 'Blocker', tone: 'red', glyph: 'bang', filled: true },
  major: { label: 'Major', tone: 'amber', glyph: 'bang' },
  minor: { label: 'Minor', tone: 'neutral', glyph: 'info' },
  human_required: { label: 'Human required', tone: 'violet', glyph: 'bang' },
  alert: { label: 'Open', tone: 'violet', glyph: 'bang' },
  resolved: { label: 'Resolved', tone: 'neutral', glyph: 'check' },
  info: { label: 'Next step', tone: 'neutral', glyph: 'info' },
}

export type PillMode = 'pill' | 'icon' | 'plain' | 'text'

export function StatePill({ status, mode = 'pill', label }: { status: PillStatus; mode?: PillMode; label?: string }) {
  const e = ENCODING[status] ?? ENCODING.open
  const text = label ?? e.label
  const half = e.glyph === 'half'
  return (
    <span className="pill" data-tone={e.tone} data-mode={mode} data-outline={e.outline ? 'true' : undefined} title={text}>
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
