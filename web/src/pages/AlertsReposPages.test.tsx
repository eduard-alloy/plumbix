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
})

afterEach(() => {
  cleanup() // unmount before the stubs go, or a late load would hit the real globals
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('Alerts page', () => {
  it('labels a no_reviewer alert and says how to start a loop', async () => {
    db.alerts.push({ id: 'al_noreviewer001', kind: 'no_reviewer', key: 'r_plumbix00001', repo_id: 'r_plumbix00001', message: 'Change c_x is waiting for an external review of step ai-review and no reviewer has polled in the last 2m0s. Run plumbix review --watch on a machine with Claude Code, or plumbix review install to keep one running.', details: { change_id: 'c_x', step: 'ai-review' }, created_at: NOW.toISOString() })
    window.history.replaceState(null, '', '/alerts')
    render(<App />)
    await screen.findByText('No reviewer is answering external reviews')
    expect(document.body.textContent).toContain('plumbix review install')
  })

  it('labels a pipeline_unapproved alert and says how to approve the file (WU-051)', async () => {
    db.alerts.push({ id: 'al_pipeunappr01', kind: 'pipeline_unapproved', key: 'r_plumbix00001:.plumbix/pipelines/deploy.yaml', repo_id: 'r_plumbix00001', message: 'A run of .plumbix/pipelines/deploy.yaml on main at 3b7f2a9 got no secrets: .plumbix/pipelines/deploy.yaml at this commit is not a version a person approved; bring it in through a Plumbix change.', details: { file: '.plumbix/pipelines/deploy.yaml', branch: 'main' }, created_at: NOW.toISOString() })
    window.history.replaceState(null, '', '/alerts')
    render(<App />)
    await screen.findByText('Pipeline file not approved: runs get no secrets')
    expect(document.body.textContent).toContain('submit the file as a change')
  })

  it('lists open alerts with their commits, resolves one, and shows resolved ones behind a toggle', async () => {
    window.history.replaceState(null, '', '/alerts')
    render(<App />)
    const item = await screen.findByText('Commits without a verdict')
    const row = item.closest('.alert')!
    expect(row.textContent).toContain('bypass_commit')
    expect(row.textContent).toContain('plumbix')
    expect(row.textContent).toContain('commit 3b7f2a9d1c0e by eduard reached main without a verdict')
    const commit = within(row as HTMLElement).getByText('3b7f2a9d1c0e').closest('.commit')!
    expect(commit.textContent).toContain('eduard')
    expect(commit.textContent).toContain('hotfix: raise runner timeout to 15m')
    expect(row.textContent).toContain('If this was intended, resolve the alert.')
    expect(screen.getByText('1 open')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Show resolved (0)' })).toBeTruthy()

    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'Resolve' }))
    await screen.findByText(/No open alerts/)
    expect(screen.getByText('0 open')).toBeTruthy()
    expect(db.alerts[0].resolved_at).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Show resolved (1)' }))
    await screen.findByText('Resolved', { selector: '.resolved-label' })
    const resolved = screen.getByText('Commits without a verdict').closest('.alert')!
    expect(resolved.textContent).toContain('Resolved')
    expect(within(resolved as HTMLElement).queryByRole('button', { name: 'Resolve' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Hide resolved (1)' }))
    expect(screen.queryByText('Commits without a verdict')).toBeNull()
  })

  it('labels token alerts by their message', async () => {
    db.alerts = [
      { id: 'al_tok000000001', kind: 'token_expiry', key: 'github', message: 'github token expires on 2026-10-22', created_at: new Date(NOW.getTime() - 3 * 3600000).toISOString() },
      { id: 'al_tok000000002', kind: 'token_expiry', key: 'azuredevops', message: 'azuredevops token check failed: 401', created_at: new Date(NOW.getTime() - 60000).toISOString() },
    ]
    window.history.replaceState(null, '', '/alerts')
    render(<App />)
    await screen.findByText('Token expires soon')
    expect(screen.getByText('Token check failed')).toBeTruthy()
    expect(screen.getByText('2 open')).toBeTruthy()
  })
})

describe('Repositories page', () => {
  it('lists repositories with their change counts', async () => {
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    // One two-line card per repository (WU-040): name, host, changes and merge on top; URL, protected branches and token below.
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
    const [top, bottom] = Array.from(plumbix.querySelectorAll('.repo-line'))
    expect(top.textContent).toContain('plumbix')
    expect(top.textContent).toContain('GitHub')
    expect(top.textContent).toContain('5 changes')
    expect(top.textContent).toContain('squash')
    // A repository gated through pull requests says so, and who merges (WU-055).
    expect(top.textContent).toContain('Pull requests · Plumbix merges')
    expect(bottom.textContent).toContain('https://github.com/plumbix/plumbix')
    expect(bottom.textContent).toContain('Protected: main')
    expect(bottom.textContent).toContain('Host token')
    const ledger = screen.getByRole('listitem', { name: 'Repository ledger-api' })
    expect(ledger.textContent).toContain('Azure DevOps')
    expect(ledger.textContent).not.toContain('Pull requests')
    expect(ledger.textContent).toContain('2 changes')
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.getByText(/Import a repository from a local folder or a remote/)).toBeTruthy()
    expect(screen.getByText('2', { selector: '.page-header .count' })).toBeTruthy()
  })

  it('shows the GitHub panel with the gating switches and the live checks (WU-061)', async () => {
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
    // Only GitHub repositories have the panel.
    expect(within(screen.getByRole('listitem', { name: 'Repository ledger-api' })).queryByRole('button', { name: 'GitHub' })).toBeNull()
    fireEvent.click(within(plumbix).getByRole('button', { name: 'GitHub' }))
    const panel = within(plumbix).getByRole('group', { name: 'GitHub for plumbix' })
    const gated = within(panel).getByRole('checkbox', { name: 'Pull requests gated for plumbix' }) as HTMLInputElement
    const merger = within(panel).getByRole('combobox', { name: 'Merged by for plumbix' }) as HTMLSelectElement
    expect(gated.checked).toBe(true)
    expect(merger.value).toBe('plumbix')
    expect(panel.textContent).toContain('Plumbix merges them when the gate approves')
    // The merger choice saves through the settings endpoint; the card's badge follows.
    fireEvent.change(merger, { target: { value: 'github' } })
    await waitFor(() => expect(db.repos[0].merger).toBe('github'))
    await waitFor(() => expect(plumbix.querySelector('.repo-line')!.textContent).toContain('Pull requests · GitHub merges'))
    expect(panel.textContent).toContain('GitHub merges them once plumbix/gate is green')
    // The live checks, each line led by a word, never a colour alone.
    fireEvent.click(within(panel).getByRole('button', { name: 'Check' }))
    const lines = await within(panel).findByRole('list', { name: 'GitHub check for plumbix' })
    const items = within(lines).getAllByRole('listitem').map((li) => li.textContent ?? '')
    expect(items).toHaveLength(3)
    expect(items[0]).toContain('Installed')
    expect(items[0]).toContain('App plumbix-gate is installed on this repository')
    expect(items[1]).toContain('Required')
    expect(items[1]).toContain('plumbix/gate is required on main')
    expect(items[2]).toContain('Webhook')
    expect(items[2]).toContain('polling every 15s as well')
  })

  it('leads every state of the GitHub check with a word: not installed with its link, not required, unknown, no intake (WU-061)', async () => {
    db.githubReports[db.repos[0].id] = {
      app: { configured: true, slug: 'plumbix-gate', installed: false, install_url: 'https://github.com/apps/plumbix-gate/installations/new', message: 'App plumbix-gate is not installed on this repository: no check run can be posted until it is' },
      gate: [
        { branch: 'main', required: false, known: true, message: 'plumbix/gate is not required on main: GitHub merges without waiting for the gate' },
        { branch: 'release', required: false, known: false, message: 'Whether plumbix/gate is required on release is unknown: reading the branch protection needs administration read' },
      ],
      intake: { mode: 'none', webhook_secret_set: false, message: 'No intake: polling is off and no webhook secret is set, so pull requests are not watched' },
    }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(within(plumbix).getByRole('button', { name: 'GitHub' }))
    const panel = within(plumbix).getByRole('group', { name: 'GitHub for plumbix' })
    fireEvent.click(within(panel).getByRole('button', { name: 'Check' }))
    const lines = await within(panel).findByRole('list', { name: 'GitHub check for plumbix' })
    const items = within(lines).getAllByRole('listitem')
    expect(items.map((li) => li.querySelector('strong')?.textContent)).toEqual(['Not installed', 'Not required', 'Unknown', 'No intake'])
    const link = within(items[0]).getByRole('link', { name: 'Install the App' }) as HTMLAnchorElement
    expect(link.href).toBe('https://github.com/apps/plumbix-gate/installations/new')
    expect(items[2].textContent).toContain('administration read')
    // A webhook secret with no delivery yet says so in the word, not only in the message.
    db.githubReports[db.repos[0].id].intake = { mode: 'webhook', webhook_secret_set: true, poll_interval: '15s', message: 'Webhook secret set: signed deliveries are accepted at POST /api/v1/hooks/github; last delivery for this repository none since the server started, and polling every 15s as well' }
    fireEvent.click(within(panel).getByRole('button', { name: 'Check' }))
    await waitFor(() => expect(within(lines).getAllByRole('listitem')[3].querySelector('strong')?.textContent).toBe('Webhook, nothing yet'))
  })

  it('turns pull requests off and on, the merger taking its default each time (WU-061)', async () => {
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    const plumbix = await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(within(plumbix).getByRole('button', { name: 'GitHub' }))
    const panel = within(plumbix).getByRole('group', { name: 'GitHub for plumbix' })
    const gated = within(panel).getByRole('checkbox', { name: 'Pull requests gated for plumbix' }) as HTMLInputElement
    const merger = within(panel).getByRole('combobox', { name: 'Merged by for plumbix' }) as HTMLSelectElement
    fireEvent.click(gated)
    await waitFor(() => expect(db.repos[0].pull_requests).toBe(false))
    expect(db.repos[0].merger).toBe('plumbix')
    await waitFor(() => expect(merger.disabled).toBe(true))
    expect(panel.textContent).toContain('Pull requests are not watched')
    expect(plumbix.querySelector('.repo-line')!.textContent).not.toContain('Pull requests ·')
    fireEvent.click(gated)
    await waitFor(() => expect(db.repos[0].pull_requests).toBe(true))
    // A repository that opts in defaults to GitHub merging (WU-054).
    expect(db.repos[0].merger).toBe('github')
    await waitFor(() => expect(merger.value).toBe('github'))
    expect(plumbix.querySelector('.repo-line')!.textContent).toContain('Pull requests · GitHub merges')
  })

  it('imports a local folder with its remote and its own token, checking access (WU-041)', async () => {
    db.folders['~/dev/billing'] = { path: '/Users/me/dev/billing', remote: 'git@github.com:acme/billing.git', default_branch: 'trunk', has_pipeline: false, host: 'github', url: 'https://github.com/acme/billing.git', problem: '' }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    expect(within(panel).getByRole('tab', { name: 'Local folder' }).getAttribute('aria-selected')).toBe('true')
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/billing' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    // The remote, turned into HTTPS, with the folder's default branch; the missing pipeline is said.
    await waitFor(() => expect((within(panel).getByLabelText('URL') as HTMLInputElement).value).toBe('https://github.com/acme/billing.git'))
    expect((within(panel).getByLabelText('Host') as HTMLSelectElement).value).toBe('github')
    expect((within(panel).getByLabelText('Protected branch') as HTMLInputElement).value).toBe('trunk')
    expect(panel.textContent).toContain('git@github.com:acme/billing.git')
    expect(panel.textContent).toContain('No .plumbix/pipeline.yaml')
    fireEvent.click(within(panel).getByLabelText('Its own token'))
    fireEvent.change(within(panel).getByLabelText('Token value'), { target: { value: 'ghp_secret_value' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Import' }))
    const repo = await waitFor(() => {
      const r = db.repos.find((x) => x.url === 'https://github.com/acme/billing.git')
      expect(r).toBeTruthy()
      return r!
    })
    expect(repo).toMatchObject({ host: 'github', protected_branches: ['trunk'], merge_strategy: 'squash' })
    await waitFor(() => expect(db.repoTokens[repo.id]).toBe('ghp_secret_value'))
    await waitFor(() => expect(panel.textContent).toContain('Access checked'))
    expect(panel.textContent).toContain('Registered billing')
    expect(panel.textContent).toContain('Token stored')
    expect(document.body.textContent).not.toContain('ghp_secret_value')
    await screen.findByRole('listitem', { name: 'Repository billing' })
  })

  it('registers a remote and clones it to a local folder with the host token (WU-041)', async () => {
    db.secrets.PLUMBIX_AZURE_DEVOPS_TOKEN = 'host-azure-token'
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.click(within(panel).getByRole('tab', { name: 'Clone a remote' }))
    fireEvent.change(within(panel).getByLabelText('Remote URL'), { target: { value: 'https://dev.azure.com/acme/ledger/_git/payments' } })
    expect(panel.textContent).toContain('Azure DevOps')
    expect(panel.textContent).not.toContain('registers')
    expect((within(panel).getByLabelText('Destination folder') as HTMLInputElement).value).toBe('~/dev/payments')
    expect((within(panel).getByLabelText('Use the host token') as HTMLInputElement).checked).toBe(true)
    fireEvent.click(within(panel).getByRole('button', { name: 'Register and clone' }))
    await waitFor(() => expect(panel.textContent).toContain('Cloned to /Users/me/dev/payments'))
    const repo = db.repos.find((x) => x.url === 'https://dev.azure.com/acme/ledger/_git/payments')!
    expect(repo.host).toBe('azuredevops')
    expect(db.clones[repo.id]).toBe('~/dev/payments')
    expect(repo.id in db.repoTokens).toBe(false)
  })

  it('shows the HTTPS address an SSH remote is registered under (WU-041)', async () => {
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.click(within(panel).getByRole('tab', { name: 'Clone a remote' }))
    fireEvent.change(within(panel).getByLabelText('Remote URL'), { target: { value: 'git@github.com:acme/notes.git' } })
    expect(panel.textContent).toContain('GitHub · registers https://github.com/acme/notes.git')
    expect((within(panel).getByLabelText('Destination folder') as HTMLInputElement).value).toBe('~/dev/notes')
    fireEvent.change(within(panel).getByLabelText('Remote URL'), { target: { value: 'https://gitlab.com/acme/notes' } })
    expect(panel.textContent).toContain('not a GitHub, Azure DevOps or SSH remote')
    expect((within(panel).getByRole('button', { name: 'Register and clone' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('publishes a folder with no remote to a new private GitHub repository, then registers it (WU-042)', async () => {
    db.folders['~/dev/notes'] = { path: '/Users/me/dev/notes', remote: '', default_branch: 'main', has_pipeline: false, host: '', url: '', problem: 'the folder has no remote: publish it to a new repository online first' }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.click(within(panel).getByRole('tab', { name: 'Publish a folder' }))
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/notes' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await waitFor(() => expect((within(panel).getByLabelText('Name') as HTMLInputElement).value).toBe('notes'))
    expect((within(panel).getByLabelText('Private') as HTMLInputElement).checked).toBe(true)
    expect(panel.textContent).toContain('adds it to this folder as origin and pushes every branch')
    fireEvent.click(within(panel).getByLabelText('Its own token'))
    fireEvent.change(within(panel).getByLabelText('Token value'), { target: { value: 'ghp_publish_value' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Create and push' }))
    await waitFor(() => expect(panel.textContent).toContain('Access checked'))
    expect(db.publishes[0]).toMatchObject({ path: '~/dev/notes', host: 'github', owner: '', name: 'notes', private: true, token: 'ghp_publish_value' })
    expect(panel.textContent).toContain('Created https://github.com/eduard/notes')
    expect(panel.textContent).toContain('Pushed draft, main')
    expect(panel.textContent).toContain('Registered notes')
    const repo = db.repos.find((r) => r.url === 'https://github.com/eduard/notes')!
    expect(db.repoTokens[repo.id]).toBe('ghp_publish_value')
    expect(document.body.textContent).not.toContain('ghp_publish_value')
  })

  it('asks for an Azure DevOps organisation and project when publishing there, and sends a folder with a remote to the other tab', async () => {
    db.secrets.PLUMBIX_AZURE_DEVOPS_TOKEN = 'host-azure-token'
    db.folders['~/dev/notes'] = { path: '/Users/me/dev/notes', remote: '', default_branch: 'main', has_pipeline: false, host: '', url: '', problem: 'no remote' }
    db.folders['~/dev/billing'] = { path: '/Users/me/dev/billing', remote: 'git@github.com:acme/billing.git', default_branch: 'main', has_pipeline: true, host: 'github', url: 'https://github.com/acme/billing.git', problem: '' }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.click(within(panel).getByRole('tab', { name: 'Publish a folder' }))
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/billing' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByText(/already has a remote/)
    expect(within(panel).queryByRole('button', { name: 'Create and push' })).toBeNull()
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/notes' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByLabelText('Name')
    fireEvent.change(within(panel).getByLabelText('Online host'), { target: { value: 'azuredevops' } })
    expect(within(panel).queryByLabelText('Private')).toBeNull()
    const create = within(panel).getByRole('button', { name: 'Create and push' }) as HTMLButtonElement
    expect(create.disabled).toBe(true)
    fireEvent.change(within(panel).getByLabelText('Organisation'), { target: { value: 'acme' } })
    fireEvent.change(within(panel).getByLabelText('Project'), { target: { value: 'ledger' } })
    expect(create.disabled).toBe(false)
    fireEvent.click(create)
    await waitFor(() => expect(panel.textContent).toContain('Created https://dev.azure.com/acme/ledger/_git/notes'))
    expect(db.publishes[0]).toMatchObject({ host: 'azuredevops', owner: 'acme', project: 'ledger', name: 'notes' })
  })

  it('says when the host has no token and asks for one for this repository instead (WU-043)', async () => {
    db.folders['~/dev/ledger'] = { path: '/Users/me/dev/ledger', remote: 'https://acme@dev.azure.com/acme/Ledger%20Service/_git/Ledger%20Service', default_branch: 'main', has_pipeline: false, host: 'azuredevops', url: 'https://dev.azure.com/acme/Ledger%20Service/_git/Ledger%20Service', problem: '' }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/ledger' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByText(/No Azure DevOps host token is set/)
    expect((within(panel).getByLabelText('Use the host token') as HTMLInputElement).disabled).toBe(true)
    expect((within(panel).getByLabelText('Its own token') as HTMLInputElement).checked).toBe(true)
    const importButton = within(panel).getByRole('button', { name: 'Import' }) as HTMLButtonElement
    expect(importButton.disabled).toBe(true)
    fireEvent.change(within(panel).getByLabelText('Token value'), { target: { value: 'azure-pat' } })
    expect(importButton.disabled).toBe(false)
    fireEvent.click(importButton)
    // The registered name reads as Azure DevOps shows it, spaces and all.
    await waitFor(() => expect(panel.textContent).toContain('Registered Ledger Service'))
    await screen.findByRole('listitem', { name: 'Repository Ledger Service' })
  })

  it('asks for a token for the repository when Settings cannot be read (WU-043 review)', async () => {
    db.folders['~/dev/billing'] = { path: '/Users/me/dev/billing', remote: 'git@github.com:acme/billing.git', default_branch: 'main', has_pipeline: false, host: 'github', url: 'https://github.com/acme/billing.git', problem: '' }
    db.secrets.PLUMBIX_GITHUB_TOKEN = 'host'
    const realFetch = globalThis.fetch
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => (String(input).endsWith('/api/v1/settings') ? Promise.resolve(new Response('{}', { status: 500 })) : realFetch(input, init)))
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/billing' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByText(/No GitHub host token is set/)
    expect((within(panel).getByLabelText('Its own token') as HTMLInputElement).checked).toBe(true)
  })

  it('says why a folder cannot be imported and imports nothing', async () => {
    db.folders['~/dev/notes'] = { path: '/Users/me/dev/notes', remote: '', default_branch: 'main', has_pipeline: false, host: '', url: '', problem: 'the folder has no remote: publish it to a new repository online first' }
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByRole('listitem', { name: 'Repository plumbix' })
    fireEvent.click(screen.getByRole('button', { name: 'Import' }))
    const panel = screen.getByRole('region', { name: 'Import a repository' })
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '~/dev/notes' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByText(/has no remote/)
    expect(within(panel).queryByRole('button', { name: 'Import' })).toBeNull()
    // The folder can be published instead, carried over to that tab.
    fireEvent.click(within(panel).getByRole('button', { name: 'Publish it instead' }))
    expect(within(panel).getByRole('tab', { name: 'Publish a folder' }).getAttribute('aria-selected')).toBe('true')
    expect((within(panel).getByLabelText('Name') as HTMLInputElement).value).toBe('notes')
    fireEvent.click(within(panel).getByRole('tab', { name: 'Local folder' }))
    fireEvent.change(within(panel).getByLabelText('Folder'), { target: { value: '/etc' } })
    fireEvent.click(within(panel).getByRole('button', { name: 'Inspect' }))
    await within(panel).findByText(/\/etc does not exist/)
    expect(db.repos).toHaveLength(2)
  })

  it('shows the empty state without repositories', async () => {
    db.repos = []
    db.changes = []
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByText('No repositories yet')
    expect(screen.getByText(/Import one from a local folder or clone a remote/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Import' })).toBeTruthy()
  })
})

describe('Design page', () => {
  it('renders both themes with the encoding table and components', async () => {
    window.history.replaceState(null, '', '/design')
    render(<App />)
    await waitFor(() => expect(document.querySelectorAll('.theme-pane')).toHaveLength(2))
    const panes = document.querySelectorAll<HTMLElement>('.theme-pane')
    expect(panes[0].dataset.theme).toBe('light')
    expect(panes[1].dataset.theme).toBe('dark')
    for (const pane of panes) {
      expect(pane.querySelectorAll('.encoding-row')).toHaveLength(28)
      expect(pane.textContent).toContain('Tokens')
      expect(pane.textContent).toContain('Components')
    }
  })
})
