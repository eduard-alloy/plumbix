import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { approved, fixRequested, needsHuman, NOW, running } from '../test/fixtures'
import { emit, install, streamOpen, type FakeDB } from '../test/server'

let db: FakeDB

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  db = install()
})

afterEach(() => {
  cleanup() // unmount before the stubs go, or a late load would hit the real globals
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function open(id: string) {
  window.history.replaceState(null, '', `/changes/${id}`)
  render(<App />)
  return screen.findByRole('complementary', { name: 'Verdict' })
}

describe('Change page', () => {
  it('shows intent, criteria, verdict, findings, risks, iterations and evidence for an approved change', async () => {
    const verdict = await open(approved.id)
    expect(screen.getByRole('heading', { level: 1, name: 'Retry failed artifact uploads' })).toBeTruthy()
    expect(screen.getByText(/Uploads to the evidence store fail/)).toBeTruthy()
    // The intent is laid out: paragraphs, a bullet list, code spans.
    const intent = document.querySelector('.intent')!
    expect(intent.querySelectorAll('p').length).toBe(2)
    expect(intent.querySelectorAll('ul li').length).toBe(2)
    expect(intent.querySelector('code')?.textContent).toBe('PLUMBIX_UPLOAD_RETRIES')
    expect(screen.getByText('no new dependencies')).toBeTruthy()
    expect(screen.getByText('3 of 3 met')).toBeTruthy()
    const ac1 = screen.getByText('A transient upload error is retried up to three times').closest('.criterion')!
    expect(ac1.textContent).toContain('TestUploadRetriesThrice covers three attempts with backoff')
    expect(ac1.textContent).toContain('ai-review')
    expect(within(ac1 as HTMLElement).getByRole('button', { name: 'evidence/ai-review/review.json' })).toBeTruthy()
    fireEvent.click(within(ac1 as HTMLElement).getByRole('button', { name: '+1 validator' }))
    expect(ac1.textContent).toContain('step test succeeded')
    expect(verdict.textContent).toContain('All 3 criteria met. No blockers.')
    expect(verdict.textContent).toContain('3/3 met · 0 blockers · 1 finding · 1 risk')
    expect(verdict.textContent).toContain('Merge squashes 9f3c1e2 onto main')
    expect(screen.getByText('Backoff ceiling is hard-coded at 8 s; consider a constant')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'internal/evidence/upload.go:71' })).toBeTruthy()
    expect(screen.getByText('2 test files were modified')).toBeTruthy()
    expect(screen.getByText('No human needed')).toBeTruthy()
    const iterations = screen.getAllByRole('button', { name: /Iteration \d/ })
    expect(iterations.map((b) => b.textContent)).toEqual([expect.stringContaining('Iteration 2'), expect.stringContaining('Iteration 1')])
    expect(iterations[0].getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByText('iteration 2 · run_8h2k4m1qz2bb · 9f3c1e2')).toBeTruthy()
    fireEvent.click(iterations[1])
    expect(screen.getByText('iteration 1 · run_3d9x7p2cq1aa · 4c2e8d1')).toBeTruthy()
    const steps = document.querySelectorAll('button.step')
    expect(steps).toHaveLength(5)
    expect(steps[1].textContent).toContain('test')
    expect(steps[1].textContent).toContain('Log')
    expect(screen.getByText('Updated 4 min ago')).toBeTruthy()
    expect(screen.getByText(/a81d7c0\.\.9f3c1e2/)).toBeTruthy()
  })

  it('merges with one click and shows the record', async () => {
    const verdict = await open(approved.id)
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
    await waitFor(() => expect(verdict.textContent).toContain('Merged into main.'))
    expect(screen.getByText('5c0e7a3b9d21')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Merge' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
  })

  it('turns a merge refusal into a next step', async () => {
    db.mergeResult = 'target_moved'
    await open(approved.id)
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Not merged: main moved')
    expect(notice.textContent).toContain('git fetch origin && git merge origin/main && git push')
    expect(within(notice).getByRole('button', { name: 'Copy command' })).toBeTruthy()
    fireEvent.click(within(notice).getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByRole('button', { name: 'Merge' })).toBeTruthy()

    db.mergeResult = 'push_rejected'
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
    const again = await screen.findByRole('status')
    expect(again.textContent).toContain('the remote changed during the merge')
    db.mergeResult = 'ok'
    fireEvent.click(within(again).getByRole('button', { name: 'Merge again' }))
    await waitFor(() => expect(screen.getByRole('complementary', { name: 'Verdict' }).textContent).toContain('Merged into main.'))
  })

  it('sends a denied push to the repository settings', async () => {
    db.mergeResult = 'push_denied'
    await open(approved.id)
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Not merged: the host refused the push')
    expect(notice.textContent).toContain('Set a token with push rights')
    fireEvent.click(within(notice).getByRole('button', { name: 'Open repository settings' }))
    expect(window.location.pathname).toBe('/repos')
  })

  it('lets a person approve with a reason, then shows the approval and the merge button', async () => {
    await open(needsHuman.id)
    expect(screen.queryByRole('button', { name: 'Merge' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    const form = screen.getByRole('form', { name: 'Approve this change' })
    const confirm = within(form).getByRole('button', { name: 'Confirm approval' })
    expect(confirm.hasAttribute('disabled')).toBe(true)
    fireEvent.change(within(form).getByLabelText('Reason'), { target: { value: 'pipeline edit read by hand' } })
    fireEvent.click(confirm)
    await waitFor(() => expect(screen.getByTestId('human-approval').textContent).toContain('Approved by a person · pipeline edit read by hand'))
    expect(screen.getByRole('button', { name: 'Merge' })).toBeTruthy()
    expect(screen.queryByRole('form', { name: 'Approve this change' })).toBeNull()
    const c = db.changes.find((x) => x.id === needsHuman.id)!
    expect(c.state).toBe('approved')
    expect(c.human_approval?.reason).toBe('pipeline edit read by hand')
  })

  it('Keep closes the approval field without approving', async () => {
    await open(needsHuman.id)
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    fireEvent.click(screen.getByRole('button', { name: 'Keep' }))
    expect(screen.queryByRole('form', { name: 'Approve this change' })).toBeNull()
    expect(db.changes.find((x) => x.id === needsHuman.id)!.state).toBe('needs_human')
  })

  it('closes only after an inline confirm', async () => {
    await open(fixRequested.id)
    expect(screen.queryByRole('button', { name: 'Merge' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.getByText('Close this change?')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Keep' }))
    expect(screen.queryByText('Close this change?')).toBeNull()
    expect(db.changes.find((c) => c.id === fixRequested.id)!.state).toBe('fix_requested')
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(screen.getByRole('complementary', { name: 'Verdict' }).textContent).toContain('Closed without merging.'))
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
  })

  it('offers Validate again for needs_human and goes live', async () => {
    const verdict = await open(needsHuman.id)
    expect(verdict.textContent).toContain('A person must look')
    expect(verdict.textContent).toContain('step build could not run: image golang:1.27 not found')
    expect(screen.getByText(/^error: step build could not run/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Validate again' }))
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Validating again')
    await waitFor(() => expect(screen.getByText('Live', { selector: '.change-header .meta' })).toBeTruthy())
  })

  it('shows a running change with its finished steps and updates from events', async () => {
    const verdict = await open(running.id)
    expect(verdict.textContent).toContain('Validating iteration 1')
    expect(verdict.textContent).toContain('Pipeline')
    expect(within(verdict).getByText('build')).toBeTruthy()
    // The step that runs now is listed with the running pill and an open log.
    const testRow = within(verdict).getByText('test').closest('button')!
    expect(testRow.textContent).toContain('Running')
    expect(testRow.hasAttribute('disabled')).toBe(false)
    expect(screen.getByText('1 pending')).toBeTruthy()
    expect(screen.getByText('Waiting for step test')).toBeTruthy()
    const run = db.runs[running.id][0]
    run.steps[1] = { name: 'test', validator: false, status: 'succeeded', exit_code: 0, duration_ms: 70000 }
    await streamOpen()
    emit({ type: 'step.finished', change_id: running.id, run_id: run.id, repo_id: running.repo_id, payload: { step: 'test', status: 'succeeded' } })
    await waitFor(() => expect(within(screen.getByRole('complementary', { name: 'Verdict' })).getByText('test').closest('button')!.textContent).toContain('1m 10s'))
  })

  it('goes back to the list with the back button and Escape', async () => {
    await open(approved.id)
    fireEvent.click(screen.getByRole('button', { name: 'Changes' }))
    expect(window.location.pathname).toBe('/')
    await screen.findByRole('list', { name: 'Changes' })
    fireEvent.click(screen.getAllByRole('listitem')[0])
    await screen.findByRole('complementary', { name: 'Verdict' })
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(window.location.pathname).toBe('/')
  })

  it('says so when the change does not exist', async () => {
    window.history.replaceState(null, '', '/changes/c_nope')
    render(<App />)
    await screen.findByText('No change c_nope')
  })
})

describe('Newer commits', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    db = install()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('tells the person the branch moved since the verdict and offers Validate again', async () => {
    const c = db.changes.find((x) => x.id === needsHuman.id)!
    c.source_head = '1e88b04fffffffffffffffffffffffffffffffff'
    const verdict = await open(needsHuman.id)
    const note = screen.getByRole('note')
    expect(note.textContent).toContain('Newer commits on chore/go-1.27')
    expect(note.textContent).toContain('1e88b04')
    expect(note.textContent).toContain('A push does not restart validation for a change that needs a person')
    fireEvent.click(within(note).getByRole('button', { name: 'Validate again' }))
    await waitFor(() => expect(verdict.textContent).toContain('Validating iteration'))
  })

  it('lets a person update a Plumbix branch from main and goes live', async () => {
    const c = db.changes.find((x) => x.id === needsHuman.id)!
    c.plumbix_branch = true
    const verdict = await open(needsHuman.id)
    fireEvent.click(screen.getByRole('button', { name: 'Update from main' }))
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Updating from main')
    await waitFor(() => expect(verdict.textContent).toContain('Validating iteration'))
    expect(screen.queryByRole('button', { name: 'Update from main' })).toBeNull()
  })

  it('offers Update from main when a merge is refused because main moved on a Plumbix branch', async () => {
    const c = db.changes.find((x) => x.id === approved.id)!
    c.plumbix_branch = true
    db.mergeResult = 'target_moved'
    await open(approved.id)
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
    const notice = await screen.findByRole('status')
    expect(notice.textContent).toContain('Not merged: main moved')
    expect(notice.textContent).not.toContain('git fetch')
    fireEvent.click(within(notice).getByRole('button', { name: 'Update from main' }))
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Updating from main'))
  })

  it('says when there is nothing to update or the merge conflicts', async () => {
    const c = db.changes.find((x) => x.id === needsHuman.id)!
    c.plumbix_branch = true
    db.refreshResult = 'already_current'
    await open(needsHuman.id)
    fireEvent.click(screen.getByRole('button', { name: 'Update from main' }))
    expect((await screen.findByRole('status')).textContent).toContain('Already up to date with main')
    fireEvent.click(within(screen.getByRole('status')).getByRole('button', { name: 'Dismiss' }))
    db.refreshResult = 'merge_conflict'
    fireEvent.click(screen.getByRole('button', { name: 'Update from main' }))
    const conflict = await screen.findByRole('status')
    expect(conflict.textContent).toContain('main conflicts with chore/go-1.27')
    expect(conflict.textContent).toContain('.plumbix/pipeline.yaml')
    expect(screen.getByRole('button', { name: 'Update from main' })).toBeTruthy()
  })
})

describe('Validator outcomes', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    db = install()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('shows what each validator decided next to how its step ran', async () => {
    const verdict = await open(fixRequested.id)
    const ai = within(verdict).getByText('ai-review').closest('.vstep')!
    expect(ai.textContent).toContain('Fix requested')
    const row = screen.getByText('ai-review', { selector: '.step .name' }).closest('.step')!
    expect(row.textContent).toContain('Succeeded')
    expect(row.textContent).toContain('Fix requested')
    const test = screen.getByText('test', { selector: '.step .name' }).closest('.step')!
    expect(test.textContent).not.toContain('Fix requested')
  })

  it('says No verdict for a validator step that produced none', async () => {
    const run = db.runs[fixRequested.id][0]
    run.result!.validators = [{ name: 'validate', outcome: 'fix', summary: 'two criteria unmet' }, { name: 'ai-review', error: 'validator step errored: image not found' }]
    run.steps = run.steps.map((s) => (s.name === 'ai-review' ? { ...s, status: 'errored', error: 'image not found' } : s))
    await open(fixRequested.id)
    const row = screen.getByText('ai-review', { selector: '.step .name' }).closest('.step')!
    expect(row.textContent).toContain('No verdict')
  })
})
