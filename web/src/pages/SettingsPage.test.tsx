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

  it('lists both families: the API profile with its key, the Claude Code profile with tools and the steps it answers', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    const apiCard = await screen.findByLabelText('Reviewer claude-api')
    expect(apiCard.textContent).toContain('default for pipeline validators')
    expect(apiCard.textContent).toContain('Anthropic API')
    expect(apiCard.textContent).toContain('billed per token')
    expect(within(apiCard).queryByLabelText('Read only')).toBeNull()
    // Its key is the known Anthropic key, set from the card and never shown.
    const anthropicRow = within(apiCard).getByText('ANTHROPIC_API_KEY', { selector: '.secret .key' }).closest('.secret') as HTMLElement
    expect(anthropicRow.textContent).toContain('Not set')
    fireEvent.click(within(anthropicRow).getByRole('button', { name: 'Set' }))
    fireEvent.change(within(anthropicRow).getByLabelText('ANTHROPIC_API_KEY value'), { target: { value: 'sk-ant-secret' } })
    fireEvent.click(within(anthropicRow).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(db.secrets.ANTHROPIC_API_KEY).toBe('sk-ant-secret'))
    expect(document.body.textContent).not.toContain('sk-ant-secret')
    expect(screen.getAllByText('ANTHROPIC_API_KEY', { selector: '.secret .key' })).toHaveLength(1)

    // The Claude Code profile: subscription, no key row, tools, and the external steps naming none.
    const codeCard = screen.getByLabelText('Reviewer claude-code')
    expect(codeCard.textContent).toContain('default for external reviews')
    expect(codeCard.textContent).toContain('subscription quota')
    expect(codeCard.querySelector('.secret')).toBeNull()
    expect(within(codeCard).queryByLabelText('Key secret')).toBeNull()
    await waitFor(() => expect(screen.getByLabelText('Reviewer claude-code').textContent).toContain('Judges ledger-api: ai-review'))
    expect(screen.getByLabelText('Reviewer claude-code').textContent).not.toContain('ai-grok')
    // The API card has a reasoning effort; the Claude Code card does not (WU-034).
    expect(within(codeCard).queryByLabelText('Reasoning effort')).toBeNull()
    fireEvent.change(within(apiCard).getByLabelText('Reasoning effort'), { target: { value: 'low' } })
    fireEvent.click(within(apiCard).getByRole('button', { name: 'Save reviewer' }))
    await waitFor(() => expect(db.profiles.find((p) => p.name === 'claude-api')?.effort).toBe('low'))

    const full = within(codeCard).getByLabelText('Full, including commands') as HTMLInputElement
    expect((within(codeCard).getByLabelText('Read only') as HTMLInputElement).checked).toBe(true)
    fireEvent.click(full)
    fireEvent.change(within(codeCard).getByLabelText('Model'), { target: { value: 'claude-sonnet-5-5' } })
    fireEvent.click(within(codeCard).getByRole('button', { name: 'Save reviewer' }))
    await waitFor(() => expect(db.profiles.find((p) => p.name === 'claude-code')?.tools).toBe('full'))
    expect(db.profiles.find((p) => p.name === 'claude-code')?.model).toBe('claude-sonnet-5-5')
    await waitFor(() => expect(screen.getByLabelText('Reviewer claude-code').textContent).toContain('Saved'))

    // A refusal is shown inline, naming the field.
    fireEvent.change(within(screen.getByLabelText('Reviewer claude-api')).getByLabelText('Model'), { target: { value: 'Not A Model' } })
    fireEvent.click(within(screen.getByLabelText('Reviewer claude-api')).getByRole('button', { name: 'Save reviewer' }))
    await screen.findByText(/model lowercase letters, digits, dots and dashes only/)
  })

  it('adds an OpenAI-compatible reviewer with its own key, makes it the default, and removes it', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    await screen.findByLabelText('Reviewer claude-api')
    const add = within(screen.getByLabelText('Add a reviewer'))
    fireEvent.change(add.getByLabelText('Name'), { target: { value: 'grok-4' } })
    expect((add.getByLabelText('Kind') as HTMLSelectElement).value).toBe('openai')
    expect(document.body.textContent).toContain('https://api.x.ai/v1')
    fireEvent.change(add.getByLabelText('Base URL'), { target: { value: 'https://api.x.ai/v1' } })
    fireEvent.change(add.getByLabelText('Model'), { target: { value: 'grok-4' } })
    fireEvent.click(add.getByRole('button', { name: 'Add reviewer' }))
    const card = await screen.findByLabelText('Reviewer grok-4')
    expect(db.profiles.find((p) => p.name === 'grok-4')).toMatchObject({ kind: 'openai', base_url: 'https://api.x.ai/v1', model: 'grok-4', tools: 'read-only', key_secret: 'PLUMBIX_REVIEWER_GROK_4_KEY' })
    expect(card.textContent).toContain('OpenAI-compatible API')
    expect(card.textContent).toContain('billed by its vendor')
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
    // The api default moves; the external one stays.
    fireEvent.click(within(screen.getByLabelText('Reviewer grok-4')).getByRole('button', { name: 'Make default' }))
    await waitFor(() => expect(db.settings.defaults.api).toBe('grok-4'))
    expect(db.settings.defaults.external).toBe('claude-code')
    await waitFor(() => expect(screen.getByLabelText('Reviewer grok-4').textContent).toContain('default for pipeline validators'))
    expect(screen.getByLabelText('Reviewer claude-api').querySelector('.default-tag')).toBeNull()
    expect(screen.getByLabelText('Reviewer claude-code').textContent).toContain('default for external reviews')
    // Removal clears its family's default.
    fireEvent.click(within(screen.getByLabelText('Reviewer grok-4')).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(screen.queryByLabelText('Reviewer grok-4')).toBeNull())
    expect(db.settings.defaults.api).toBe('')
    expect(db.settings.defaults.external).toBe('claude-code')
  })

  it('adds a Claude Code in-the-pipeline reviewer: a token secret and tools, no effort or base URL (WU-035)', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    await screen.findByLabelText('Reviewer claude-api')
    const add = within(screen.getByLabelText('Add a reviewer'))
    fireEvent.change(add.getByLabelText('Name'), { target: { value: 'cc' } })
    fireEvent.change(add.getByLabelText('Kind'), { target: { value: 'claude-code-image' } })
    expect(add.queryByLabelText('Base URL')).toBeNull()
    expect(screen.getByLabelText('Add a reviewer').textContent).toContain('claude setup-token')
    fireEvent.click(add.getByRole('button', { name: 'Add reviewer' }))
    const card = await screen.findByLabelText('Reviewer cc')
    expect(db.profiles.find((p) => p.name === 'cc')).toMatchObject({ kind: 'claude-code-image', base_url: '', tools: 'read-only', effort: '', key_secret: 'PLUMBIX_REVIEWER_CC_KEY' })
    expect(card.textContent).toContain('Claude Code (in the pipeline)')
    expect(card.textContent).toContain('plumbix-claude-code-validator')
    // Tools and the token secret, like the loop's profile plus a key; no effort.
    expect((within(card).getByLabelText('Read only') as HTMLInputElement).checked).toBe(true)
    // The tools switch says what full means for this kind: commands beside the token.
    expect(card.textContent).toContain('a container that holds your token')
    expect(screen.getByLabelText('Reviewer claude-code').textContent).not.toContain('holds your token')
    expect(within(card).queryByLabelText('Reasoning effort')).toBeNull()
    expect(within(card).getByText('PLUMBIX_REVIEWER_CC_KEY', { selector: '.secret .key' })).toBeTruthy()
    fireEvent.click(within(card).getByLabelText('Full, including commands'))
    fireEvent.click(within(card).getByRole('button', { name: 'Save reviewer' }))
    await waitFor(() => expect(db.profiles.find((p) => p.name === 'cc')?.tools).toBe('full'))
    expect(db.profiles.find((p) => p.name === 'cc')?.effort).toBe('')
    // The editor's profile list names the kind.
    expect(db.profiles.find((p) => p.name === 'cc')?.kind).toBe('claude-code-image')
  })

  it('refuses a profile the server rejects, naming the field', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    await screen.findByLabelText('Reviewer claude-api')
    const add = within(screen.getByLabelText('Add a reviewer'))
    fireEvent.change(add.getByLabelText('Name'), { target: { value: 'local' } })
    fireEvent.change(add.getByLabelText('Base URL'), { target: { value: 'localhost:11434' } })
    fireEvent.click(add.getByRole('button', { name: 'Add reviewer' }))
    await screen.findByText(/base_url "localhost:11434" must be an http\(s\) URL/)
  })

  it('shows which external reviewers the server has heard from, or how to start one', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Settings' })
    expect(document.body.textContent).toContain('answered outside the pipeline by plumbix review --watch')
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
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
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
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
    const ledger = screen.getByRole('listitem', { name: 'Repository ledger-api' })
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
