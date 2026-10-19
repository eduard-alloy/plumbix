// What the import panel knows about a remote before asking the server
// (WU-041): the host it belongs to and the URL Plumbix registers, by the
// same rules as change.DetectRemote on the server.
import type { Repo } from '../model'

export interface DetectedRemote {
  host: Repo['host']
  url: string
}

const GITHUB_HTTPS = /^https:\/\/github\.com\/[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/
const AZURE_HTTPS = [/^https:\/\/dev\.azure\.com\/[^/\s]+\/[^/\s]+\/_git\/[^/\s]+$/, /^https:\/\/[A-Za-z0-9-]+\.visualstudio\.com\/[^/\s]+\/_git\/[^/\s]+$/]
const GITHUB_SSH = /^(?:ssh:\/\/)?git@github\.com[:/]([A-Za-z0-9-]+\/[A-Za-z0-9._-]+)$/
const AZURE_SSH = /^(?:ssh:\/\/)?git@ssh\.dev\.azure\.com[:/]v3\/([^/\s]+)\/([^/\s]+)\/([^/\s]+)$/
const SSH = /^ssh:\/\/\S+$/
const SCP = /^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:\S+$/

/** detectRemote names the host of a remote and the URL to register, or null when Plumbix cannot reach it. */
export function detectRemote(remote: string): DetectedRemote | null {
  const r = remote.trim()
  let m = GITHUB_SSH.exec(r)
  if (m) return { host: 'github', url: `https://github.com/${m[1]}` }
  m = AZURE_SSH.exec(r)
  if (m) return { host: 'azuredevops', url: `https://dev.azure.com/${m[1]}/${m[2]}/_git/${m[3]}` }
  if (r.startsWith('https://')) {
    const url = r.replace(/^https:\/\/[^@/]+@/, 'https://').replace(/\/+$/, '')
    if (GITHUB_HTTPS.test(url)) return { host: 'github', url }
    if (AZURE_HTTPS.some((re) => re.test(url))) return { host: 'azuredevops', url }
    return null
  }
  if (SSH.test(r) || SCP.test(r) || r.startsWith('file:///') || r.startsWith('/')) return { host: 'git', url: r }
  return null
}

/** remoteName is the repository's name in a URL: its last path segment without .git. */
export function remoteName(url: string): string {
  return url.trim().replace(/\/+$/, '').replace(/\.git$/, '').split(/[/:]/).pop() ?? ''
}
