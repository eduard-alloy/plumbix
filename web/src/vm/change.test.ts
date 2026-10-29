import { describe, expect, it } from 'vitest'
import { changeView, refreshNotice, refusalNotice } from './change'
import { approved, approvedRuns, closed, fixRequested, fixRequestedRuns, merged, needsHuman, needsHumanRuns, NOW, open, repos, running, runningRuns } from '../test/fixtures'

const plumbix = repos[0]

describe('changeView for an approved change', () => {
  const v = changeView(approved, approvedRuns, plumbix, undefined, NOW)

  it('fills the header from the change and its latest run', () => {
    expect(v.header).toMatchObject({ repo: 'plumbix', host: 'GitHub', src: 'feat/retry-uploads', tgt: 'main', head: '9f3c1e2', base: 'a81d7c0', iter: '2 of 2', by: 'kai', updated: 'Updated 4 min ago' })
    expect(v.header.submitted).toMatch(/^\d\d:\d\d$/)
    expect(v.intent).toHaveLength(3)
    expect(v.intent[2]).toMatchObject({ kind: 'ul' })
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
    expect(v.verdict.steps.map((s) => s.name)).toEqual(['validate', 'ai-review', 'ai-grok'])
    expect(v.verdict.steps.map((s) => s.outcome)).toEqual(['approve', 'approve', 'reject'])
  })

  it('groups findings by severity with their links, and lists risks', () => {
    // The advisory blocker is listed and tagged, but never counted (WU-030).
    expect(v.findings.map((g) => [g.label, g.items.length])).toEqual([
      ['Blockers', 1],
      ['Minor', 1],
    ])
    expect(v.findings[0].items[0]).toMatchObject({ severity: 'blocker', validator: 'ai-grok', advisory: true })
    expect(v.findings[1].items[0]).toMatchObject({ severity: 'minor', message: 'Backoff ceiling is hard-coded at 8 s; consider a constant', validator: 'ai-review', advisory: false, criterion: '', loc: 'internal/evidence/upload.go:71', file: 'internal/evidence/upload.go', line: 71 })
    expect(v.verdict.counts).toContain('0 blockers')
    expect(v.verdict.steps.find((s) => s.name === 'ai-grok')).toMatchObject({ advisory: true, outcome: 'reject' })
    expect(v.risks).toEqual([{ area: 'tests', message: '2 test files were modified', validator: 'ai-review', advisory: false, humanRequired: false }])
    expect(v.judged).toBe(true)
  })

  it('lists iterations newest first and the selected run’s evidence', () => {
    expect(v.iterations).toHaveLength(2)
    expect(v.iterations[0]).toMatchObject({ n: 2, status: 'approved', headline: 'All criteria met', head: '9f3c1e2', dur: '4m 00s', ago: '4m', selected: true, index: 1 })
    expect(v.iterations[1]).toMatchObject({ n: 1, status: 'fix_requested', headline: 'AC-1 unmet: only one retry attempted', dur: '3m 00s', selected: false, index: 0 })
    expect(v.evidence.label).toBe('iteration 2 · run_8h2k4m1qz2bb · 9f3c1e2')
    expect(v.evidence.steps).toHaveLength(6)
    expect(v.evidence.steps[0]).toMatchObject({ name: 'build', status: 'succeeded', exit: 'exit 0', dur: '42s', hasLog: true, validator: false })
    expect(v.evidence.steps[3]).toMatchObject({ name: 'validate', validator: true })
    expect(v.evidence.runId).toBe('run_8h2k4m1qz2bb')
    expect(v.evidence.error).toBe('')
    const earlier = changeView(approved, approvedRuns, plumbix, 0, NOW)
    expect(earlier.evidence.label).toBe('iteration 1 · run_3d9x7p2cq1aa · 4c2e8d1')
    expect(earlier.evidence.steps[1]).toMatchObject({ name: 'test', status: 'failed', exit: 'exit 1' })
    expect(earlier.iterations[1].selected).toBe(true)
  })

  it('says when a failure was allowed, a step was retried or skipped by its paths', () => {
    const steps = approvedRuns[1].steps
    const runs = [approvedRuns[0], { ...approvedRuns[1], steps: [
      { ...steps[0], name: 'flaky', status: 'failed' as const, exit_code: 1, attempts: 3, allowed_failure: true, stage: 'checks' },
      { ...steps[0], name: 'api', status: 'skipped' as const, exit_code: 0, duration_ms: 0, reason: 'paths' as const },
      { ...steps[0], name: 'after', status: 'skipped' as const, exit_code: 0, duration_ms: 0, reason: 'needs' as const },
      { ...steps[0], name: 'retried', attempts: 2 },
      ...steps,
    ] }]
    const v = changeView(approved, runs, plumbix, undefined, NOW)
    const by = Object.fromEntries(v.evidence.steps.map((s) => [s.name, s]))
    expect(by.flaky).toMatchObject({ exit: 'exit 1 · allowed · attempt 3', stage: 'checks' })
    expect(by.api).toMatchObject({ exit: 'paths unchanged', hasLog: false })
    expect(by.after).toMatchObject({ exit: 'not run', hasLog: false })
    expect(by.retried.exit).toBe('exit 0 · attempt 2')
    expect(by.build.exit).toBe('exit 0')
  })

  it('offers Merge and Close', () => {
    expect(v.actions).toEqual({ primary: 'merge', close: true, approve: false, refresh: false })
    expect(v.diffRange).toBe('a81d7c0..9f3c1e2')
  })
})

describe('changeView for other states', () => {
  it('fix_requested: blockers in the sentence, no primary action', () => {
    const v = changeView(fixRequested, fixRequestedRuns, plumbix, undefined, NOW)
    expect(v.verdict.sentence).toBe('2 blockers. Sent back to mira; a new push validates again.')
    expect(v.verdict.steps.map((s) => [s.name, s.outcome])).toEqual([
      ['validate', 'fix'],
      ['ai-review', 'fix'],
    ])
    expect(v.evidence.steps.find((s) => s.name === 'ai-review')?.outcome).toBe('fix')
    expect(v.evidence.steps.find((s) => s.name === 'test')?.outcome).toBeUndefined()
    expect(v.findings.map((g) => [g.label, g.items.length])).toEqual([
      ['Blockers', 2],
      ['Minor', 1],
    ])
    expect(v.findings[0].items[0].criterion).toBe('AC-1')
    expect(v.risks[0].humanRequired).toBe(true)
    expect(v.actions).toEqual({ primary: null, close: true, approve: false, refresh: false })
    expect(v.criteria[2]).toMatchObject({ check: 'check: go test ./internal/hosts/github/...', status: 'met' })
  })

  it('running: live header, pending criteria, pipeline progress', () => {
    const v = changeView(running, runningRuns, plumbix, undefined, NOW)
    expect(v.header.updated).toBe('Live')
    expect(v.header.iter).toBe('1 of 1')
    expect(v.critCount).toBe('1 pending')
    expect(v.criteria[0]).toMatchObject({ status: 'pending', reason: 'Waiting for step test', by: '' })
    expect(v.verdict).toMatchObject({ sentence: 'Validating iteration 1. The verdict arrives when the pipeline finishes.', counts: '', stepsLabel: 'Pipeline' })
    expect(v.verdict.steps.map((s) => s.name)).toEqual(['build', 'test'])
    expect(v.verdict.steps[1]).toMatchObject({ status: 'running', exit: '', dur: '', hasLog: true })
    expect(v.iterations[0]).toMatchObject({ n: 1, status: 'running', headline: 'Running', dur: '1m 12s' })
    expect(v.judged).toBe(false)
    expect(v.actions).toEqual({ primary: null, close: true, approve: false, refresh: false })
  })

  it('needs_human: the error is the reason, criteria are not judged, Validate again is offered', () => {
    const v = changeView(needsHuman, needsHumanRuns, repos[1], undefined, NOW)
    expect(v.header.host).toBe('Azure DevOps')
    expect(v.verdict.sentence).toBe('A person must look: the pipeline could not produce evidence.')
    expect(v.verdict.reasons).toEqual(['step build could not run: image golang:1.27 not found'])
    expect(v.verdict.counts).toBe('')
    expect(v.criteria[0]).toMatchObject({ status: 'unknown', reason: 'Not judged: step build could not run: image golang:1.27 not found' })
    expect(v.evidence.steps[0].outcome).toBeUndefined()
    expect(v.critCount).toBe('0 of 1 met')
    expect(v.iterations[0]).toMatchObject({ status: 'errored', headline: 'step build could not run: image golang:1.27 not found' })
    expect(v.evidence.error).toBe('step build could not run: image golang:1.27 not found')
    expect(v.actions).toEqual({ primary: 'validate', close: true, approve: true, refresh: false })
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
    expect(m.actions).toEqual({ primary: null, close: false, approve: false, refresh: false })
    const c = changeView(closed, fixRequestedRuns, plumbix, undefined, NOW)
    expect(c.verdict.sentence).toBe('Closed without merging.')
    expect(c.actions).toEqual({ primary: null, close: false, approve: false, refresh: false })
  })
})

describe('newer commits on the branch', () => {
  it('is reported when the branch head differs from the judged head and the change is not running', () => {
    const judged = changeView(approved, approvedRuns, plumbix, undefined, NOW)
    expect(judged.newerCommits).toBeNull()
    const moved = changeView({ ...approved, source_head: '1e88b04fffffffffffffffffffffffffffffffff' }, approvedRuns, plumbix, undefined, NOW)
    expect(moved.newerCommits).toBe('1e88b04')
    const same = changeView({ ...approved, source_head: approvedRuns[1].head }, approvedRuns, plumbix, undefined, NOW)
    expect(same.newerCommits).toBeNull()
    const live = changeView({ ...running, source_head: 'ffffffff' }, runningRuns, plumbix, undefined, NOW)
    expect(live.newerCommits).toBeNull()
    const sticky = changeView({ ...needsHuman, source_head: 'abcdef0123' }, needsHumanRuns, repos[1], undefined, NOW)
    expect(sticky.newerCommits).toBe('abcdef0')
    const done = changeView({ ...merged, source_head: 'abcdef0123' }, approvedRuns, plumbix, undefined, NOW)
    expect(done.newerCommits).toBeNull()
  })
})

describe('human approval', () => {
  it('offers Approve on needs_human and escalated changes and shows a standing approval', () => {
    const nh = changeView(needsHuman, needsHumanRuns, repos[1], undefined, NOW)
    expect(nh.actions.approve).toBe(true)
    expect(nh.humanApproval).toBeNull()
    expect(changeView(approved, approvedRuns, plumbix, undefined, NOW).actions.approve).toBe(false)
    expect(changeView(fixRequested, fixRequestedRuns, repos[1], undefined, NOW).actions.approve).toBe(false)
    const byPerson = changeView({ ...needsHuman, state: 'approved', human_approval: { reason: 'pipeline edit read by hand', head: 'abc', at: new Date(NOW.getTime() - 5 * 60000).toISOString() } }, needsHumanRuns, repos[1], undefined, NOW)
    // An approval recorded before WU-060 names nobody; a named one says who, and through GitHub when a review did it.
    expect(byPerson.humanApproval).toEqual({ who: 'Approved by a person', reason: 'pipeline edit read by hand', ago: '5m' })
    expect(byPerson.actions.primary).toBe('merge')
    const at = new Date(NOW.getTime() - 5 * 60000).toISOString()
    expect(changeView({ ...needsHuman, state: 'approved', human_approval: { reason: 'r', head: 'abc', at, by: 'eduard@mac', source: 'plumbix' } }, needsHumanRuns, repos[1], undefined, NOW).humanApproval?.who).toBe('Approved by eduard@mac')
    expect(changeView({ ...needsHuman, state: 'approved', human_approval: { reason: 'r', head: 'abc', at, by: 'lead', source: 'github_review' } }, needsHumanRuns, repos[1], undefined, NOW).humanApproval?.who).toBe('Approved on GitHub by lead')
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
    const pd = refusalNotice('push_denied', ctx)
    expect(pd).toMatchObject({ title: 'Not merged: the host refused the push', action: 'settings', actionLabel: 'Open repository settings' })
    expect(pd.body).toContain('token')
    expect(refusalNotice('invalid_state', ctx)).toMatchObject({ title: 'Not merged: this change is no longer approved', action: 'iterations' })
    expect(refusalNotice('validating', ctx)).toMatchObject({ title: 'Validating again', action: null })
    expect(refusalNotice('something_else', ctx)).toMatchObject({ title: 'Not merged', action: null })
  })
})

describe('changeView for a branch Plumbix created', () => {
  it('offers Update from main only when the change is neither final nor running', () => {
    const own = { ...needsHuman, plumbix_branch: true }
    expect(changeView(own, needsHumanRuns, plumbix, undefined, NOW).actions.refresh).toBe(true)
    expect(changeView(needsHuman, needsHumanRuns, plumbix, undefined, NOW).actions.refresh).toBe(false)
    expect(changeView({ ...own, state: 'approved' }, needsHumanRuns, plumbix, undefined, NOW).actions.refresh).toBe(true)
    expect(changeView({ ...own, state: 'running' }, runningRuns, plumbix, undefined, NOW).actions.refresh).toBe(false)
    expect(changeView({ ...own, state: 'merged' }, needsHumanRuns, plumbix, undefined, NOW).actions.refresh).toBe(false)
    expect(changeView({ ...own, state: 'closed' }, needsHumanRuns, plumbix, undefined, NOW).actions.refresh).toBe(false)
  })
})

describe('refusalNotice for a branch Plumbix created', () => {
  it('offers Update from main instead of a command when main moved', () => {
    const tm = refusalNotice('target_moved', { src: 'pipeline/stage-checks', tgt: 'main', head: '322bdb0', base: '5f64ef8', plumbixBranch: true })
    expect(tm).toMatchObject({ title: 'Not merged: main moved', cmd: '', action: 'refresh', actionLabel: 'Update from main' })
    expect(tm.body).toContain('Plumbix can bring main into pipeline/stage-checks')
  })
})

describe('refreshNotice', () => {
  const ctx = { src: 'pipeline/stage-checks', tgt: 'main', head: '322bdb0', base: '5f64ef8', plumbixBranch: true }
  it('explains what Update from main did or why it did nothing', () => {
    expect(refreshNotice('refreshing', ctx)).toMatchObject({ title: 'Updating from main', action: null })
    expect(refreshNotice('refreshing', ctx).body).toContain('merged main into pipeline/stage-checks')
    expect(refreshNotice('already_current', ctx)).toMatchObject({ title: 'Already up to date with main', action: null })
    const conflict = refreshNotice('merge_conflict', ctx, 'merge conflict: .plumbix/pipeline.yaml')
    expect(conflict).toMatchObject({ title: 'Not updated: main conflicts with pipeline/stage-checks', action: null })
    expect(conflict.body).toContain('.plumbix/pipeline.yaml')
    expect(refreshNotice('not_plumbix_branch', ctx, 'x')).toMatchObject({ title: 'Not updated: this branch is not Plumbix’s', action: null })
    expect(refreshNotice('push_denied', ctx)).toMatchObject({ title: 'Not updated: the host refused the push', action: 'settings' })
    expect(refreshNotice('invalid_state', ctx)).toMatchObject({ title: 'Not updated: the change moved on', action: null })
    expect(refreshNotice('something_else', ctx)).toMatchObject({ title: 'Not updated', action: null })
    const failed = refreshNotice('failed', ctx)
    expect(failed).toMatchObject({ title: 'Not updated: Plumbix did not answer', action: null })
    expect(failed.body).toContain('Nothing is known about the branch')
  })
})

describe('criterion notes (WU-062)', () => {
  it('says why a pull request item was not bound, beside the check label', () => {
    const c = { ...needsHuman, acceptance_criteria: [{ id: 'AC-1', text: 'Typo in the step', note: 'names step `bulid`, which the pipeline has not' }] }
    const v = changeView(c, needsHumanRuns, repos[1], undefined, NOW)
    expect(v.criteria[0].check).toBe('no check · names step `bulid`, which the pipeline has not')
  })
})

describe('merge record (WU-063)', () => {
  it('says who merged, and when GitHub merged on a requester\'s behalf', () => {
    const at = new Date(NOW.getTime() - 60000).toISOString()
    const base = { ...approved, state: 'merged' as const }
    expect(changeView({ ...base, merge: { commit: 'abc'.padEnd(40, '0'), previous: 'def'.padEnd(40, '0'), strategy: 'squash', run_id: 'r', merged_at: at } }, approvedRuns, plumbix, undefined, NOW).verdict.record?.mergedBy).toBe('Plumbix')
    expect(changeView({ ...base, merge: { commit: 'abc'.padEnd(40, '0'), previous: 'def'.padEnd(40, '0'), strategy: 'github', run_id: 'r', merged_by: 'lead', merged_at: at } }, approvedRuns, plumbix, undefined, NOW).verdict.record?.mergedBy).toBe('GitHub, by lead')
    expect(changeView({ ...base, merge: { commit: 'abc'.padEnd(40, '0'), previous: 'def'.padEnd(40, '0'), strategy: 'github', run_id: 'r', merged_by: 'eduard', auto_merge: true, merged_at: at } }, approvedRuns, plumbix, undefined, NOW).verdict.record?.mergedBy).toBe('GitHub auto-merge, requested by eduard')
  })
})
