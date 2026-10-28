import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { App } from '../App'
import { approved, fixRequested, NOW, running } from '../test/fixtures'
import { emit, install, streamOpen, type FakeDB } from '../test/server'

let db: FakeDB

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  db = install()
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup() // unmount before the stubs go, or a late load would hit the real globals
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function renderList() {
  render(<App />)
  const list = await screen.findByRole('list', { name: 'Changes' })
  return list
}

function rowIds(list: HTMLElement): string[] {
  return within(list)
    .getAllByRole('listitem')
    .map((r) => r.getAttribute('data-id') ?? '')
}

describe('Changes page', () => {
  it('shows a skeleton, then the grouped list with the verdict column', async () => {
    render(<App />)
    expect(screen.getByLabelText('Loading changes').getAttribute('aria-busy')).toBe('true')
    const list = await screen.findByRole('list', { name: 'Changes' })
    expect(screen.getByRole('heading', { name: 'Needs you' })).toBeTruthy()
    expect(screen.getByText('Ready to merge, or waiting for a person')).toBeTruthy()
    expect(rowIds(list).slice(0, 2)).toEqual([approved.id, 'c_w4x6z1b8c3nf'])
    const first = within(list).getAllByRole('listitem')[0]
    expect(first.textContent).toContain('Retry failed artifact uploads')
    expect(first.textContent).toContain('3/3 met · 0 blockers')
    expect(first.textContent).toContain('plumbix')
    expect(first.textContent).toContain('feat/retry-uploads')
    const fix = within(list).getByText('Add Azure DevOps token expiry warning').closest('[role=listitem]')!
    expect(fix.textContent).toContain('1/3 met')
    expect(fix.textContent).toContain('2 blockers')
    const live = within(list).getByText('Stream step logs over the event stream').closest('[role=listitem]')!
    expect(live.textContent).toContain('iteration 1 · 1m 12s')
    expect(screen.getByText('7', { selector: '.page-header .count' })).toBeTruthy()
  })

  it('filters by text, repository and state, and clears', async () => {
    const list = await renderList()
    fireEvent.change(screen.getByLabelText('Filter changes'), { target: { value: 'token expiry' } })
    expect(rowIds(list)).toEqual([fixRequested.id])
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(rowIds(list)).toHaveLength(7)
    fireEvent.click(screen.getByRole('button', { name: 'ledger-api' }))
    expect(rowIds(list)).toEqual(['c_w4x6z1b8c3nf', 'c_e2y5u8i3o6pa'])
    fireEvent.click(screen.getByRole('button', { name: 'All repositories' }))
    fireEvent.change(screen.getByLabelText('State'), { target: { value: 'running' } })
    expect(rowIds(list)).toEqual([running.id])
    fireEvent.change(screen.getByLabelText('State'), { target: { value: 'closed' } })
    fireEvent.change(screen.getByLabelText('Filter changes'), { target: { value: 'zzz' } })
    expect(screen.getByText('No changes match these filters.')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(rowIds(screen.getByRole('list', { name: 'Changes' }))).toHaveLength(7)
  })

  it('lays each row out on two lines: state, title, by, moved; then origin, repository, branch, verdict, iteration (WU-058)', async () => {
    const list = await renderList()
    const row = list.querySelector(`[data-id="${fixRequested.id}"]`) as HTMLElement
    const lines = Array.from(row.querySelectorAll('.line')).map((l) => Array.from(l.querySelectorAll('.cell')).map((c) => c.textContent?.trim()))
    expect(lines).toHaveLength(2)
    expect(lines[0].slice(0, 3)).toEqual(['Fix requested', 'Add Azure DevOps token expiry warning', 'mira'])
    expect(lines[0][3]).toMatch(/^\d+m$/)
    expect(lines[1]).toEqual(['PR #7', 'plumbix', 'feat/ado-token-expiry → main', '1/3 met · 2 blockers', '1'])
    const header = document.querySelector('.cols') as HTMLElement
    expect(header.textContent).toContain('State')
    expect(header.textContent).toContain('Origin')
  })

  it('tags each row with its origin and filters by it (WU-057)', async () => {
    const list = await renderList()
    const pr = list.querySelector(`[data-id="${fixRequested.id}"]`) as HTMLElement
    expect(within(pr).getByText('PR #7')).toBeTruthy()
    expect(within(pr).getByText('PR #7').getAttribute('title')).toContain('pull request #7')
    const api = list.querySelector(`[data-id="${approved.id}"]`) as HTMLElement
    expect(within(api).getByText('API')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Origin'), { target: { value: 'pr' } })
    expect(rowIds(list)).toEqual([fixRequested.id])
    expect(window.location.search).toContain('origin=pr')
    fireEvent.change(screen.getByLabelText('Origin'), { target: { value: 'api' } })
    expect(rowIds(list)).toHaveLength(6)
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(rowIds(list)).toHaveLength(7)
  })

  it('moves with the keyboard and opens with Enter', async () => {
    const list = await renderList()
    fireEvent.keyDown(window, { key: 'ArrowDown' })
    const rows = within(list).getAllByRole('listitem')
    expect(document.activeElement).toBe(rows[0])
    fireEvent.keyDown(window, { key: 'j' })
    expect(document.activeElement).toBe(rows[1])
    fireEvent.keyDown(window, { key: 'k' })
    expect(document.activeElement).toBe(rows[0])
    fireEvent.keyDown(window, { key: '/' })
    expect(document.activeElement).toBe(screen.getByLabelText('Filter changes'))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(document.activeElement).not.toBe(screen.getByLabelText('Filter changes'))
    rows[0].focus()
    fireEvent.keyDown(rows[0], { key: 'Enter' })
    fireEvent.click(rows[0])
    expect(window.location.pathname).toBe(`/changes/${approved.id}`)
    await screen.findByRole('heading', { name: 'Retry failed artifact uploads' })
  })

  it('shows the empty state when there are no repositories', async () => {
    db.repos = []
    db.changes = []
    render(<App />)
    await screen.findByText('No repositories yet')
    expect(screen.getByText(/plumbix repo add/)).toBeTruthy()
  })

  it('updates rows in place from the event stream and shows the last event', async () => {
    const list = await renderList()
    expect(rowIds(list)[0]).toBe(approved.id)
    const c = db.changes.find((x) => x.id === approved.id)!
    c.state = 'merged'
    c.updated_at = new Date().toISOString()
    await streamOpen()
    emit({ type: 'change.state_changed', change_id: approved.id, repo_id: approved.repo_id, payload: { from: 'approved', to: 'merged' } })
    await waitFor(() => expect(rowIds(screen.getByRole('list', { name: 'Changes' }))[0]).not.toBe(approved.id))
    expect(within(screen.getByRole('list', { name: 'Changes' })).getByText('Retry failed artifact uploads').closest('[role=listitem]')!.textContent).toContain('Merged')
    expect(screen.getByTitle(/change state changed · merged · c_k7m2q9x4w1zp/)).toBeTruthy()
    expect(screen.getByText('Live')).toBeTruthy()
  })

  it('shows how to sign in when the API answers 401', async () => {
    db.signedIn = false
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in from your terminal' })
  })
})
