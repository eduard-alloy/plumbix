import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { NOW, repos } from '../test/fixtures'
import { install, type FakeDB } from '../test/server'

let db: FakeDB

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

async function open(path = '/pipelines') {
  window.history.replaceState(null, '', path)
  render(<App />)
  await screen.findByRole('button', { name: 'build' })
}

const card = (id: string) => screen.getByRole('button', { name: id })

describe('Pipelines page', () => {
  it('loads the first repository\'s pipeline as stage bands with cards, lines and chips', async () => {
    await open()
    expect(screen.getByRole('tab', { name: 'plumbix' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByText('.plumbix/pipeline.yaml at main · 4f81c2a')).toBeTruthy()
    const names = screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))
    expect(names).toEqual(['Stage 1: build', 'Stage 2: checks', 'Stage 3: deploy-check', 'Stage V: validators'])
    const checks = screen.getByRole('region', { name: 'Stage 2: checks' })
    expect(checks.textContent).toContain('2 steps · starts after stage 1')
    expect(within(checks).getByRole('button', { name: 'test' }).textContent).toContain('1m 58s')
    expect(within(checks).getByRole('button', { name: 'contract' }).textContent).toContain('not run yet')
    expect(within(checks).getByRole('button', { name: 'contract' }).textContent).toContain('after build')
    expect(screen.getByRole('button', { name: 'Add after' }).hasAttribute('disabled')).toBe(true)
    expect(screen.queryByRole('complementary')).toBeNull()
  })

  it('switches repositories through the segmented control and the URL', async () => {
    await open()
    fireEvent.click(screen.getByRole('tab', { name: 'ledger-api' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'build' })).toBeNull())
    expect(screen.getByRole('button', { name: 'test' }).title).toContain('node:24')
    expect(window.location.pathname).toBe(`/pipelines/${repos[1].id}`)
  })

  it('selects a card, edits it in the inspector, and the YAML view marks the change', async () => {
    await open()
    fireEvent.click(card('test'))
    const panel = await screen.findByRole('complementary')
    expect(panel.getAttribute('aria-label')).toBe('Step')
    expect((within(panel).getByLabelText('Name') as HTMLInputElement).value).toBe('test')
    expect(within(panel).getByLabelText('Retries').textContent).toBe('1')
    fireEvent.click(within(panel).getByRole('button', { name: 'More retries' }))
    expect(within(panel).getByLabelText('Retries').textContent).toBe('2')
    expect(screen.getByText('1 edit')).toBeTruthy()
    expect(screen.getByText('test: retries 2')).toBeTruthy()
    // Needs offers only cycle-free steps.
    const add = within(panel).getByLabelText('Add need') as HTMLSelectElement
    expect(Array.from(add.options).map((o) => o.value)).toEqual(['', 'contract'])
    fireEvent.click(within(panel).getByRole('switch', { name: 'Allowed to fail' }))
    expect(screen.getByText('test: retries 2, allowed to fail')).toBeTruthy()
    // Caches kept between runs (WU-029): a path is added and written as cache:.
    fireEvent.click(within(panel).getByRole('button', { name: '+ cache path' }))
    fireEvent.change(within(panel).getByLabelText('Cache path 1'), { target: { value: '/root/.cache/go-build' } })
    expect(screen.getByText('test: retries 2, allowed to fail, caches /root/.cache/go-build')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'YAML' }))
    const yaml = screen.getByRole('region', { name: 'Pipeline YAML' })
    const added = Array.from(yaml.querySelectorAll('.yl.add')).map((l) => l.textContent)
    expect(added.some((t) => t!.includes('retries: 2'))).toBe(true)
    expect(added.some((t) => t!.includes('allow_failure: true'))).toBe(true)
    expect(added.some((t) => t!.includes('cache: [/root/.cache/go-build]'))).toBe(true)
    expect(yaml.textContent).toContain('Read-only here.')
  })

  it('says what a validator step runs: the image is the validator, no commands', async () => {
    await open()
    fireEvent.click(card('validate'))
    const panel = await screen.findByRole('complementary')
    expect(panel.textContent).toContain("its image's entrypoint is the validator")
    expect(panel.textContent).toContain('/plumbix/out/verdict.json')
    expect(panel.textContent).toContain('plumbix-ai-validator')
  })

  it('lets a validator step pick its reviewer profile and reasoning effort, written to the file (WU-034)', async () => {
    await open()
    fireEvent.click(card('validate'))
    const panel = await screen.findByRole('complementary')
    const profile = within(panel).getByLabelText('Reviewer profile') as HTMLSelectElement
    await waitFor(() => expect(Array.from(profile.options).map((o) => o.value)).toEqual(['', 'claude-api', 'claude-code']))
    expect(profile.options[1].textContent).toContain('Anthropic API')
    fireEvent.change(profile, { target: { value: 'claude-api' } })
    fireEvent.change(within(panel).getByLabelText('Reasoning effort'), { target: { value: 'low' } })
    expect(screen.getByText('validate: reviewer claude-api, effort low')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'YAML' }))
    const yaml = screen.getByRole('region', { name: 'Pipeline YAML' })
    const added = Array.from(yaml.querySelectorAll('.yl.add')).map((l) => l.textContent)
    expect(added.some((t) => t!.includes('reviewer: claude-api'))).toBe(true)
    expect(added.some((t) => t!.includes('effort: low'))).toBe(true)
  })

  it('renames a step and rewires its dependents; Escape clears the selection', async () => {
    await open()
    fireEvent.click(card('build'))
    const panel = await screen.findByRole('complementary')
    const name = within(panel).getByLabelText('Name')
    fireEvent.change(name, { target: { value: 'compile' } })
    fireEvent.keyDown(name, { key: 'Enter' })
    expect(screen.getByRole('button', { name: 'compile' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'YAML' }))
    expect(screen.getByRole('region', { name: 'Pipeline YAML' }).textContent).toContain('needs: [compile]')
    fireEvent.click(screen.getByRole('button', { name: 'Visual' }))
    expect(screen.getByRole('complementary')).toBeTruthy()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('complementary')).toBeNull()
  })

  it('adds after, merges and groups through the toolbar', async () => {
    await open()
    fireEvent.click(card('test'))
    fireEvent.click(screen.getByRole('button', { name: 'Add after' }))
    const step = await screen.findByRole('button', { name: 'step' })
    expect(step.getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByText('added step after test')).toBeTruthy()
    fireEvent.click(card('contract'), { shiftKey: true })
    expect(screen.getByText('2 steps selected')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Merge into one step' }))
    expect(screen.getByRole('button', { name: 'contract-step' })).toBeTruthy()
    fireEvent.click(card('build'))
    fireEvent.click(card('deploy-check'), { metaKey: true })
    fireEvent.change(screen.getByLabelText('Stage name'), { target: { value: 'edges' } })
    fireEvent.click(screen.getByRole('button', { name: 'Group as stage' }))
    expect(screen.getByRole('region', { name: 'Stage 1: edges' })).toBeTruthy()
  })

  it('saves as a change: posts the YAML on a new branch and shows the opened change', async () => {
    await open()
    fireEvent.click(card('build'))
    const panel = await screen.findByRole('complementary')
    fireEvent.change(within(panel).getByLabelText('Image'), { target: { value: 'golang:1.28' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save as change' }))
    const branch = screen.getByLabelText('Branch') as HTMLInputElement
    expect(branch.value).toBe('pipeline/build-image-golang-1-28')
    expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Pipeline: build: image golang:1.28')
    fireEvent.change(branch, { target: { value: 'pipeline/go-1-28' } })
    fireEvent.click(screen.getByRole('button', { name: 'Push branch and open change' }))
    const status = await screen.findByRole('status')
    expect(status.textContent).toContain('Change opened.')
    expect(status.textContent).toContain('c_pipeline0001')
    expect(status.textContent).toContain('pipeline/go-1-28')
    expect(db.proposals).toHaveLength(1)
    expect(db.proposals[0]).toMatchObject({ repo: repos[0].id, branch: 'pipeline/go-1-28', target_branch: 'main' })
    expect(db.proposals[0].content).toContain('    image: golang:1.28\n')
    expect(db.proposals[0].content).toContain('  - name: validate\n    image: plumbix-reference-validator:dev\n    validator: true\n    needs: [build, test, contract, deploy-check]')
    expect(screen.queryByText('1 edit')).toBeNull()
    expect(screen.getByRole('tab', { name: 'plumbix' }).textContent).toBe('plumbix')
    fireEvent.click(within(status).getByRole('button', { name: 'Open change' }))
    expect(window.location.pathname).toBe('/changes/c_pipeline0001')
  })

  it('explains a refused push and keeps the edits', async () => {
    db.proposeResult = 'push_denied'
    await open()
    fireEvent.click(card('build'))
    fireEvent.click(within(await screen.findByRole('complementary')).getByRole('button', { name: 'More retries' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save as change' }))
    fireEvent.click(screen.getByRole('button', { name: 'Push branch and open change' }))
    await waitFor(() => expect(screen.getByText(/The host refused the push/)).toBeTruthy())
    expect(screen.getByText('1 edit')).toBeTruthy()
    expect(db.proposals).toHaveLength(0)
  })

  it('shows parse problems instead of an editor when the file does not parse', async () => {
    db.pipelines[repos[0].id] = { ...db.pipelines[repos[0].id], content: 'version: 2\n', pipeline: null, problems: [{ path: 'version', line: 1, message: 'must be 1, got 2' }] }
    window.history.replaceState(null, '', '/pipelines')
    render(<App />)
    await screen.findByText('This pipeline file does not parse')
    expect(screen.getByText('line 1: version: must be 1, got 2')).toBeTruthy()
    expect(screen.queryByRole('toolbar')).toBeNull()
  })
})
