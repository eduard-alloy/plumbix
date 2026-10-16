import { describe, expect, it } from 'vitest'
import { changeView, refusalNotice } from './change'
import { approved, approvedRuns, closed, fixRequested, fixRequestedRuns, merged, needsHuman, needsHumanRuns, NOW, open, repos, running, runningRuns } from '../test/fixtures'

const plumbix = repos[0]

describe('changeView for an approved change', () => {
  const v = changeView(approved, approvedRuns, plumbix, undefined, NOW)

  it('fills the header from the change and its latest run', () => {
    expect(v.header).toMatchObject({ repo: 'plumbix', host: 'GitHub', src: 'feat/retry-uploads', tgt: 'main', head: '9f3c1e2', base: 'a81d7c0', iter: '2 of 2', by: 'kai', updated: 'Updated 4 min ago' })
    expect(v.header.submitted).toMatch(/^\d\d:\d\d$/)
    expect(v.intent).toHaveLength(2)
    expect(v.constraints).toEqual(['no new dependencies'])
  })

  it('shows each criterion with its combined status, the leading assessment and the rest behind a toggle', () => {
    expect(v.critCount).toBe('3 of 3 met')
    const [ac1, , ac3] = v.criteria
    expect(ac1).toMatchObject({ id: 'AC-1', status: 'met', check: 'check: step test', reason: 'TestUploadRetriesThrice covers three attempts with backoff', by: 'ai-review' })
    expect(ac1.evidence).toEqual(['evidence/ai-review/review.json'])
    expect(ac1.more).toHaveLength(1)
    expect(ac1.more[0]).toMatchObject({ validator: 'validate', status: 'met', reason: 'step test succeeded', evidence: ['evidence/test/log.txt'] })
    expect(ac3.check).toBe('no check')
  })

  it('writes the verdict panel', () => {
    expect(v.verdict).toMatchObject({
      runId: 'run_8h2k4m1qz2bb',
      state: 'approved',
      iterLabel: 'iteration 2',
      sentence: 'All 3 criteria met. No blockers.',
      counts: '3/3 met · 0 blockers · 1 finding · 1 risk',
      stepsLabel: 'Validators',
      mergeNote: 'Merge squashes 9f3c1e2 onto main (squash, set per repository).',
      reasons: [],
      record: undefined,
    })
    expect(v.verdict.steps.map((s) => s.name)).toEqual(['validate', 'ai-review'])
  })

  it('groups findings by severity with their links, and lists risks', () => {
    expect(v.findings.map((g) => [g.label, g.items.length])).toEqual([['Minor', 1]])
    expect(v.findings[0].items[0]).toMatchObject({ severity: 'minor', message: 'Backoff ceiling is hard-coded at 8 s; consider a constant', validator: 'ai-review', criterion: '', loc: 'internal/evidence/upload.go:71', file: 'internal/evidence/upload.go', line: 71 })
    expect(v.risks).toEqual([{ area: 'tests', message: '2 test files were modified', validator: 'ai-review', humanRequired: false }])
    expect(v.judged).toBe(true)
  })

  it('lists iterations newest first and the selected run’s evidence', () => {
    expect(v.iterations).toHaveLength(2)
    expect(v.iterations[0]).toMatchObject({ n: 2, status: 'approved', headline: 'All criteria met', head: '9f3c1e2', dur: '4m 00s', ago: '4m', selected: true, index: 1 })
    expect(v.iterations[1]).toMatchObject({ n: 1, status: 'fix_requested', headline: 'AC-1 unmet: only one retry attempted', dur: '3m 00s', selected: false, index: 0 })
    expect(v.evidence.label).toBe('iteration 2 · run_8h2k4m1qz2bb · 9f3c1e2')
    expect(v.evidence.steps).toHaveLength(5)
    expect(v.evidence.steps[0]).toMatchObject({ name: 'build', status: 'succeeded', exit: 'exit 0', dur: '42s', hasLog: true, validator: false })
    expect(v.evidence.steps[3]).toMatchObject({ name: 'validate', validator: true })
    expect(v.evidence.runId).toBe('run_8h2k4m1qz2bb')
    expect(v.evidence.error).toBe('')
    const earlier = changeView(approved, approvedRuns, plumbix, 0, NOW)
    expect(earlier.evidence.label).toBe('iteration 1 · run_3d9x7p2cq1aa · 4c2e8d1')
    expect(earlier.evidence.steps[1]).toMatchObject({ name: 'test', status: 'failed', exit: 'exit 1' })
    expect(earlier.iterations[1].selected).toBe(true)
  })

  it('offers Merge and Close', () => {
    expect(v.actions).toEqual({ primary: 'merge', close: true })
    expect(v.diffRange).toBe('a81d7c0..9f3c1e2')
  })
})

describe('changeView for other states', () => {
  it('fix_requested: blockers in the sentence, no primary action', () => {
    const v = changeView(fixRequested, fixRequestedRuns, plumbix, undefined, NOW)
    expect(v.verdict.sentence).toBe('2 blockers. Sent back to mira; a new push validates again.')
    expect(v.findings.map((g) => [g.label, g.items.length])).toEqual([
      ['Blockers', 2],
      ['Minor', 1],
    ])
    expect(v.findings[0].items[0].criterion).toBe('AC-1')
    expect(v.risks[0].humanRequired).toBe(true)
    expect(v.actions).toEqual({ primary: null, close: true })
    expect(v.criteria[2]).toMatchObject({ check: 'check: go test ./internal/hosts/github/...', status: 'met' })
  })

  it('running: live header, pending criteria, pipeline progress', () => {
    const v = changeView(running, runningRuns, plumbix, undefined, NOW)
    expect(v.header.updated).toBe('Live')
    expect(v.header.iter).toBe('1 of 1')
    expect(v.critCount).toBe('1 pending')
    expect(v.criteria[0]).toMatchObject({ status: 'pending', reason: 'Waiting for step test', by: '' })
    expect(v.verdict).toMatchObject({ sentence: 'Validating iteration 1. The verdict arrives when the pipeline finishes.', counts: '', stepsLabel: 'Pipeline' })
    expect(v.verdict.steps.map((s) => s.name)).toEqual(['build'])
    expect(v.iterations[0]).toMatchObject({ n: 1, status: 'running', headline: 'Running', dur: '1m 12s' })
    expect(v.judged).toBe(false)
    expect(v.actions).toEqual({ primary: null, close: true })
  })

  it('needs_human: the error is the reason, criteria are not judged, Validate again is offered', () => {
    const v = changeView(needsHuman, needsHumanRuns, repos[1], undefined, NOW)
    expect(v.header.host).toBe('Azure DevOps')
    expect(v.verdict.sentence).toBe('A person must look: the pipeline could not produce evidence.')
    expect(v.verdict.reasons).toEqual(['step build could not run: image golang:1.27 not found'])
    expect(v.verdict.counts).toBe('')
    expect(v.criteria[0]).toMatchObject({ status: 'unknown', reason: 'Not judged: step build could not run: image golang:1.27 not found' })
    expect(v.critCount).toBe('0 of 1 met')
    expect(v.iterations[0]).toMatchObject({ status: 'errored', headline: 'step build could not run: image golang:1.27 not found' })
    expect(v.evidence.error).toBe('step build could not run: image golang:1.27 not found')
    expect(v.actions).toEqual({ primary: 'validate', close: true })
  })

  it('open without runs: placeholders everywhere', () => {
    const v = changeView(open, [], repos[1], undefined, NOW)
    expect(v.header).toMatchObject({ head: '—', base: '—', iter: '—' })
    expect(v.verdict).toMatchObject({ runId: 'no run yet', sentence: 'Waiting to validate.', iterLabel: '' })
    expect(v.iterations).toEqual([])
    expect(v.evidence.label).toBe('none yet')
    expect(v.criteria[0]).toMatchObject({ status: 'pending', reason: 'Judged when the pipeline finishes' })
    expect(v.diffRange).toBe('')
  })

  it('merged and closed: no actions, merge record when merged', () => {
    const m = changeView(merged, approvedRuns, plumbix, undefined, NOW)
    expect(m.verdict.sentence).toBe('Merged into main.')
    expect(m.verdict.record).toMatchObject({ commit: '5c0e7a3b9d21', previous: 'a81d7c0bbbbb', strategy: 'squash' })
    expect(m.actions).toEqual({ primary: null, close: false })
    const c = changeView(closed, fixRequestedRuns, plumbix, undefined, NOW)
    expect(c.verdict.sentence).toBe('Closed without merging.')
    expect(c.actions).toEqual({ primary: null, close: false })
  })
})

describe('refusalNotice', () => {
  const ctx = { src: 'feat/retry-uploads', tgt: 'main', head: '9f3c1e2', base: 'a81d7c0' }
  it('turns each merge refusal into a next step', () => {
    expect(refusalNotice('source_moved', ctx)).toMatchObject({ title: 'Not merged: new commits on feat/retry-uploads', action: 'validate' })
    expect(refusalNotice('source_moved', ctx).body).toContain('The verdict is for 9f3c1e2')
    const tm = refusalNotice('target_moved', ctx)
    expect(tm).toMatchObject({ title: 'Not merged: main moved', cmd: 'git fetch origin && git merge origin/main && git push', action: 'copy' })
    expect(tm.body).toContain('base a81d7c0')
    expect(refusalNotice('push_rejected', ctx)).toMatchObject({ title: 'Not merged: the remote changed during the merge', action: 'merge' })
    expect(refusalNotice('invalid_state', ctx)).toMatchObject({ title: 'Not merged: this change is no longer approved', action: 'iterations' })
    expect(refusalNotice('validating', ctx)).toMatchObject({ title: 'Validating again', action: null })
    expect(refusalNotice('something_else', ctx)).toMatchObject({ title: 'Not merged', action: null })
  })
})
