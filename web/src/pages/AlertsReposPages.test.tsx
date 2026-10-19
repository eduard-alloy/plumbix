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
    expect(bottom.textContent).toContain('https://github.com/plumbix/plumbix')
    expect(bottom.textContent).toContain('Protected: main')
    expect(bottom.textContent).toContain('Host token')
    const ledger = screen.getByRole('listitem', { name: 'Repository ledger-api' })
    expect(ledger.textContent).toContain('Azure DevOps')
    expect(ledger.textContent).toContain('2 changes')
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.getByText(/Import a repository from a local folder or a remote/)).toBeTruthy()
    expect(screen.getByText('2', { selector: '.page-header .count' })).toBeTruthy()
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
