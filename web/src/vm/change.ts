// The change screen's view model: everything the page shows, computed from
// the change, its runs and its repo. Pure, so it is tested without the DOM.
import type { Change, ChangeState, CriterionStatus, Repo, Run, Severity, Step, ValidatorDecision, ValidatorOutcome } from '../model'
import { ago, agoLong, clock, dateTime, duration, elapsedSeconds, minutesBetween, plural, shortSha } from '../format'
import { latestJudged, latestRun, repoName } from './changes'

const HOST: Record<Repo['host'], string> = { github: 'GitHub', azuredevops: 'Azure DevOps', git: 'git' }

export type CriterionRowStatus = CriterionStatus | 'pending'

export interface AssessmentVM {
  validator: string
  status: CriterionStatus
  reason: string
  evidence: string[]
}

export interface CriterionVM {
  id: string
  text: string
  check: string
  status: CriterionRowStatus
  reason: string
  by: string
  evidence: string[]
  more: AssessmentVM[]
}

export interface StepVM {
  name: string
  validator: boolean
  status: Step['status'] | 'queued'
  exit: string
  dur: string
  hasLog: boolean
  /** What a validator decided: approve, fix, reject, or 'none' when it produced no verdict. Absent for normal steps. */
  outcome?: ValidatorDecision | 'none'
  summary?: string
}

export interface FindingVM {
  severity: Severity
  message: string
  validator: string
  criterion: string
  loc: string
  file: string
  line: number
}

export interface FindingGroupVM {
  label: string
  severity: Severity
  items: FindingVM[]
}

export interface RiskVM {
  area: string
  message: string
  validator: string
  humanRequired: boolean
}

export interface IterationVM {
  n: number
  index: number
  status: ChangeState | 'running' | 'errored' | 'cancelled'
  headline: string
  head: string
  dur: string
  ago: string
  selected: boolean
}

export interface ChangeView {
  header: {
    repo: string
    host: string
    src: string
    tgt: string
    head: string
    base: string
    iter: string
    by: string
    submitted: string
    updated: string
  }
  intent: string[]
  constraints: string[]
  critCount: string
  criteria: CriterionVM[]
  judged: boolean
  verdict: {
    runId: string
    state: ChangeState
    iterLabel: string
    sentence: string
    counts: string
    reasons: string[]
    stepsLabel: string
    steps: StepVM[]
    mergeNote: string
    record: { commit: string; previous: string; strategy: string; mergedAt: string } | undefined
  }
  findings: FindingGroupVM[]
  risks: RiskVM[]
  iterations: IterationVM[]
  evidence: { label: string; runId: string; error: string; steps: StepVM[] }
  actions: { primary: 'merge' | 'validate' | null; close: boolean }
  diffRange: string
  /** Short sha of commits on the branch newer than the judged head, or null. */
  newerCommits: string | null
  /** True when only a person moves the change on: a push starts nothing. */
  needsPerson: boolean
}

function checkLabel(c: Change['acceptance_criteria'][number]): string {
  if (c.check?.steps?.length) return `check: step ${c.check.steps.join(', ')}`
  if (c.check?.command) return `check: ${c.check.command}`
  return 'no check'
}

function stepVM(s: Step, outcomes?: ValidatorOutcome[]): StepVM {
  const vm: StepVM = { name: s.name, validator: s.validator, status: s.status, exit: `exit ${s.exit_code}`, dur: duration(s.duration_ms / 1000), hasLog: s.status !== 'skipped' }
  if (s.validator && outcomes) {
    const o = outcomes.find((x) => x.name === s.name)
    if (o) {
      vm.outcome = o.outcome ?? 'none'
      vm.summary = o.summary ?? o.error
    }
  }
  return vm
}

function runDuration(r: Run, now: Date): string {
  if (r.status === 'running') return duration(elapsedSeconds(r.started_at, now))
  if (r.finished_at) return duration(Math.max(0, (Date.parse(r.finished_at) - Date.parse(r.started_at)) / 1000))
  return duration(r.steps.reduce((a, s) => a + s.duration_ms, 0) / 1000)
}

function headline(r: Run): string {
  if (r.status === 'running') return 'Running'
  if (r.status === 'cancelled') return 'Cancelled'
  if (r.error) return r.error
  const res = r.result
  if (!res) return r.status === 'errored' ? 'Errored' : 'Finished'
  const blocker = res.findings.find((f) => f.severity === 'blocker')
  switch (res.state) {
    case 'approved':
      return 'All criteria met'
    case 'fix_requested':
      return blocker?.message ?? 'Fixes requested'
    default:
      return res.reasons[0] ?? blocker?.message ?? res.state.replace('_', ' ')
  }
}

export function changeView(c: Change, runs: Run[], repo: Repo | undefined, selected: number | undefined, now: Date): ChangeView {
  const nR = runs.length
  const cur = latestRun(runs)
  const judgedRun = latestJudged(runs)
  const result = judgedRun?.result
  const sel = Math.min(selected ?? nR - 1, nR - 1)
  const run = sel >= 0 ? runs[sel] : undefined
  const judged = !['running', 'open'].includes(c.state)
  const withHead = [...runs].reverse().find((r) => r.head)

  const criteria: CriterionVM[] = c.acceptance_criteria.map((ac) => {
    const combined = result?.criteria.find((x) => x.id === ac.id)
    const check = checkLabel(ac)
    if (!combined) {
      let status: CriterionRowStatus = 'pending'
      let reason = check === 'no check' ? 'Judged when the pipeline finishes' : `Waiting for ${check.replace('check: ', '')}`
      if (judged) {
        status = 'unknown'
        reason = `Not judged: ${cur?.error ?? result?.reasons[0] ?? 'the pipeline produced no verdict'}`
      }
      return { id: ac.id, text: ac.text, check, status, reason, by: '', evidence: [], more: [] }
    }
    const [head, ...rest] = combined.assessments
    return {
      id: ac.id,
      text: ac.text,
      check,
      status: combined.status,
      reason: head?.reason ?? 'Not judged',
      by: head?.validator ?? '',
      evidence: head?.evidence ?? [],
      more: rest.map((a) => ({ validator: a.validator, status: a.status, reason: a.reason, evidence: a.evidence ?? [] })),
    }
  })
  const met = criteria.filter((x) => x.status === 'met').length
  const tot = criteria.length
  const findingsAll = result?.findings ?? []
  const blockers = findingsAll.filter((f) => f.severity === 'blocker').length
  const risksAll = result?.risks ?? []

  const sentences: Record<ChangeState, string> = {
    approved: `All ${tot} criteria met. No blockers.`,
    fix_requested: `${plural(blockers, 'blocker')}. Sent back to ${c.submitted_by || 'the author'}; a new push validates again.`,
    running: `Validating iteration ${cur?.iteration ?? 1}. The verdict arrives when the pipeline finishes.`,
    needs_human: 'A person must look: the pipeline could not produce evidence.',
    escalated: 'Still not approved after three fix rounds. Push a fix yourself, or close the change.',
    rejected: 'Wrong at the root. Fixing details will not get this approved.',
    merged: `Merged into ${c.target_branch}.`,
    open: nR ? 'Waiting to validate again.' : 'Waiting to validate.',
    closed: 'Closed without merging.',
  }
  const showPipeline = c.state === 'running'
  const verdictSteps = cur ? (showPipeline ? cur.steps : cur.steps.filter((s) => s.validator)) : []
  const strategy = repo?.merge_strategy ?? 'squash'
  const mergeVerb = { squash: 'Merge squashes ', merge: 'Merge creates a merge commit for ', rebase: 'Merge rebases ' }[strategy]
  const reasons = result?.reasons.length ? result.reasons : cur?.error ? [cur.error] : []

  const groups: FindingGroupVM[] = (['blocker', 'major', 'minor'] as Severity[])
    .map((sev) => ({
      severity: sev,
      label: { blocker: 'Blockers', major: 'Major', minor: 'Minor' }[sev],
      items: findingsAll
        .filter((f) => f.severity === sev)
        .map((f) => ({ severity: sev, message: f.message, validator: f.validator, criterion: f.criterion ?? '', loc: f.file ? `${f.file}:${f.line ?? ''}` : '', file: f.file ?? '', line: f.line ?? 0 })),
    }))
    .filter((g) => g.items.length)

  return {
    header: {
      repo: repoName(repo, c.repo_id),
      host: repo ? HOST[repo.host] : '',
      src: c.source_branch,
      tgt: c.target_branch,
      head: withHead ? shortSha(withHead.head) : '—',
      base: withHead ? shortSha(withHead.base) : '—',
      iter: nR ? `${nR} of ${nR}` : '—',
      by: c.submitted_by ?? '',
      submitted: clock(c.created_at),
      updated: c.state === 'running' ? 'Live' : `Updated ${agoLong(minutesBetween(c.updated_at, now))}`,
    },
    intent: c.intent.split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean),
    constraints: c.constraints ?? [],
    critCount: judged ? `${met} of ${tot} met` : `${tot} pending`,
    criteria,
    judged,
    verdict: {
      runId: cur?.id ?? 'no run yet',
      state: c.state,
      iterLabel: cur?.iteration ? `iteration ${cur.iteration}` : '',
      sentence: sentences[c.state],
      counts: judged && result && c.state !== 'needs_human' ? `${met}/${tot} met · ${plural(blockers, 'blocker')} · ${plural(findingsAll.length, 'finding')} · ${plural(risksAll.length, 'risk')}` : '',
      reasons: ['rejected', 'needs_human', 'escalated'].includes(c.state) ? reasons : [],
      stepsLabel: showPipeline ? 'Pipeline' : 'Validators',
      steps: verdictSteps.map((s) => stepVM(s, cur?.result?.validators)),
      mergeNote: c.state === 'approved' && cur ? `${mergeVerb}${shortSha(cur.head)} onto ${c.target_branch} (${strategy}, set per repository).` : '',
      record: c.state === 'merged' && c.merge ? { commit: c.merge.commit.slice(0, 12), previous: c.merge.previous.slice(0, 12), strategy: c.merge.strategy, mergedAt: dateTime(c.merge.merged_at) } : undefined,
    },
    findings: groups,
    risks: risksAll.map((r) => ({ area: r.area, message: r.message, validator: r.validator, humanRequired: r.human_required })),
    iterations: [...runs]
      .map((r, i) => ({ r, i }))
      .reverse()
      .map(({ r, i }) => ({
        n: r.iteration ?? i + 1,
        index: i,
        status: r.status === 'running' ? 'running' : r.status === 'errored' ? 'errored' : r.status === 'cancelled' ? 'cancelled' : (r.outcome ?? 'open'),
        headline: headline(r),
        head: shortSha(r.head) || '—',
        dur: runDuration(r, now),
        ago: ago(minutesBetween(r.finished_at ?? r.started_at, now)),
        selected: i === sel,
      })),
    evidence: {
      label: run ? `iteration ${run.iteration ?? sel + 1} · ${run.id} · ${shortSha(run.head) || '—'}` : 'none yet',
      runId: run?.id ?? '',
      error: run?.error ?? '',
      steps: run ? run.steps.map((s) => stepVM(s, run.result?.validators)) : [],
    },
    actions: {
      primary: c.state === 'approved' ? 'merge' : c.state === 'needs_human' || c.state === 'escalated' ? 'validate' : null,
      close: !['merged', 'closed'].includes(c.state),
    },
    diffRange: withHead ? `${shortSha(withHead.base)}..${shortSha(withHead.head)}` : '',
    newerCommits: c.source_head && cur?.head && c.source_head !== cur.head && !['running', 'open', 'merged', 'closed'].includes(c.state) ? shortSha(c.source_head) : null,
    needsPerson: ['needs_human', 'escalated', 'rejected'].includes(c.state),
  }
}

export interface NoticeVM {
  title: string
  body: string
  cmd: string
  action: 'validate' | 'copy' | 'merge' | 'iterations' | null
  actionLabel: string
}

/** refusalNotice turns a merge refusal code (or 'validating') into the
 * design's next-step notice. */
export function refusalNotice(code: string, ctx: { src: string; tgt: string; head: string; base: string }): NoticeVM {
  switch (code) {
    case 'source_moved':
      return { title: `Not merged: new commits on ${ctx.src}`, body: `The verdict is for ${ctx.head}. The branch has moved since. Validate again; merge becomes available if the new head is approved.`, cmd: '', action: 'validate', actionLabel: 'Validate again' }
    case 'target_moved':
      return {
        title: `Not merged: ${ctx.tgt} moved`,
        body: `${ctx.tgt} moved since the verdict, which was for base ${ctx.base}. Bring ${ctx.tgt} into the branch and push. Plumbix validates the new head.`,
        cmd: `git fetch origin && git merge origin/${ctx.tgt} && git push`,
        action: 'copy',
        actionLabel: 'Copy command',
      }
    case 'push_rejected':
      return { title: 'Not merged: the remote changed during the merge', body: 'Nothing was pushed and the verdict still stands. Try the merge again.', cmd: '', action: 'merge', actionLabel: 'Merge again' }
    case 'invalid_state':
      return { title: 'Not merged: this change is no longer approved', body: 'A new push started another validation. Merge becomes available again if it is approved.', cmd: '', action: 'iterations', actionLabel: 'Show iterations' }
    case 'validating':
      return { title: 'Validating again', body: `Plumbix is validating ${ctx.src} again. This page updates as steps finish.`, cmd: '', action: null, actionLabel: '' }
    default:
      return { title: 'Not merged', body: 'The server refused the merge. Reload the change and try again.', cmd: '', action: null, actionLabel: '' }
  }
}
