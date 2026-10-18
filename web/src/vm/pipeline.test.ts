import { describe, expect, it } from 'vitest'
import * as fx from '../test/fixtures'
import type { PipelineFile } from '../model'
import { addStep, bands, diffSummary, find, fromApi, groupSteps, levels, mergeSteps, needOptions, removeSteps, renameStep, saveDefaults, setField, toYaml, toggleValidator, yamlDiff, yamlStr, type PModel } from './pipeline'

const sampleFile = fx.pipelineFile

const model = (): PModel => fromApi(sampleFile)!

describe('pipeline model', () => {
  it('reads the served file into steps, env rows, secrets and stages', () => {
    const p = model()
    expect(p.steps.map((s) => s.id)).toEqual(['build', 'test', 'contract', 'deploy-check', 'validate'])
    const test = find(p, 'test')!
    expect(test).toMatchObject({ timeout: '15m', retries: 1, group: 'checks', evidence: ['coverage.out'], cache: [], last: 118, lastStatus: 'succeeded' })
    expect(find(p, 'build')!.cache).toEqual(['/go/pkg/mod', '/root/.cache/go-build'])
    expect(test.env).toEqual([{ k: 'GOFLAGS', v: '-mod=readonly', secret: false }])
    expect(find(p, 'contract')!.env).toEqual([{ k: 'API_TOKEN', v: '', secret: true }])
    expect(find(p, 'contract')).toMatchObject({ allowFail: true, paths: ['api/**'], last: null })
    expect(find(p, 'validate')).toMatchObject({ validator: true, run: [] })
    expect(p.stageOrder).toEqual(['checks'])
    expect(fromApi({ ...sampleFile, pipeline: null })).toBeNull()
  })

  it('writes format v1 YAML: string run for one command, list for more, validators last needing every step', () => {
    const p = model()
    const y = toYaml(p).join('\n')
    expect(y).toBe(
      [
        'version: 1',
        'steps:',
        '  - name: build',
        '    image: golang:1.27',
        '    run: go build ./...',
        '    cache: [/go/pkg/mod, /root/.cache/go-build]',
        '    timeout: 10m',
        '  - name: test',
        '    image: golang:1.27',
        '    run: go test ./...',
        '    needs: [build]',
        '    env:',
        '      GOFLAGS: "-mod=readonly"',
        '    artifacts: [coverage.out]',
        '    timeout: 15m',
        '    retries: 1',
        '  - name: contract',
        '    image: golang:1.27',
        '    run: go run ./cmd/openapi-diff',
        '    needs: [build]',
        '    secrets: [API_TOKEN]',
        '    allow_failure: true',
        '    when:',
        '      paths: ["api/**"]',
        '  - name: deploy-check',
        '    image: alpine:3',
        '    run: echo ok',
        '    needs: [test, contract]',
        '  - name: validate',
        '    image: plumbix-reference-validator:dev',
        '    validator: true',
        '    needs: [build, test, contract, deploy-check]',
        'stages:',
        '  checks: [test, contract]',
      ].join('\n'),
    )
    const two = setField(p, 'build', 'run', ['go build ./...', 'go vet ./...'])
    expect(toYaml(two).join('\n')).toContain('    run:\n      - go build ./...\n      - go vet ./...')
  })

  it('quotes YAML scalars that would read as something else', () => {
    expect(yamlStr('go test ./...')).toBe('go test ./...')
    expect(yamlStr('true')).toBe('"true"')
    expect(yamlStr('10')).toBe('"10"')
    expect(yamlStr('echo "hi" && test -z "$(gofmt -l .)"')).toBe('"echo \\"hi\\" && test -z \\"$(gofmt -l .)\\""')
    expect(yamlStr('key: value')).toBe('"key: value"')
    expect(yamlStr('-mod=readonly')).toBe('"-mod=readonly"')
    expect(yamlStr('api/**')).toBe('"api/**"')
    expect(yamlStr('a # b')).toBe('"a # b"')
  })

  it('adds steps with unique names, after or in parallel', () => {
    const p = model()
    const after = addStep(p, ['test'], 'checks')
    expect(after.id).toBe('step')
    expect(find(after.model, 'step')).toMatchObject({ needs: ['test'], group: 'checks' })
    const again = addStep(after.model, [], null)
    expect(again.id).toBe('step-2')
  })

  it('removes a step and rewires the steps that needed it', () => {
    const p = removeSteps(model(), ['test'])
    expect(find(p, 'test')).toBeUndefined()
    expect(find(p, 'deploy-check')!.needs).toEqual(['contract', 'build'])
    expect(toYaml(p).join('\n')).toContain('  checks: [contract]')
  })

  it('renames a step everywhere and refuses taken or empty names', () => {
    const r = renameStep(model(), 'build', 'Compile!')
    expect(r.id).toBe('compile')
    expect(find(r.model, 'test')!.needs).toEqual(['compile'])
    expect(find(r.model, 'contract')!.needs).toEqual(['compile'])
    expect(renameStep(model(), 'build', 'test').id).toBe('build')
    expect(renameStep(model(), 'build', '!!!').id).toBe('build')
  })

  it('only offers cycle-free, non-validator needs and drops a need that would loop', () => {
    const p = model()
    expect(needOptions(p, 'build')).toEqual([])
    expect(needOptions(p, 'contract')).toEqual(['test'])
    expect(needOptions(p, 'deploy-check')).toEqual(['build'])
    const looped = setField(p, 'build', 'needs', ['deploy-check', 'validate'])
    expect(find(looped, 'build')!.needs).toEqual([])
  })

  it('turns a step into a validator: it loses needs, paths, stage and allow_failure, dependents are rewired, and it moves last', () => {
    const p = toggleValidator(model(), 'contract')
    const c = find(p, 'contract')!
    expect(c).toMatchObject({ validator: true, needs: [], paths: [], group: null, allowFail: false })
    expect(find(p, 'deploy-check')!.needs).toEqual(['test', 'build'])
    expect(p.steps.map((s) => s.id)).toEqual(['build', 'test', 'deploy-check', 'validate', 'contract'])
    expect(levels(p).contract).toBe(0)
    const back = toggleValidator(p, 'contract')
    expect(find(back, 'contract')!.validator).toBe(false)
  })

  it('merges steps into one with the union of needs, env and evidence, the longest timeout and the highest retries', () => {
    const r = mergeSteps(model(), ['contract', 'test'])!
    expect(r.id).toBe('test-contract')
    const m = find(r.model, 'test-contract')!
    expect(m.run).toEqual(['go test ./...', 'go run ./cmd/openapi-diff'])
    expect(m.needs).toEqual(['build'])
    expect(m.timeout).toBe('15m')
    expect(m.retries).toBe(1)
    expect(m.allowFail).toBe(false)
    expect(m.env).toEqual([
      { k: 'GOFLAGS', v: '-mod=readonly', secret: false },
      { k: 'API_TOKEN', v: '', secret: true },
    ])
    expect(m.paths).toEqual([])
    expect(m.group).toBe('checks')
    expect(find(r.model, 'deploy-check')!.needs).toEqual(['test-contract'])
    expect(r.model.steps.map((s) => s.id)).toEqual(['build', 'test-contract', 'deploy-check', 'validate'])
    expect(mergeSteps(model(), ['test'])).toBeNull()
  })

  it('groups steps into a named stage', () => {
    const p = groupSteps(model(), ['build', 'deploy-check', 'validate'], 'Edge Checks')
    expect(find(p, 'build')!.group).toBe('edge-checks')
    expect(find(p, 'validate')!.group).toBeNull()
    expect(p.stageOrder).toEqual(['checks', 'edge-checks'])
  })

  it('summarises edits and marks added YAML lines', () => {
    const base = model()
    let p = addStep(base, ['test'], null).model
    p = setField(p, 'build', 'retries', 2)
    p = removeSteps(p, ['contract'])
    expect(diffSummary(base, p)).toEqual(['added step after test', 'removed contract', 'build: retries 2', 'deploy-check: needs test, build'])
    const marked = yamlDiff(p, base)
    expect(marked.filter((l) => l.added).map((l) => l.t)).toContain('    retries: 2')
    expect(marked.find((l) => l.t === '  - name: build')!.added).toBe(false)
    expect(diffSummary(base, base)).toEqual([])
    expect(saveDefaults('plumbix', diffSummary(base, p))).toEqual({
      branch: 'pipeline/added-step-after-test',
      title: 'Pipeline: added step after test; removed contract',
      intent: 'Change the plumbix pipeline: added step after test; removed contract; build: retries 2; deploy-check: needs test, build. No other file changes.',
    })
  })

  it('lays out bands: named stages and waves by depth, chains on one row, validators last', () => {
    const b = bands(model(), new Set(['test']))
    expect(b.map((x) => x.name)).toEqual(['build', 'checks', 'deploy-check', 'validators'])
    expect(b[0].sub).toBe('1 step · unnamed · starts first')
    expect(b[1].sub).toBe('2 steps · starts after stage 1')
    expect(b[1].est).toBe('~1m 58s')
    const checks = b[1]
    const test = checks.cards.find((c) => c.id === 'test')!
    const contract = checks.cards.find((c) => c.id === 'contract')!
    expect([test.x, test.y]).toEqual([0, 0])
    expect([contract.x, contract.y]).toEqual([400, 0])
    expect(test.sel).toBe(true)
    // Chips that do not fit whole within the card's budget collapse into +N, whose title lists them.
    expect(test.chips.map((c) => c.t)).toEqual(['after build', '15m', '+2'])
    expect(test.chips[2].title).toBe('retries 1 · 1 evidence')
    expect(contract.chips.map((c) => c.t)).toEqual(['after build', '+3'])
    expect(contract.chips[1].title).toBe('allowed to fail · if api/** · 1 secret')
    expect(test.last).toBe('1m 58s')
    expect(contract.last).toBe('not run yet')
    const deploy = b[2].cards[0]
    expect(deploy.chips.map((c) => c.t)).toEqual(['after test', 'after contract'])
    expect(b[3].validators).toBe(true)
    expect(b[3].cards[0].cmd).toBe('')
    // Three independent steps in one wave fill the first row left to right.
    const flat: PModel = { steps: ['a', 'b', 'c', 'd'].map((id) => ({ ...find(model(), 'build')!, id, needs: [], group: null })), stageOrder: [] }
    const fb = bands(flat, new Set())
    expect(fb).toHaveLength(1)
    expect(fb[0].cards.map((c) => [c.x, c.y])).toEqual([
      [0, 0],
      [400, 0],
      [800, 0],
      [0, 70],
    ])
    // A chain inside one stage shares a row with a line between the cards.
    const chain = groupSteps(model(), ['build', 'test', 'contract', 'deploy-check'], 'all')
    const cb = bands(chain, new Set())[0]
    const at = (id: string) => cb.cards.find((c) => c.id === id)!
    expect([at('build').x, at('build').y]).toEqual([0, 0])
    expect([at('test').x, at('test').y]).toEqual([400, 0])
    expect([at('contract').x, at('contract').y]).toEqual([400, 70])
    expect([at('deploy-check').x, at('deploy-check').y]).toEqual([800, 0])
    expect(cb.edges).toHaveLength(4)
    expect(cb.w).toBe(1160)
  })
})

describe('advisory validators in the editor (WU-030)', () => {
  const served = (): PipelineFile => ({
    file: '.plumbix/pipeline.yaml',
    branch: 'main',
    sha: 'abc',
    content: '',
    pipeline: {
      version: 1,
      steps: [
        { name: 'test', image: 'golang:1.27', commands: ['go test ./...'], needs: [], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
        { name: 'ai-grok', image: 'plumbix-ai-validator:dev', commands: [], needs: ['test'], env: {}, secrets: [], artifacts: [], cache: [], validator: true, advisory: true, external: false, reviewer: 'grok-4', retries: 0, allow_failure: false, paths: [] },
      ],
      stages: [],
    },
    problems: [],
  })

  it('reads advisory, writes it back after the validator line, and lists it as a change', () => {
    const m = fromApi(served())!
    expect(find(m, 'ai-grok')!.advisory).toBe(true)
    const lines = toYaml(m)
    expect(lines).toContain('    advisory: true')
    expect(lines.indexOf('    advisory: true')).toBeGreaterThan(lines.indexOf('    validator: true'))
    const plainAgain = toggleValidator(m, 'ai-grok')
    expect(find(plainAgain, 'ai-grok')).toMatchObject({ validator: false, advisory: false })
    expect(toYaml(plainAgain).join('\n')).not.toContain('advisory')
    const counted = setField(m, 'ai-grok', 'advisory', false)
    expect(diffSummary(m, counted)).toContain('ai-grok: counts')
    expect(diffSummary(counted, m)).toContain('ai-grok: advisory')
  })
})
