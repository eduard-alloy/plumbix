import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { DiffView } from './DiffView'
import { App } from '../App'
import { approved, NOW } from '../test/fixtures'
import { diffWithHunks, install } from '../test/server'
import type { Finding } from '../model'

describe('DiffView', () => {
  const findings: Finding[] = [
    { validator: 'ai-review', severity: 'minor', message: 'Backoff ceiling is hard-coded at 8 s; consider a constant', file: 'internal/evidence/upload.go', line: 71 },
    { validator: 'ai-review', severity: 'major', message: 'Changelog entry is vague', file: 'CHANGELOG.md', line: 9 },
  ]

  it('renders files, hunks, numbered lines and signs', () => {
    const { container } = render(<DiffView diff={diffWithHunks()} findings={findings} highlight={null} />)
    const files = container.querySelectorAll('.diff-file')
    expect(files).toHaveLength(2)
    expect(files[0].textContent).toContain('internal/evidence/upload.go')
    expect(files[0].textContent).toContain('+3')
    expect(files[0].textContent).toContain('−1')
    expect(container.querySelector('.hunk-header')!.textContent).toContain('func (u *Uploader) put')
    const lines = container.querySelectorAll('.diff-line')
    expect(lines.length).toBeGreaterThan(5)
    const added = container.querySelectorAll('.diff-line[data-type="add"]')[1]
    expect(added.textContent).toContain('+')
    expect(added.querySelector('.new')!.textContent).toBe('71')
    expect(added.querySelector('.old')!.textContent).toBe('')
    const deleted = container.querySelector('.diff-line[data-type="del"]')!
    expect(deleted.querySelector('.old')!.textContent).toBe('70')
    expect(deleted.querySelector('.new')!.textContent).toBe('')
  })

  it('shows a finding inline under its line and marks the highlighted line or file', () => {
    const { container, rerender } = render(<DiffView diff={diffWithHunks()} findings={findings} highlight={{ file: 'internal/evidence/upload.go', line: 71 }} />)
    const note = screen.getByText('Backoff ceiling is hard-coded at 8 s; consider a constant').closest('.diff-note')!
    expect(note.previousElementSibling!.getAttribute('data-diff-hl')).toBe('1')
    expect(container.querySelectorAll('[data-diff-hl="1"]')).toHaveLength(1)
    rerender(<DiffView diff={diffWithHunks()} findings={findings} highlight={{ file: 'CHANGELOG.md', line: 9 }} />)
    expect(container.querySelector('.diff-file[data-diff-file-hl="1"]')!.textContent).toContain('CHANGELOG.md')
    expect(container.querySelectorAll('[data-diff-hl="1"]')).toHaveLength(0)
  })

  it('says so for binary and truncated diffs', () => {
    const d = diffWithHunks()
    d.files[1].binary = true
    d.files[1].hunks = undefined
    const { container, rerender } = render(<DiffView diff={d} findings={[]} highlight={null} />)
    expect(container.textContent).toContain('Binary file')
    rerender(<DiffView diff={{ ...d, truncated: true }} findings={[]} highlight={null} />)
    expect(container.textContent).toContain('too large')
  })
})

describe('Diff on the change page', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    install()
    window.history.replaceState(null, '', `/changes/${approved.id}`)
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('loads on demand and a file:line link opens it at the line', async () => {
    render(<App />)
    await screen.findByRole('complementary', { name: 'Verdict' })
    await screen.findByText(/2 files · \+4 −1 · a81d7c0\.\.9f3c1e2/)
    fireEvent.click(screen.getByRole('button', { name: 'Load diff' }))
    await screen.findByText(/func \(u \*Uploader\) put/)
    expect(screen.getByRole('button', { name: 'Hide diff' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Hide diff' }))
    expect(screen.queryByText(/func \(u \*Uploader\) put/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'internal/evidence/upload.go:71' }))
    await waitFor(() => expect(document.querySelector('[data-diff-hl="1"]')).not.toBeNull())
    expect(document.querySelector('[data-diff-hl="1"]')!.textContent).toContain('71')
  })
})
