import { describe, expect, it } from 'vitest'
import { listGroups, repoName, verdictColumn, type ListFilters } from './changes'
import { allChanges, approved, closed, fixRequested, needsHuman, NOW, open, repos, running, runsByChange } from '../test/fixtures'

const none: ListFilters = { q: '', repo: 'all', state: 'all', origin: 'all' }

describe('listGroups', () => {
  it('puts Needs you first, newest activity first, then everything else with running on top', () => {
    const groups = listGroups(allChanges, runsByChange, repos, none, NOW)
    expect(groups.map((g) => g.title)).toEqual(['Needs you', 'Everything else'])
    expect(groups[0].sub).toBe('Ready to merge, or waiting for a person')
    expect(groups[0].rows.map((r) => r.id)).toEqual([approved.id, needsHuman.id])
    expect(groups[1].rows.map((r) => r.id)).toEqual([running.id, open.id, fixRequested.id, 'c_m3q7t1z8x2ra', closed.id])
    expect(groups[0].count).toBe(2)
  })

  it('names the only group Changes when nothing needs you', () => {
    const groups = listGroups([fixRequested, open], runsByChange, repos, none, NOW)
    expect(groups.map((g) => g.title)).toEqual(['Changes'])
  })

  it('filters by text over title, id and branch, by repository and by state', () => {
    const byText = listGroups(allChanges, runsByChange, repos, { ...none, q: 'ado-token' }, NOW)
    expect(byText.flatMap((g) => g.rows).map((r) => r.id)).toEqual([fixRequested.id])
    const byId = listGroups(allChanges, runsByChange, repos, { ...none, q: 'c_w4x6' }, NOW)
    expect(byId.flatMap((g) => g.rows).map((r) => r.id)).toEqual([needsHuman.id])
    const byRepo = listGroups(allChanges, runsByChange, repos, { ...none, repo: 'r_ledgerapi001' }, NOW)
    expect(byRepo.flatMap((g) => g.rows).map((r) => r.id)).toEqual([needsHuman.id, open.id])
    const byState = listGroups(allChanges, runsByChange, repos, { ...none, state: 'merged' }, NOW)
    expect(byState.flatMap((g) => g.rows).map((r) => r.id)).toEqual(['c_m3q7t1z8x2ra'])
    expect(listGroups(allChanges, runsByChange, repos, { ...none, q: 'nothing like this' }, NOW)).toEqual([])
  })

  it('fills the row columns from the change and its runs', () => {
    const [needs] = listGroups(allChanges, runsByChange, repos, none, NOW)
    const row = needs.rows[0]
    expect(row).toMatchObject({
      id: approved.id,
      state: 'approved',
      title: 'Retry failed artifact uploads',
      repo: 'plumbix',
      src: 'feat/retry-uploads',
      tgt: 'main',
      iter: '2',
      by: 'kai',
      moved: '4m',
      verdict: '3/3 met · 0 blockers',
      blockers: '',
    })
  })
})

describe('verdictColumn', () => {
  it('describes each state in a few mono words', () => {
    expect(verdictColumn(approved, runsByChange[approved.id], NOW)).toEqual({ text: '3/3 met · 0 blockers', blockers: '' })
    expect(verdictColumn(fixRequested, runsByChange[fixRequested.id], NOW)).toEqual({ text: '1/3 met', blockers: '2 blockers' })
    expect(verdictColumn(running, runsByChange[running.id], NOW)).toEqual({ text: 'iteration 1 · 1m 12s', blockers: '' })
    expect(verdictColumn(open, [], NOW)).toEqual({ text: 'waiting to validate', blockers: '' })
    expect(verdictColumn(needsHuman, runsByChange[needsHuman.id], NOW)).toEqual({ text: 'step build could not run: image golang:1.27 not found', blockers: '' })
    expect(verdictColumn(closed, runsByChange[closed.id], NOW)).toEqual({ text: '1/3 met', blockers: '2 blockers' })
  })
})

describe('repository names (WU-043)', () => {
  it('decodes URL encoding in the name and keeps a name that is not valid encoding', () => {
    const repo = (url: string) => ({ id: 'r_1', host: 'azuredevops' as const, url, protected_branches: ['main'], merge_strategy: 'squash' as const, merger: 'plumbix' as const, created_at: '' })
    expect(repoName(repo('https://dev.azure.com/acme/Ledger%20Service/_git/Ledger%20Service'))).toBe('Ledger Service')
    expect(repoName(repo('https://github.com/acme/notes.git'))).toBe('notes')
    expect(repoName(repo('https://dev.azure.com/acme/p/_git/odd%zz'))).toBe('odd%zz')
  })
})

