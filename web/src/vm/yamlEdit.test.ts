import { describe, expect, it } from 'vitest'
import * as fx from '../test/fixtures'
import { addStep, fromApi, groupSteps, removeSteps, renameStep, renderYaml, setField, toggleValidator, yamlDiff, type PModel } from './pipeline'
import { applyModel } from './yamlEdit'

/** Plumbix's own pipeline file: the header comment the editor used to drop, flow lists, plain scalars, 30m. */
const own = `# Plumbix's own pipeline. Every change to this repository is validated by
# Plumbix itself: four independent steps run in parallel, then two
# validators judge the change against its intent. The deterministic
# validator must approve; the AI review alone is never enough (A12).
# The AI review is an external step (A26): a reviewer loop on the
# owner's machine, plumbix review --watch, answers it with Claude Code.
# The Go and npm steps keep their module, build and package caches between
# runs (WU-029, A30), per repository and step, under the server's data
# directory. ai-grok asks the grok-4 reviewer profile from inside the
# pipeline and is advisory (WU-030, A31): its verdict is recorded beside
# the Claude review for comparison and never counted.
version: 1
steps:
  - name: build
    image: golang:1.27
    run: go build ./...
    cache: [/go/pkg/mod, /root/.cache/go-build]
  - name: test
    image: golang:1.27
    run: go test ./...
    cache: [/go/pkg/mod, /root/.cache/go-build]
  - name: lint
    image: golang:1.27
    run: go vet ./... && test -z "$(gofmt -l .)"
    cache: [/go/pkg/mod, /root/.cache/go-build]
  - name: web
    image: node:24-alpine
    run: cd web && npm ci --no-audit --no-fund && npm test -- --reporter=dot && npm run build
    cache: [/root/.npm]
  - name: validate
    image: plumbix-reference-validator:dev
    validator: true
    needs: [build, test, lint, web]
  - name: ai-review
    validator: true
    external: true
    needs: [build, test, lint, web]
    timeout: 30m
  - name: ai-grok
    image: plumbix-ai-validator:dev
    validator: true
    reviewer: grok-4
    advisory: true
    needs: [build, test, lint, web]
stages:
  checks: [build, test, lint, web]
`

/** ownModel is the model the server would serve for that file. */
function ownModel(): PModel {
  const step = (name: string, extra: Record<string, unknown>) => ({ name, image: '', commands: [], needs: [], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [], stage: '', ...extra })
  const checks = { cache: ['/go/pkg/mod', '/root/.cache/go-build'], stage: 'checks' }
  const file = {
    ...fx.pipelineFile,
    content: own,
    pipeline: {
      version: 1,
      steps: [
        step('build', { image: 'golang:1.27', commands: ['go build ./...'], ...checks }),
        step('test', { image: 'golang:1.27', commands: ['go test ./...'], ...checks }),
        step('lint', { image: 'golang:1.27', commands: ['go vet ./... && test -z "$(gofmt -l .)"'], ...checks }),
        step('web', { image: 'node:24-alpine', commands: ['cd web && npm ci --no-audit --no-fund && npm test -- --reporter=dot && npm run build'], cache: ['/root/.npm'], stage: 'checks' }),
        step('validate', { image: 'plumbix-reference-validator:dev', validator: true, needs: ['build', 'test', 'lint', 'web'] }),
        step('ai-review', { validator: true, external: true, needs: ['build', 'test', 'lint', 'web'], timeout: '30m' }),
        step('ai-grok', { image: 'plumbix-ai-validator:dev', validator: true, reviewer: 'grok-4', advisory: true, needs: ['build', 'test', 'lint', 'web'] }),
      ],
      stages: [{ name: 'checks', steps: ['build', 'test', 'lint', 'web'] }],
    },
    last_run: undefined,
  }
  return fromApi(file as typeof fx.pipelineFile)!
}

const lines = (s: string) => s.replace(/\n$/, '').split('\n')
/** added returns the lines of b that are not in a, and removed the reverse, as a multiset difference. */
function delta(a: string, b: string): { added: string[]; removed: string[] } {
  const pool = new Map<string, number>()
  for (const l of lines(a)) pool.set(l, (pool.get(l) ?? 0) + 1)
  const added: string[] = []
  for (const l of lines(b)) {
    const n = pool.get(l) ?? 0
    if (n > 0) pool.set(l, n - 1)
    else added.push(l)
  }
  const removed = [...pool.entries()].flatMap(([l, n]) => Array(n).fill(l) as string[])
  return { added: added.sort(), removed: removed.sort() }
}

describe('the editor keeps the file text (WU-036)', () => {
  it('saves an unchanged model byte-identical', () => {
    expect(applyModel(own, ownModel())).toBe(own)
    expect(renderYaml(ownModel(), own)).toBe(own)
  })

  it('writes effort: medium on ai-grok as exactly one added line; the header, quoting and 30m stay', () => {
    const out = applyModel(own, setField(ownModel(), 'ai-grok', 'effort', 'medium'))
    expect(delta(own, out)).toEqual({ added: ['    effort: medium'], removed: [] })
    expect(out.startsWith("# Plumbix's own pipeline.")).toBe(true)
    expect(out).toContain('    run: go vet ./... && test -z "$(gofmt -l .)"\n')
    expect(out).toContain('    timeout: 30m\n')
    expect(out).not.toContain('30m0s')
  })

  it('replaces a changed scalar in place, deletes a dropped flag and keeps a renamed step\'s comment', () => {
    const file = `# top
version: 1
steps:
  - name: build   # the compiler
    image: golang:1.27
    run: go build ./...
    timeout: 10m
  - name: test
    image: golang:1.27
    run:
      - go test ./...
      - echo done
    needs:
      - build
    allow_failure: true
    retries: 2
    custom_key: kept
`
    const model = fromApi({
      ...fx.pipelineFile,
      content: file,
      pipeline: {
        version: 1,
        steps: [
          { name: 'build', image: 'golang:1.27', commands: ['go build ./...'], needs: [], env: {}, secrets: [], artifacts: [], cache: [], timeout: '10m', validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [], stage: '' },
          { name: 'test', image: 'golang:1.27', commands: ['go test ./...', 'echo done'], needs: ['build'], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 2, allow_failure: true, paths: [], stage: '' },
        ],
        stages: [],
      },
      last_run: undefined,
    })!
    let m = setField(model, 'build', 'timeout', '20m')
    m = setField(m, 'test', 'allowFail', false)
    m = setField(m, 'test', 'needs', ['build'])
    m = renameStep(m, 'build', 'compile').model
    const out = applyModel(file, m)
    expect(out).toBe(`# top
version: 1
steps:
  - name: compile # the compiler
    image: golang:1.27
    run: go build ./...
    timeout: 20m
  - name: test
    image: golang:1.27
    run:
      - go test ./...
      - echo done
    needs:
      - compile
    retries: 2
    custom_key: kept
`)
  })

  it('appends a new step and a stage in the writer\'s order, drops an empty stage, and keeps block lists block', () => {
    let m = ownModel()
    const added = addStep(m, ['build'], 'extra')
    m = setField(added.model, added.id, 'run', ['echo one', 'echo two'])
    m = setField(m, added.id, 'paths', ['web/**'])
    const out = applyModel(own, m)
    const d = delta(own, out)
    // The validators need the new step too, so their needs lines are replaced.
    expect(d.removed).toEqual(Array(3).fill('    needs: [build, test, lint, web]'))
    expect(d.added).toEqual(['  - name: step', '    image: alpine:3', '    run:', '      - echo one', '      - echo two', '    needs: [build]', '    when:', '      paths: [web/**]', '    needs: [build, test, lint, web, step]', '    needs: [build, test, lint, web, step]', '    needs: [build, test, lint, web, step]', '  extra: [step]'].sort())
    expect(out).toContain('  - name: step\n    image: alpine:3\n    run:\n      - echo one\n      - echo two\n    needs: [build]\n    when:\n      paths: [web/**]\n')
    // Removing every step of a stage removes the stage; a step gone from the model leaves the file.
    const gone = applyModel(own, removeSteps(groupSteps(ownModel(), ['build', 'test', 'lint', 'web'], 'all'), ['build', 'test', 'lint', 'web']))
    expect(gone).not.toContain('stages:')
    expect(gone).not.toContain('name: build')
    expect(gone).toContain("# Plumbix's own pipeline.")
  })

  it('turns a step into a validator in place and falls back to the writer for text it cannot read', () => {
    const out = applyModel(own, toggleValidator(ownModel(), 'web'))
    const d = delta(own, out)
    expect(d.added).toEqual(['    validator: true', '    needs: [build, test, lint]', '    needs: [build, test, lint]', '    needs: [build, test, lint]', '    needs: [build, test, lint]', '  checks: [build, test, lint]'].sort())
    expect(d.removed).toEqual(['    cache: [/root/.npm]', '    needs: [build, test, lint, web]', '    needs: [build, test, lint, web]', '    needs: [build, test, lint, web]', '  checks: [build, test, lint, web]'].sort())
    expect(out).toContain("# Plumbix's own pipeline.")
    expect(applyModel('steps: [', ownModel())).toBe(renderYaml(ownModel(), null))
    expect(applyModel('- just a list\n', ownModel())).toContain('version: 1\nsteps:\n  - name: build')
  })

  it('never edits one served step for two model steps: a renamed step keeps its text and a new step under the old name is new', () => {
    const file = `version: 1
steps:
  - name: build # the compiler
    image: golang:1.27
    run: go build ./...
`
    const model = fromApi({
      ...fx.pipelineFile,
      content: file,
      pipeline: { version: 1, steps: [{ name: 'build', image: 'golang:1.27', commands: ['go build ./...'], needs: [], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [], stage: '' }], stages: [] },
      last_run: undefined,
    })!
    const renamed = renameStep(model, 'build', 'compile').model
    const added = addStep(renamed, [], null)
    const m = renameStep(added.model, added.id, 'build').model
    expect(applyModel(file, m)).toBe(`version: 1
steps:
  - name: compile # the compiler
    image: golang:1.27
    run: go build ./...
  - name: build
    image: alpine:3
    run: echo "replace me"
`)
    // Two model steps pointing at one served step (a model built by hand) edit it once; the second is written fresh.
    const twice: PModel = { ...m, steps: m.steps.map((s) => ({ ...s, src: 'build' })) }
    const out = applyModel(file, twice)
    expect(out.match(/# the compiler/g)).toHaveLength(1)
    expect(out).toContain('  - name: compile # the compiler\n')
    expect(out).toContain('  - name: build\n    image: alpine:3\n')
  })

  it('marks only the added lines in the YAML view', () => {
    const base = ownModel()
    const marks = yamlDiff(setField(base, 'ai-grok', 'effort', 'medium'), base, own)
    expect(marks.filter((l) => l.added).map((l) => l.t)).toEqual(['    effort: medium'])
    expect(marks[0].t).toBe("# Plumbix's own pipeline. Every change to this repository is validated by")
  })
})
