import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { NOW } from '../test/fixtures'
import { install, type FakeDB } from '../test/server'

let db: FakeDB

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  db = install()
  window.history.replaceState(null, '', '/settings')
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function row(key: string): HTMLElement {
  return screen.getByText(key, { selector: '.secret .key' }).closest('.secret') as HTMLElement
}

describe('Settings page', () => {
  it('lists the known secrets with their state and sets one without ever showing it', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    const gh = row('PLUMBIX_GITHUB_TOKEN')
    expect(gh.textContent).toContain('Not set')
    expect(gh.textContent).toContain('Default token for every GitHub repository')
    fireEvent.click(within(gh).getByRole('button', { name: 'Set' }))
    const input = within(gh).getByLabelText('PLUMBIX_GITHUB_TOKEN value') as HTMLInputElement
    expect(input.type).toBe('password')
    fireEvent.change(input, { target: { value: 'github_pat_secret' } })
    fireEvent.click(within(gh).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(row('PLUMBIX_GITHUB_TOKEN').textContent).toContain('Set'))
    expect(db.secrets.PLUMBIX_GITHUB_TOKEN).toBe('github_pat_secret')
    expect(document.body.textContent).not.toContain('github_pat_secret')
    expect(row('PLUMBIX_GITHUB_TOKEN').textContent).toContain('just now')
    expect(within(row('PLUMBIX_GITHUB_TOKEN')).queryByLabelText('PLUMBIX_GITHUB_TOKEN value')).toBeNull()
  })

  it('checks a host token and removes a secret', async () => {
    db.secrets.PLUMBIX_AZURE_DEVOPS_TOKEN = 'pat'
    db.secretChecks.PLUMBIX_AZURE_DEVOPS_TOKEN = { ok: true, expires_at: '2027-01-01T00:00:00Z', message: 'token accepted; expires 2027-01-01' }
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    const ado = row('PLUMBIX_AZURE_DEVOPS_TOKEN')
    fireEvent.click(within(ado).getByRole('button', { name: 'Check' }))
    await waitFor(() => expect(row('PLUMBIX_AZURE_DEVOPS_TOKEN').textContent).toContain('token accepted; expires 2027-01-01'))
    expect(within(row('ANTHROPIC_API_KEY')).queryByRole('button', { name: 'Check' })).toBeNull()
    fireEvent.click(within(row('PLUMBIX_AZURE_DEVOPS_TOKEN')).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(row('PLUMBIX_AZURE_DEVOPS_TOKEN').textContent).toContain('Not set'))
    expect(db.secrets.PLUMBIX_AZURE_DEVOPS_TOKEN).toBeUndefined()
  })



  it('edits the global guidance and adds a custom secret', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    fireEvent.change(screen.getByLabelText('Review guidance'), { target: { value: 'Prefer small diffs.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save guidance' }))
    await waitFor(() => expect(db.settings.guidance).toBe('Prefer small diffs.'))
    await screen.findByText('Saved')

    fireEvent.change(screen.getByLabelText('New secret name'), { target: { value: 'MY_DEPLOY_KEY' } })
    fireEvent.change(screen.getByLabelText('New secret value'), { target: { value: 'v' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add secret' }))
    await waitFor(() => expect(db.secrets.MY_DEPLOY_KEY).toBe('v'))
    await waitFor(() => expect(row('MY_DEPLOY_KEY').textContent).toContain('Set'))
    expect(row('MY_DEPLOY_KEY').textContent).toContain('Named by a pipeline step')
  })

  it('lists the reviewer profiles with their key, which steps they judge, and lets the operator change tools and model', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    const card = await screen.findByLabelText('Reviewer claude')
    expect(card.textContent).toContain('default')
    expect(card.textContent).toContain('Anthropic')
    // The default judges the steps naming no reviewer: ledger-api's external ai-review.
    await waitFor(() => expect(card.textContent).toContain('Judges ledger-api: ai-review'))
    expect(card.textContent).not.toContain('ai-grok')
    // Its key is the known Anthropic key, set from the card and never shown.
    expect(within(card).getByText('ANTHROPIC_API_KEY', { selector: '.secret .key' })).toBeTruthy()
    expect(within(card).getByText('ANTHROPIC_API_KEY', { selector: '.secret .key' }).closest('.secret')!.textContent).toContain('Not set')
    const anthropicRow = within(card).getByText('ANTHROPIC_API_KEY', { selector: '.secret .key' }).closest('.secret') as HTMLElement
    fireEvent.click(within(anthropicRow).getByRole('button', { name: 'Set' }))
    fireEvent.change(within(anthropicRow).getByLabelText('ANTHROPIC_API_KEY value'), { target: { value: 'sk-ant-secret' } })
    fireEvent.click(within(anthropicRow).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(db.secrets.ANTHROPIC_API_KEY).toBe('sk-ant-secret'))
    expect(document.body.textContent).not.toContain('sk-ant-secret')
    // The Anthropic key is not repeated under Other secrets.
    expect(screen.getAllByText('ANTHROPIC_API_KEY', { selector: '.secret .key' })).toHaveLength(1)

    // Tools and model.
    const full = within(card).getByLabelText('Full, including commands') as HTMLInputElement
    expect((within(card).getByLabelText('Read only') as HTMLInputElement).checked).toBe(true)
    fireEvent.click(full)
    fireEvent.change(within(card).getByLabelText('Model'), { target: { value: 'claude-sonnet-5-5' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save reviewer' }))
    await waitFor(() => expect(db.profiles.find((p) => p.name === 'claude')?.tools).toBe('full'))
    expect(db.profiles.find((p) => p.name === 'claude')?.model).toBe('claude-sonnet-5-5')
    await waitFor(() => expect(screen.getByLabelText('Reviewer claude').textContent).toContain('Saved'))

    // A refusal is shown inline, naming the field.
    fireEvent.change(within(screen.getByLabelText('Reviewer claude')).getByLabelText('Model'), { target: { value: 'Not A Model' } })
    fireEvent.click(within(screen.getByLabelText('Reviewer claude')).getByRole('button', { name: 'Save reviewer' }))
    await screen.findByText(/model lowercase letters, digits, dots and dashes only/)
  })

  it('adds an OpenAI-compatible reviewer with its own key, makes it the default, and removes it', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    await screen.findByLabelText('Reviewer claude')
    const add = within(screen.getByLabelText('Add a reviewer'))
    fireEvent.change(add.getByLabelText('Name'), { target: { value: 'grok-4' } })
    expect((add.getByLabelText('Kind') as HTMLSelectElement).value).toBe('openai')
    expect(document.body.textContent).toContain('https://api.x.ai/v1')
    fireEvent.change(add.getByLabelText('Base URL'), { target: { value: 'https://api.x.ai/v1' } })
    fireEvent.change(add.getByLabelText('Model'), { target: { value: 'grok-4' } })
    fireEvent.click(add.getByRole('button', { name: 'Add reviewer' }))
    const card = await screen.findByLabelText('Reviewer grok-4')
    expect(db.profiles.find((p) => p.name === 'grok-4')).toMatchObject({ kind: 'openai', base_url: 'https://api.x.ai/v1', model: 'grok-4', tools: 'read-only', key_secret: 'PLUMBIX_REVIEWER_GROK_4_KEY' })
    expect(card.textContent).toContain('OpenAI-compatible')
    // ledger-api's ai-grok step names it.
    await waitFor(() => expect(screen.getByLabelText('Reviewer grok-4').textContent).toContain('Judges ledger-api: ai-grok'))
    // Its key.
    const keyRow = within(card).getByText('PLUMBIX_REVIEWER_GROK_4_KEY', { selector: '.secret .key' }).closest('.secret') as HTMLElement
    expect(keyRow.textContent).toContain('Not set')
    fireEvent.click(within(keyRow).getByRole('button', { name: 'Set' }))
    fireEvent.change(within(keyRow).getByLabelText('PLUMBIX_REVIEWER_GROK_4_KEY value'), { target: { value: 'xai-secret' } })
    fireEvent.click(within(keyRow).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(db.secrets.PLUMBIX_REVIEWER_GROK_4_KEY).toBe('xai-secret'))
    expect(document.body.textContent).not.toContain('xai-secret')
    await waitFor(() => expect(within(screen.getByLabelText('Reviewer grok-4')).getByText('PLUMBIX_REVIEWER_GROK_4_KEY', { selector: '.secret .key' }).closest('.secret')!.textContent).toContain('Set'))
    // Default moves.
    fireEvent.click(within(screen.getByLabelText('Reviewer grok-4')).getByRole('button', { name: 'Make default' }))
    await waitFor(() => expect(db.settings.default_reviewer).toBe('grok-4'))
    await waitFor(() => expect(screen.getByLabelText('Reviewer grok-4').textContent).toContain('default'))
    expect(within(screen.getByLabelText('Reviewer claude')).queryByText('default', { selector: '.default-tag' })).toBeNull()
    // Removal clears the default.
    fireEvent.click(within(screen.getByLabelText('Reviewer grok-4')).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(screen.queryByLabelText('Reviewer grok-4')).toBeNull())
    expect(db.settings.default_reviewer).toBe('')
  })

  it('refuses a profile the server rejects, naming the field', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    await screen.findByLabelText('Reviewer claude')
    const add = within(screen.getByLabelText('Add a reviewer'))
    fireEvent.change(add.getByLabelText('Name'), { target: { value: 'local' } })
    fireEvent.change(add.getByLabelText('Base URL'), { target: { value: 'localhost:11434' } })
    fireEvent.click(add.getByRole('button', { name: 'Add reviewer' }))
    await screen.findByText(/base_url "localhost:11434" must be an http\(s\) URL/)
  })

  it('shows which external reviewers the server has heard from, or how to start one', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    expect(document.body.textContent).toContain('answered by plumbix review --watch running Claude Code')
    const section = (await screen.findByRole('heading', { name: 'External reviewers' })).parentElement!
    await waitFor(() => expect(section.textContent).toContain('No reviewer has polled since the server started'))
    expect(section.textContent).toContain('plumbix review install')
    db.reviewers = [{ name: 'eduard@mac', last_seen: new Date(NOW.getTime() - 20_000).toISOString() }, { name: 'ci-box', repo_id: 'r_plumbix00001', last_seen: new Date(NOW.getTime() - 5 * 60_000).toISOString() }]
    fireEvent.click(within(section).getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(section.textContent).toContain('eduard@mac'))
    expect(section.textContent).toContain('ci-box')
    expect(section.textContent).toContain('plumbix only')
    expect(section.textContent).toContain('just now')
  })



  it('edits per-repository guidance from the repositories page', async () => {
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    const rows = await screen.findAllByRole('row')
    const plumbix = rows.find((r) => r.textContent?.includes('https://github.com/plumbix/plumbix'))!
    fireEvent.click(within(plumbix).getByRole('button', { name: 'Guidance' }))
    const area = await screen.findByLabelText('Guidance for plumbix')
    fireEvent.change(area, { target: { value: 'Go code needs tests.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(db.repoGuidance.r_plumbix00001).toBe('Go code needs tests.'))
    await screen.findByText('Saved')
  })

  it('gives a repository its own token and checks it', async () => {
    db.repoTokens.r_ledgerapi001 = 'already-set'
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    const rows = await screen.findAllByRole('row')
    const plumbix = rows.find((r) => r.textContent?.includes('https://github.com/plumbix/plumbix'))!
    const ledger = rows.find((r) => r.textContent?.includes('ledger-api'))!
    await waitFor(() => expect(ledger.textContent).toContain('Own token'))
    expect(plumbix.textContent).toContain('Host token')
    fireEvent.click(within(plumbix).getByRole('button', { name: 'Token' }))
    const panel = await screen.findByLabelText('Token for plumbix')
    expect(panel.textContent).toContain('Uses the GitHub token from Settings')
    fireEvent.click(within(panel).getByRole('button', { name: 'Set' }))
    const input = within(panel).getByLabelText('Token value for plumbix') as HTMLInputElement
    expect(input.type).toBe('password')
    fireEvent.change(input, { target: { value: 'github_pat_repo_only' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(db.repoTokens.r_plumbix00001).toBe('github_pat_repo_only'))
    await waitFor(() => expect(screen.getByLabelText('Token for plumbix').textContent).toContain('Own token'))
    expect(document.body.textContent).not.toContain('github_pat_repo_only')
    fireEvent.click(within(screen.getByLabelText('Token for plumbix')).getByRole('button', { name: 'Check' }))
    await screen.findByText(/fetched 3 branches with the repository's own token/)
    fireEvent.click(within(screen.getByLabelText('Token for plumbix')).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(db.repoTokens.r_plumbix00001).toBeUndefined())
    await waitFor(() => expect(screen.getByLabelText('Token for plumbix').textContent).toContain('Uses the GitHub token from Settings'))
  })

  it('explains that the host token is a default and fine-grained tokens are per repository', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    expect(row('PLUMBIX_GITHUB_TOKEN').textContent).toContain('Default token for every GitHub repository')
    expect(row('PLUMBIX_GITHUB_TOKEN').textContent).toContain('Repositories page')
  })

  it('has a Settings entry in the sidebar', async () => {
    window.history.replaceState(null, '', '/')
    render(<App />)
    await screen.findByRole('list', { name: 'Changes' })
    fireEvent.click(screen.getByRole('button', { name: 'Settings' }))
    await screen.findByRole('heading', { name: 'Settings' })
    expect(window.location.pathname).toBe('/settings')
  })
})
