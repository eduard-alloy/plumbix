import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { StatePill, ENCODING, type PillStatus } from './StatePill'

describe('StatePill', () => {
  it('has an entry for every status in the encoding table', () => {
    const keys: PillStatus[] = [
      'open', 'running', 'approved', 'fix_requested', 'rejected', 'needs_human', 'escalated', 'merged', 'closed',
      'finished', 'errored', 'cancelled',
      'succeeded', 'failed', 'timed_out', 'skipped', 'queued',
      'met', 'unmet', 'unknown', 'pending',
      'blocker', 'major', 'minor',
      'human_required', 'alert', 'resolved', 'info',
    ]
    for (const k of keys) {
      expect(ENCODING[k], k).toBeDefined()
      expect(ENCODING[k].label, k).not.toBe('')
    }
  })

  it('gives open, merged and closed a colour of their own', () => {
    expect(ENCODING.open.tone).toBe('blue')
    expect(ENCODING.merged.tone).toBe('teal')
    expect(ENCODING.closed.tone).toBe('plum')
    expect(ENCODING.running.tone).toBe('blue')
    for (const tone of ['teal', 'plum']) {
      const { container } = render(<StatePill status={tone === 'teal' ? 'merged' : 'closed'} />)
      expect((container.firstElementChild as HTMLElement).dataset.tone).toBe(tone)
    }
  })

  it('carries that colour on the border only, not as a fill', () => {
    for (const status of ['open', 'merged', 'closed'] as const) {
      expect(ENCODING[status].outline, status).toBe(true)
      expect(ENCODING[status].filled, status).toBeFalsy()
      const { container } = render(<StatePill status={status} />)
      expect((container.firstElementChild as HTMLElement).dataset.outline, status).toBe('true')
    }
    expect(ENCODING.approved.outline).toBeFalsy()
    const { container } = render(<StatePill status="approved" />)
    expect((container.firstElementChild as HTMLElement).dataset.outline).toBeUndefined()
  })

  it('renders icon and label in pill mode, with the tone as data', () => {
    const { container } = render(<StatePill status="fix_requested" />)
    const pill = container.firstElementChild as HTMLElement
    expect(pill.textContent).toBe('Fix requested')
    expect(pill.querySelector('svg')).not.toBeNull()
    expect(pill.dataset.tone).toBe('amber')
    expect(pill.dataset.mode).toBe('pill')
    expect(pill.title).toBe('Fix requested')
  })

  it('keeps the label for screen readers in icon mode', () => {
    const { container } = render(<StatePill status="met" mode="icon" />)
    const pill = container.firstElementChild as HTMLElement
    expect(pill.querySelector('svg')).not.toBeNull()
    expect(screen.getByText('Met').className).toContain('sr-only')
  })

  it('shows only the label in text mode and accepts a custom label', () => {
    const { container } = render(<StatePill status="approved" mode="text" label="Signed in" />)
    const pill = container.firstElementChild as HTMLElement
    expect(pill.querySelector('svg')).toBeNull()
    expect(pill.textContent).toBe('Signed in')
  })

  it('draws a dashed circle for queued and pending, a filled one for blockers', () => {
    const dashed = render(<StatePill status="queued" />).container.querySelector('circle')!
    expect(dashed.getAttribute('stroke-dasharray')).toBe('2.1 2.1')
    const filled = render(<StatePill status="blocker" />).container.querySelector('circle')!
    expect(filled.getAttribute('fill')).toBe('currentColor')
    const half = render(<StatePill status="running" />).container.querySelector('path')!
    expect(half.getAttribute('fill')).toBe('currentColor')
  })
})
