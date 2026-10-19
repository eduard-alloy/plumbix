import { describe, expect, it } from 'vitest'
import type { PipelineFile } from '../model'
import { blockers, diffSummary, find, fromApi, REASONS, renderYaml, setField, toggleDisabled, toYaml } from './pipeline'

/** An imported pipeline (WU-044): one step blocked by Docker, one source task with no image or commands. */
const src = `# imported
version: 1
steps:
  - name: unit
    image: golang:1.27
    run: go test ./...
  - name: integration
    image: golang:1.27
    run: docker run --rm redis
    needs: [unit]
    disabled:
      reason: docker
      note: starts Redis with docker run
  - name: publish
    needs: [integration]
    disabled: {reason: task}
`

const file: PipelineFile = {
  file: '.plumbix/pipeline.yaml',
  branch: 'main',
  sha: 'abc',
  content: src,
  problems: [],
  pipeline: {
    version: 1,
    disabled: true,
    stages: [],
    steps: [
      { name: 'unit', image: 'golang:1.27', commands: ['go test ./...'], needs: [], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [] },
      { name: 'integration', image: 'golang:1.27', commands: ['docker run --rm redis'], needs: ['unit'], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [], disabled: { reason: 'docker', note: 'starts Redis with docker run' } },
      { name: 'publish', image: '', commands: [], needs: ['integration'], env: {}, secrets: [], artifacts: [], cache: [], validator: false, advisory: false, external: false, retries: 0, allow_failure: false, paths: [], disabled: { reason: 'task' } },
    ],
  },
}

describe('disabled steps in the editor (WU-044)', () => {
  it('reads disabled steps and counts what blocks the pipeline', () => {
    const m = fromApi(file)!
    expect(find(m, 'integration')!.disabled).toEqual({ reason: 'docker', note: 'starts Redis with docker run' })
    expect(find(m, 'publish')!.disabled).toEqual({ reason: 'task', note: '' })
    expect(find(m, 'unit')!.disabled).toBeNull()
    expect(blockers(m)).toEqual([
      { reason: 'docker', count: 1 },
      { reason: 'task', count: 1 },
    ])
    expect(Object.keys(REASONS)).toEqual(['user', 'docker', 'services', 'host', 'task', 'template', 'condition', 'checkout', 'deployment', 'secrets', 'variables'])
  })

  it('saves an unrelated edit byte for byte around the disabled steps', () => {
    const m = fromApi(file)!
    expect(renderYaml(m, src)).toBe(src)
    const edited = setField(m, 'unit', 'timeout', '5m')
    const out = renderYaml(edited, src)
    expect(out).toContain('    disabled:\n      reason: docker\n      note: starts Redis with docker run\n')
    expect(out).toContain('    disabled: {reason: task}\n')
    expect(out).not.toMatch(/image: ["']{2}/)
  })

  it('writes disabled as a map and no image for a step that has none', () => {
    const lines = toYaml(fromApi(file)!)
    const at = lines.indexOf('  - name: publish')
    expect(lines.slice(at + 1)).toEqual(['    needs: [integration]', '    disabled:', '      reason: task'])
    expect(lines).toContain('      note: starts Redis with docker run')
  })

  it('disables a step by hand with reason user and enables it again, but never enables an imported block', () => {
    const m = fromApi(file)!
    const off = toggleDisabled(m, 'unit')
    expect(find(off, 'unit')!.disabled).toEqual({ reason: 'user', note: '' })
    expect(diffSummary(m, off)).toContain('unit disabled')
    expect(renderYaml(off, src)).toContain('    run: go test ./...\n    disabled:\n      reason: user\n')
    const on = toggleDisabled(off, 'unit')
    expect(find(on, 'unit')!.disabled).toBeNull()
    expect(renderYaml(on, src)).toBe(src)
    expect(toggleDisabled(m, 'integration')).toBe(m)
  })

  it('keeps a hand-disabled step with no image disabled until it has one, never writing an empty image', () => {
    const m = fromApi(file)!
    const noImage = setField(toggleDisabled(m, 'unit'), 'unit', 'image', '')
    expect(toggleDisabled(noImage, 'unit')).toBe(noImage)
    expect(renderYaml(noImage, src)).not.toMatch(/image: ["']{2}/)
    const withImage = setField(noImage, 'unit', 'image', 'golang:1.28')
    expect(find(toggleDisabled(withImage, 'unit'), 'unit')!.disabled).toBeNull()
    const noRun = setField(withImage, 'unit', 'run', [])
    expect(toggleDisabled(noRun, 'unit')).toBe(noRun)
  })
})
