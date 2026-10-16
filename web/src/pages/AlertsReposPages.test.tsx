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
    const rows = await screen.findAllByRole('row')
    const plumbix = rows.find((r) => r.textContent?.includes('https://github.com/plumbix/plumbix'))!
    expect(plumbix.textContent).toContain('plumbix')
    expect(plumbix.textContent).toContain('GitHub')
    expect(plumbix.textContent).toContain('main')
    expect(plumbix.textContent).toContain('squash')
    expect(within(plumbix).getByText('5')).toBeTruthy()
    const ledger = rows.find((r) => r.textContent?.includes('ledger-api'))!
    expect(ledger.textContent).toContain('Azure DevOps')
    expect(within(ledger).getByText('2')).toBeTruthy()
    expect(screen.getByText(/Read-only\. Register and edit repositories from the terminal/)).toBeTruthy()
    expect(screen.getByText('2', { selector: '.page-header .count' })).toBeTruthy()
  })

  it('shows the empty state without repositories', async () => {
    db.repos = []
    db.changes = []
    window.history.replaceState(null, '', '/repos')
    render(<App />)
    await screen.findByText('No repositories yet')
    expect(screen.getByText(/registered from the terminal/)).toBeTruthy()
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
