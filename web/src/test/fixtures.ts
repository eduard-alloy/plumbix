// API-shaped sample data for tests, modelled on the design handoff. Times
// are relative to NOW so ages are stable.
import type { Alert, Change, PipelineFile, Repo, Run, Step } from '../model'

export const NOW = new Date('2026-10-16T09:30:00Z')

function at(minutesAgo: number): string {
  return new Date(NOW.getTime() - minutesAgo * 60000).toISOString()
}

export const repos: Repo[] = [
  { id: 'r_plumbix00001', host: 'github', url: 'https://github.com/plumbix/plumbix', protected_branches: ['main'], merge_strategy: 'squash', pull_requests: true, merger: 'plumbix', created_at: at(99999) },
  { id: 'r_ledgerapi001', host: 'azuredevops', url: 'https://dev.azure.com/northwind/ledger/_git/ledger-api', protected_branches: ['main'], merge_strategy: 'merge', merger: 'plumbix', created_at: at(99999) },
]

function step(name: string, status: Step['status'], ms: number, exit = 0, validator = false): Step {
  return { name, validator: validator || name === 'validate' || name === 'ai-review', status, exit_code: exit, duration_ms: ms }
}

const okSteps = (): Step[] => [step('build', 'succeeded', 42000), step('test', 'succeeded', 118000), step('lint', 'succeeded', 11000), step('validate', 'succeeded', 4000), step('ai-review', 'succeeded', 38000)]
const fixSteps = (): Step[] => [step('build', 'succeeded', 41000), step('test', 'failed', 123000, 1), step('lint', 'succeeded', 10000), step('validate', 'succeeded', 3000), step('ai-review', 'succeeded', 35000)]

export const approved: Change = {
  id: 'c_k7m2q9x4w1zp',
  repo_id: 'r_plumbix00001',
  source_branch: 'feat/retry-uploads',
  target_branch: 'main',
  title: 'Retry failed artifact uploads',
  intent:
    'Uploads to the evidence store fail a few times a day on transient network errors and the whole run is marked errored.\n\nRetry an upload up to three times with exponential backoff before giving up, and record each attempt in the step log so the failure is visible when it does happen.\n\n- no retry on a 4xx answer\n- `PLUMBIX_UPLOAD_RETRIES` caps the count',
  acceptance_criteria: [
    { id: 'AC-1', text: 'A transient upload error is retried up to three times', check: { steps: ['test'] } },
    { id: 'AC-2', text: 'The retry attempts appear in the step log', check: { steps: ['test'] } },
    { id: 'AC-3', text: 'No change to the public API or the pipeline format' },
  ],
  constraints: ['no new dependencies'],
  submitted_by: 'kai',
  state: 'approved',
  created_at: at(78),
  updated_at: at(4),
}

export const approvedRuns: Run[] = [
  {
    id: 'run_3d9x7p2cq1aa',
    repo_id: approved.repo_id,
    change_id: approved.id,
    branch: approved.source_branch,
    iteration: 1,
    head: '4c2e8d1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    status: 'finished',
    outcome: 'fix_requested',
    result: {
      state: 'fix_requested',
      criteria: [
        { id: 'AC-1', text: 'A transient upload error is retried up to three times', status: 'unmet', assessments: [{ validator: 'validate', status: 'unmet', reason: 'step test failed (exit 1)', evidence: ['evidence/test/log.txt'] }] },
        { id: 'AC-2', text: 'The retry attempts appear in the step log', status: 'met', assessments: [{ validator: 'validate', status: 'met', reason: 'step test succeeded', evidence: ['evidence/test/log.txt'] }] },
        { id: 'AC-3', text: 'No change to the public API or the pipeline format', status: 'unknown', assessments: [{ validator: 'validate', status: 'unknown', reason: 'no check given' }] },
      ],
      findings: [{ validator: 'ai-review', severity: 'blocker', message: 'AC-1 unmet: only one retry attempted', criterion: 'AC-1', file: 'internal/evidence/upload.go', line: 64 }],
      risks: [],
      reasons: [],
      validators: [
        { name: 'validate', outcome: 'fix', summary: 'AC-1 unmet' },
        { name: 'ai-review', outcome: 'fix', summary: 'only one retry attempted' },
      ],
    },
    steps: fixSteps(),
    started_at: at(44),
    finished_at: at(41),
  },
  {
    id: 'run_8h2k4m1qz2bb',
    repo_id: approved.repo_id,
    change_id: approved.id,
    branch: approved.source_branch,
    iteration: 2,
    head: '9f3c1e2cccccccccccccccccccccccccccccccccc',
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    status: 'finished',
    outcome: 'approved',
    result: {
      state: 'approved',
      criteria: [
        {
          id: 'AC-1',
          text: 'A transient upload error is retried up to three times',
          status: 'met',
          assessments: [
            { validator: 'ai-review', status: 'met', reason: 'TestUploadRetriesThrice covers three attempts with backoff', evidence: ['evidence/ai-review/review.json'] },
            { validator: 'validate', status: 'met', reason: 'step test succeeded', evidence: ['evidence/test/log.txt'] },
          ],
        },
        { id: 'AC-2', text: 'The retry attempts appear in the step log', status: 'met', assessments: [{ validator: 'validate', status: 'met', reason: 'step test succeeded', evidence: ['evidence/test/log.txt'] }] },
        {
          id: 'AC-3',
          text: 'No change to the public API or the pipeline format',
          status: 'met',
          assessments: [
            { validator: 'ai-review', status: 'met', reason: 'no files under contracts/ or .plumbix/ changed', evidence: ['evidence/ai-review/review.json'] },
            { validator: 'validate', status: 'unknown', reason: 'no check given' },
          ],
        },
      ],
      findings: [
        { validator: 'ai-review', severity: 'minor', message: 'Backoff ceiling is hard-coded at 8 s; consider a constant', file: 'internal/evidence/upload.go', line: 71 },
        { validator: 'ai-grok', advisory: true, severity: 'blocker', message: 'The retry cap should be configurable, not a constant', file: 'internal/evidence/upload.go', line: 70 },
      ],
      risks: [{ validator: 'ai-review', area: 'tests', message: '2 test files were modified', human_required: false }],
      reasons: [],
      validators: [
        { name: 'validate', outcome: 'approve', summary: 'all checked criteria met' },
        { name: 'ai-review', outcome: 'approve', summary: 'the change does what it says' },
        { name: 'ai-grok', advisory: true, outcome: 'reject', summary: 'would reject: the retry cap is a magic number' },
      ],
    },
    steps: [...okSteps(), { ...step('ai-grok', 'succeeded', 29000, 0, true), advisory: true }],
    started_at: at(8),
    finished_at: at(4),
  },
]

export const fixRequested: Change = {
  ...approved,
  id: 'c_p2v8n4r1t6ya',
  source_branch: 'feat/ado-token-expiry',
  title: 'Add Azure DevOps token expiry warning',
  intent: 'Azure DevOps tokens expire without warning. Raise a token_expiry alert seven days before.',
  acceptance_criteria: [
    { id: 'AC-1', text: 'Expiry is read from the Azure DevOps API', check: { steps: ['test'] } },
    { id: 'AC-2', text: 'A token_expiry alert is raised seven days before expiry', check: { steps: ['test'] } },
    { id: 'AC-3', text: 'GitHub token checks are unchanged', check: { command: 'go test ./internal/hosts/github/...' } },
  ],
  constraints: ['no new dependencies', 'never log token values'],
  submitted_by: 'mira',
  pull_request: { number: 7, url: 'https://github.com/plumbix/plumbix/pull/7' },
  state: 'fix_requested',
  created_at: at(50),
  updated_at: at(12),
}

export const fixRequestedRuns: Run[] = [
  {
    id: 'run_6t1v9b3nq3cc',
    repo_id: fixRequested.repo_id,
    change_id: fixRequested.id,
    branch: fixRequested.source_branch,
    iteration: 1,
    head: '2b7e4f0dddddddddddddddddddddddddddddddddd',
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    status: 'finished',
    outcome: 'fix_requested',
    result: {
      state: 'fix_requested',
      criteria: [
        { id: 'AC-1', text: 'Expiry is read from the Azure DevOps API', status: 'unmet', assessments: [{ validator: 'ai-review', status: 'unmet', reason: 'Expiry is parsed from the token name; the API is never called', evidence: ['evidence/ai-review/review.json'] }] },
        { id: 'AC-2', text: 'A token_expiry alert is raised seven days before expiry', status: 'unmet', assessments: [{ validator: 'ai-review', status: 'unmet', reason: 'The warning goes to the server log; no alert is created', evidence: ['evidence/ai-review/review.json'] }] },
        { id: 'AC-3', text: 'GitHub token checks are unchanged', status: 'met', assessments: [{ validator: 'validate', status: 'met', reason: 'command exited 0', evidence: ['evidence/validate/checks.txt'] }] },
      ],
      findings: [
        { validator: 'ai-review', severity: 'blocker', message: 'Expiry is parsed from the token name instead of calling GET _apis/tokens/pats', criterion: 'AC-1', file: 'internal/hosts/azure/token.go', line: 112 },
        { validator: 'ai-review', severity: 'blocker', message: 'Expiry is logged with slog.Warn but no alert is created', criterion: 'AC-2', file: 'internal/alerts/token.go', line: 44 },
        { validator: 'ai-review', severity: 'minor', message: 'TestTokenExpiry uses time.Now(); inject a clock so the test is stable', file: 'internal/hosts/azure/token_test.go', line: 18 },
      ],
      risks: [{ validator: 'ai-review', area: 'credentials', message: 'The token is sent through a new HTTP client; check it is not logged when a request fails', human_required: true }],
      reasons: [],
      validators: [
        { name: 'validate', outcome: 'fix', summary: 'two criteria unmet' },
        { name: 'ai-review', outcome: 'fix', summary: 'expiry is not read from the API' },
      ],
    },
    steps: fixSteps(),
    started_at: at(16),
    finished_at: at(12),
  },
]

export const running: Change = {
  ...approved,
  id: 'c_h9d3s7l2m5qe',
  source_branch: 'feat/log-stream',
  title: 'Stream step logs over the event stream',
  intent: 'Logs should follow live.',
  acceptance_criteria: [{ id: 'AC-1', text: 'New lines appear within a second', check: { steps: ['test'] } }],
  constraints: [],
  submitted_by: 'kai',
  state: 'running',
  created_at: at(3),
  updated_at: at(1),
}

export const runningRuns: Run[] = [
  {
    id: 'run_live00000dd',
    repo_id: running.repo_id,
    change_id: running.id,
    branch: running.source_branch,
    iteration: 1,
    head: '77aa11beeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    status: 'running',
    steps: [step('build', 'succeeded', 25000), step('test', 'running', 0)],
    started_at: new Date(NOW.getTime() - 72000).toISOString(),
  },
]

export const needsHuman: Change = {
  ...approved,
  id: 'c_w4x6z1b8c3nf',
  repo_id: 'r_ledgerapi001',
  source_branch: 'chore/go-1.27',
  title: 'Bump Go toolchain to 1.27',
  intent: 'Use the current toolchain.',
  acceptance_criteria: [{ id: 'AC-1', text: 'Builds on 1.27', check: { steps: ['build'] } }],
  constraints: [],
  submitted_by: 'otto',
  state: 'needs_human',
  created_at: at(30),
  updated_at: at(9),
}

export const needsHumanRuns: Run[] = [
  {
    id: 'run_nh000000ee',
    repo_id: needsHuman.repo_id,
    change_id: needsHuman.id,
    branch: needsHuman.source_branch,
    iteration: 1,
    head: '12ab34cffffffffffffffffffffffffffffffffff',
    base: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    status: 'errored',
    outcome: 'needs_human',
    error: 'step build could not run: image golang:1.27 not found',
    steps: [{ name: 'build', validator: false, status: 'errored', exit_code: 0, duration_ms: 1200, error: 'image golang:1.27 not found' }],
    started_at: at(10),
    finished_at: at(9),
  },
]

export const open: Change = {
  ...approved,
  id: 'c_e2y5u8i3o6pa',
  repo_id: 'r_ledgerapi001',
  source_branch: 'feat/idempotency-keys',
  title: 'Add idempotency keys to POST /entries',
  intent: 'Retries must not double-post.',
  acceptance_criteria: [{ id: 'AC-1', text: 'A repeated key returns the first result' }],
  constraints: [],
  submitted_by: 'otto',
  state: 'open',
  created_at: at(3),
  updated_at: at(3),
}

export const merged: Change = {
  ...approved,
  id: 'c_m3q7t1z8x2ra',
  source_branch: 'feat/repo-strategy',
  title: 'Show merge strategy on the repositories page',
  state: 'merged',
  merge: { commit: '5c0e7a3b9d21aaaaaaaaaaaaaaaaaaaaaaaaaaaa', previous: 'a81d7c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', strategy: 'squash', run_id: 'run_8h2k4m1qz2bb', merged_at: at(70) },
  created_at: at(200),
  updated_at: at(70),
}

export const closed: Change = {
  ...approved,
  id: 'c_u3i6o9p2a5sd',
  source_branch: 'exp/parallel-steps',
  title: 'Experiment: run independent steps in parallel',
  state: 'closed',
  created_at: at(400),
  updated_at: at(300),
}

export const allChanges: Change[] = [approved, fixRequested, running, needsHuman, open, merged, closed]

export const runsByChange: Record<string, Run[]> = {
  [approved.id]: approvedRuns,
  [fixRequested.id]: fixRequestedRuns,
  [running.id]: runningRuns,
  [needsHuman.id]: needsHumanRuns,
  [open.id]: [],
  [merged.id]: approvedRuns,
  [closed.id]: fixRequestedRuns,
}

export const alerts: Alert[] = [
  {
    id: 'al_5n2c8v1xaaaa',
    kind: 'bypass_commit',
    key: 'main@3b7f2a9',
    repo_id: 'r_plumbix00001',
    message: 'commit 3b7f2a9d1c0e by eduard reached main without a verdict',
    details: { branch: 'main', head: '3b7f2a9d1c0e', count: 1, commits: [{ sha: '3b7f2a9d1c0e', author: 'eduard', email: 'e@example.com', time: at(27), subject: 'hotfix: raise runner timeout to 15m' }] },
    created_at: at(27),
  },
]

export const pipelineFile: PipelineFile = {
  file: '.plumbix/pipeline.yaml',
  branch: 'main',
  sha: '4f81c2a0000000000000000000000000deadbeef',
  content: 'version: 1\nsteps: []\n',
  pipeline: {
    version: 1,
    steps: [
      { name: 'build', image: 'golang:1.27', commands: ['go build ./...'], needs: [], env: {}, secrets: [], artifacts: [], cache: ['/go/pkg/mod', '/root/.cache/go-build'], timeout: '10m', validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
      { name: 'test', image: 'golang:1.27', commands: ['go test ./...'], needs: ['build'], env: { GOFLAGS: '-mod=readonly' }, secrets: [], artifacts: ['coverage.out'], cache: [], timeout: '15m', validator: false, advisory: false, external: false, retries: 1, allow_failure: false, paths: [], stage: 'checks' },
      { name: 'contract', image: 'golang:1.27', commands: ['go run ./cmd/openapi-diff'], needs: ['build'], env: {}, secrets: ['API_TOKEN'], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: true, paths: ['api/**'], stage: 'checks' },
      { name: 'deploy-check', image: 'alpine:3', commands: ['echo ok'], needs: ['test', 'contract'], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
      { name: 'validate', image: 'plumbix-reference-validator:dev', commands: [], needs: ['test', 'contract', 'deploy-check'], env: {}, secrets: [], artifacts: [], cache: [], validator: true, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
    ],
    stages: [{ name: 'checks', steps: ['test', 'contract'] }],
    disabled: false,
  },
  problems: [],
  last_run: { run_id: 'run_1', head: '4f81c2a', steps: { build: { status: 'succeeded', duration_ms: 42000 }, test: { status: 'succeeded', duration_ms: 118000 }, validate: { status: 'succeeded', duration_ms: 4000 } } },
}

export const pipelineFiles: Record<string, PipelineFile> = {
  [repos[0].id]: pipelineFile,
  [repos[1].id]: {
    file: '.plumbix/pipeline.yaml',
    branch: 'main',
    sha: 'a1b2c3d4000000000000000000000000deadbeef',
    content: 'version: 1\nsteps:\n  - {name: test, image: node:24, run: npm test}\n',
    pipeline: {
      version: 1,
      steps: [
        { name: 'test', image: 'node:24', commands: ['npm test'], needs: [], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
        { name: 'ai-review', image: '', commands: [], needs: ['test'], env: {}, secrets: [], artifacts: [], cache: [], validator: true, advisory: false, external: true, retries: 0, allow_failure: false, paths: [] },
        { name: 'ai-grok', image: 'plumbix-ai-validator:dev', commands: [], needs: ['test'], env: {}, secrets: [], artifacts: [], cache: [], validator: true, advisory: true, external: false, reviewer: 'grok-4', retries: 0, allow_failure: false, paths: [] },
      ],
      stages: [],
      disabled: false,
    },
    problems: [],
  },
}
