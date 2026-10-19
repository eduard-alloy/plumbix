import { describe, expect, it } from 'vitest'
import { detectRemote, remoteName } from './remote'

// The same cases as TestDetectRemote in internal/change, so the clone tab and the server agree (WU-041 review).
describe('detectRemote', () => {
  it('maps GitHub and Azure DevOps HTTPS and SSH remotes to HTTPS and keeps plain git remotes', () => {
    const cases: [string, string, string][] = [
      ['https://github.com/eduard-alloy/plumbix.git', 'github', 'https://github.com/eduard-alloy/plumbix.git'],
      ['https://github.com/eduard-alloy/plumbix/', 'github', 'https://github.com/eduard-alloy/plumbix'],
      ['https://someone@github.com/eduard-alloy/plumbix', 'github', 'https://github.com/eduard-alloy/plumbix'],
      ['git@github.com:eduard-alloy/plumbix.git', 'github', 'https://github.com/eduard-alloy/plumbix.git'],
      ['ssh://git@github.com/eduard-alloy/plumbix.git', 'github', 'https://github.com/eduard-alloy/plumbix.git'],
      ['https://acme@dev.azure.com/acme/ledger/_git/ledger-api', 'azuredevops', 'https://dev.azure.com/acme/ledger/_git/ledger-api'],
      ['https://acme.visualstudio.com/ledger/_git/ledger-api', 'azuredevops', 'https://acme.visualstudio.com/ledger/_git/ledger-api'],
      ['git@ssh.dev.azure.com:v3/acme/ledger/ledger-api', 'azuredevops', 'https://dev.azure.com/acme/ledger/_git/ledger-api'],
      ['ssh://git@git.example.com/team/app.git', 'git', 'ssh://git@git.example.com/team/app.git'],
      ['git@git.example.com:team/app.git', 'git', 'git@git.example.com:team/app.git'],
      ['/srv/git/app.git', 'git', '/srv/git/app.git'],
      ['file:///srv/git/app.git', 'git', 'file:///srv/git/app.git'],
    ]
    for (const [input, host, url] of cases) expect(detectRemote(input), input).toEqual({ host, url })
  })

  it('refuses what the server refuses', () => {
    for (const bad of ['', 'https://gitlab.com/team/app.git', 'relative/path', 'ftp://x/y', 'ssh://host', 'git@host:/abs']) expect(detectRemote(bad), bad).toBeNull()
  })

  it('names a repository by its last path segment', () => {
    expect(remoteName('git@github.com:acme/notes.git')).toBe('notes')
    expect(remoteName('https://dev.azure.com/acme/ledger/_git/payments/')).toBe('payments')
  })
})
