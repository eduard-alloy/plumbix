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
export type StepStatus = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'errored' | 'skipped' | 'cancelled'
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
  /** Set while a person's approval stands; a new run clears it. */
  human_approval?: { reason: string; head: string; at: string }
  /** True when Plumbix created the source branch (a pipeline editor change); it can be updated from its target. */
  plumbix_branch?: boolean
  source_head?: string
  created_at: string
  updated_at: string
}

export interface Step {
  name: string
  validator: boolean
  /** A validator whose verdict was recorded but not counted (WU-030). */
  advisory?: boolean
  status: StepStatus
  exit_code: number
  duration_ms: number
  error?: string
  /** Present when above one: the step was retried. */
  attempts?: number
  /** The failure was tolerated by the step's allow_failure. */
  allowed_failure?: boolean
  /** Why a skipped step did not run. */
  reason?: 'needs' | 'paths'
  /** The pipeline stage the step belongs to. */
  stage?: string
}

export interface Assessment {
  validator: string
  status: CriterionStatus
  reason: string
  evidence?: string[]
  /** From an advisory validator: shown, not merged into the criterion's status (WU-030). */
  advisory?: boolean
}

export interface CombinedCriterion {
  id: string
  text: string
  status: CriterionStatus
  assessments: Assessment[]
}

export interface Finding {
  validator: string
  advisory?: boolean
  severity: Severity
  message: string
  criterion?: string
  file?: string
  line?: number
}

export interface Risk {
  validator: string
  advisory?: boolean
  area: string
  message: string
  human_required: boolean
}

export type ValidatorDecision = 'approve' | 'fix' | 'reject'

export interface ValidatorOutcome {
  name: string
  /** Recorded, never counted (WU-030). */
  advisory?: boolean
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

/** An external reviewer the server has heard from since it started (WU-026). */
export interface Reviewer {
  name: string
  /** The reviewer's repository filter, when it polls for one repository only. */
  repo_id?: string
  last_seen: string
}

export interface Alert {
  id: string
  kind: 'bypass_commit' | 'token_expiry' | 'no_reviewer'
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

/** anthropic and openai are API kinds (the container validator, billed per token); claude-code is the external loop on a person's machine, on its subscription. */
export type ReviewerKind = 'anthropic' | 'openai' | 'claude-code'
export type ReviewerTools = 'read-only' | 'full'
/** Where a profile runs: api profiles judge from the container validator, the external one answers external steps. */
export type ReviewerFamily = 'api' | 'external'

/** ReviewerProfile is a named model configuration a validator step is bound to with reviewer: <name> (WU-028, A29). */
export interface ReviewerProfile {
  name: string
  kind: ReviewerKind
  family: ReviewerFamily
  /** For openai: the chat-completions base, such as https://api.openai.com/v1. */
  base_url: string
  /** Empty means the kind's default. */
  model: string
  /** Whether a reviewer with tools (Claude Code) may run commands. */
  tools: ReviewerTools
  /** The secret holding the key; set through the secrets endpoint, never shown. */
  key_secret: string
  updated_at: string
  key_set: boolean
  /** Whether this is its family's default. */
  default: boolean
}

export interface ReviewerInput {
  kind: ReviewerKind
  base_url?: string
  model?: string
  tools?: ReviewerTools
  key_secret?: string
}

export interface SettingsView {
  guidance: string
  /** Each family's default profile name; empty for none. */
  defaults: { api: string; external: string }
  reviewers: ReviewerProfile[]
  secrets: SecretStatus[]
}

export interface RepoSettings {
  guidance: string
  token_set?: boolean
  token_updated_at?: string
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
  | 'step.started'
  | 'step.finished'
  | 'run.finished'
  | 'verdict.issued'
  | 'alert.raised'
  | 'alert.resolved'
  | 'change.approved_by_person'
  | 'change.refreshed'

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

/** PipelineStep is one step of the parsed pipeline file, as the API serves it. */
export interface PipelineStep {
  name: string
  image: string
  commands: string[]
  needs: string[]
  env: Record<string, string>
  secrets: string[]
  artifacts: string[]
  /** Absolute container paths kept between runs of the repository (WU-029). */
  cache: string[]
  timeout?: string
  validator: boolean
  /** The validator's verdict is recorded but never counted (WU-030). */
  advisory: boolean
  /** The validator waits for a reviewer outside the pipeline. */
  external: boolean
  /** The reviewer profile the validator step names; absent for the default. */
  reviewer?: string
  retries: number
  allow_failure: boolean
  paths: string[]
  stage?: string
}

export interface PipelineModel {
  version: number
  steps: PipelineStep[]
  stages: { name: string; steps: string[] }[]
}

export interface PipelineProblem {
  path: string
  line: number
  message: string
}

/** PipelineFile is a repository's pipeline file at a branch head. */
export interface PipelineFile {
  file: string
  branch: string
  sha: string
  content: string
  pipeline: PipelineModel | null
  problems: PipelineProblem[]
  last_run?: { run_id: string; head: string; steps: Record<string, { status: string; duration_ms: number }> }
}
