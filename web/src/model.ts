// The API's shapes, as contracts/openapi.yaml defines them.

export type ChangeState =
  | 'open'
  | 'running'
  | 'approved'
  | 'fix_requested'
  | 'rejected'
  | 'needs_human'
  | 'escalated'
  | 'merged'
  | 'closed'

export const CHANGE_STATES: ChangeState[] = [
  'open',
  'running',
  'approved',
  'fix_requested',
  'rejected',
  'needs_human',
  'escalated',
  'merged',
  'closed',
]

export const NEEDS_YOU: ChangeState[] = ['approved', 'needs_human', 'escalated']

export type RunStatus = 'running' | 'finished' | 'errored' | 'cancelled'
export type StepStatus = 'succeeded' | 'failed' | 'timed_out' | 'errored' | 'skipped' | 'cancelled'
export type CriterionStatus = 'met' | 'unmet' | 'unknown'
export type Severity = 'blocker' | 'major' | 'minor'

export interface Repo {
  id: string
  host: 'github' | 'azuredevops' | 'git'
  url: string
  protected_branches: string[]
  merge_strategy: 'squash' | 'merge' | 'rebase'
  created_at: string
}

export interface Criterion {
  id: string
  text: string
  check?: { steps?: string[]; command?: string }
}

export interface Merge {
  commit: string
  previous: string
  strategy: string
  run_id: string
  merged_at: string
}

export interface Change {
  id: string
  repo_id: string
  source_branch: string
  target_branch: string
  title: string
  intent: string
  acceptance_criteria: Criterion[]
  constraints: string[]
  submitted_by?: string
  state: ChangeState
  merge?: Merge
  created_at: string
  updated_at: string
}

export interface Step {
  name: string
  validator: boolean
  status: StepStatus
  exit_code: number
  duration_ms: number
  error?: string
}

export interface Assessment {
  validator: string
  status: CriterionStatus
  reason: string
  evidence?: string[]
}

export interface CombinedCriterion {
  id: string
  text: string
  status: CriterionStatus
  assessments: Assessment[]
}

export interface Finding {
  validator: string
  severity: Severity
  message: string
  criterion?: string
  file?: string
  line?: number
}

export interface Risk {
  validator: string
  area: string
  message: string
  human_required: boolean
}

export type ValidatorDecision = 'approve' | 'fix' | 'reject'

export interface ValidatorOutcome {
  name: string
  outcome?: ValidatorDecision
  summary?: string
  error?: string
}

export interface Result {
  state: ChangeState
  criteria: CombinedCriterion[]
  findings: Finding[]
  risks: Risk[]
  reasons: string[]
  validators?: ValidatorOutcome[]
}

export interface Run {
  id: string
  repo_id: string
  change_id?: string
  branch: string
  iteration?: number
  head: string
  base: string
  status: RunStatus
  outcome?: ChangeState
  result?: Result
  error?: string
  steps: Step[]
  started_at: string
  finished_at?: string
}

export interface Alert {
  id: string
  kind: 'bypass_commit' | 'token_expiry'
  key: string
  repo_id?: string
  message: string
  details?: Record<string, unknown>
  created_at: string
  resolved_at?: string
}

export interface DiffLine {
  type: 'context' | 'add' | 'del'
  old?: number
  new?: number
  text: string
}

export interface DiffHunk {
  old_start: number
  old_lines: number
  new_start: number
  new_lines: number
  header: string
  lines: DiffLine[]
}

export interface DiffFile {
  path: string
  status: 'added' | 'modified' | 'deleted'
  binary: boolean
  additions: number
  deletions: number
  hunks?: DiffHunk[]
}

export interface Diff {
  base: string
  head: string
  additions: number
  deletions: number
  truncated: boolean
  files: DiffFile[]
}

export interface SecretStatus {
  key: string
  set: boolean
  known: boolean
  purpose: string
  host?: string
  updated_at?: string
}

export interface SettingsView {
  ai_model: string
  guidance: string
  secrets: SecretStatus[]
}

export interface SecretCheck {
  ok: boolean
  expires_at?: string
  message: string
}

export type EventType =
  | 'change.submitted'
  | 'change.state_changed'
  | 'change.merged'
  | 'run.started'
  | 'step.finished'
  | 'run.finished'
  | 'verdict.issued'
  | 'alert.raised'
  | 'alert.resolved'

export interface PlumbixEvent {
  id: number
  type: EventType
  time: string
  repo_id?: string
  change_id?: string
  run_id?: string
  payload: Record<string, unknown>
}

export interface ApiError {
  code: string
  message: string
  details?: Record<string, unknown>
}
