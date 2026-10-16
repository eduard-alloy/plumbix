import { useEffect, useState } from 'react'

// A browser's standing with the server. The full sign-in screen comes with
// WU-011; this one proves the chain: link → session → authenticated call.
type Standing =
  | { kind: 'loading' }
  | { kind: 'signed_in'; expiresAt?: string }
  | { kind: 'signed_out' }
  | { kind: 'link_used' }
  | { kind: 'link_expired' }
  | { kind: 'link_invalid' }
  | { kind: 'error'; message: string }

async function exchangeLink(token: string): Promise<Standing> {
  const res = await fetch('/api/v1/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (res.ok) {
    const body = (await res.json()) as { expires_at?: string }
    return { kind: 'signed_in', expiresAt: body.expires_at }
  }
  const body = (await res.json().catch(() => null)) as { error?: { code?: string; message?: string } } | null
  const code = body?.error?.code
  if (code === 'link_used' || code === 'link_expired' || code === 'link_invalid') return { kind: code }
  return { kind: 'error', message: body?.error?.message ?? `HTTP ${res.status}` }
}

async function currentStanding(): Promise<Standing> {
  const res = await fetch('/api/v1/session')
  if (res.status === 401) return { kind: 'signed_out' }
  if (!res.ok) return { kind: 'error', message: `HTTP ${res.status}` }
  const body = (await res.json()) as { expires_at?: string }
  return { kind: 'signed_in', expiresAt: body.expires_at }
}

export function App() {
  const [standing, setStanding] = useState<Standing>({ kind: 'loading' })
  const [version, setVersion] = useState('')
  const host = window.location.host

  useEffect(() => {
    fetch('/api/v1/health')
      .then((r) => r.json())
      .then((b: { version?: string }) => setVersion(b.version ?? ''))
      .catch(() => setVersion(''))
    const token = new URLSearchParams(window.location.search).get('t')
    const run = window.location.pathname === '/login' && token ? exchangeLink(token) : currentStanding()
    run.then(setStanding).catch((e: unknown) => setStanding({ kind: 'error', message: String(e) }))
    if (token) window.history.replaceState(null, '', '/login')
  }, [])

  return (
    <>
      <header className="topbar">
        <span className="wordmark">plumbix</span>
        <span className="host">
          {host}
          {version ? ` · ${version}` : ''}
        </span>
      </header>
      <main className="page">
        <Body standing={standing} host={host} />
      </main>
    </>
  )
}

function Body({ standing, host }: { standing: Standing; host: string }) {
  switch (standing.kind) {
    case 'loading':
      return <p>…</p>
    case 'signed_in':
      return (
        <>
          <span className="status" data-tone="green">
            Signed in
          </span>
          <h1>This browser is signed in</h1>
          <p>
            Plumbix on {host} will recognise this browser until you run plumbix logout
            {standing.expiresAt ? ` or the session expires on ${new Date(standing.expiresAt).toLocaleDateString()}` : ''}.
          </p>
          <p className="foot">The screens arrive with the next work units. You can close the terminal prompt.</p>
        </>
      )
    case 'link_used':
      return (
        <>
          <span className="status" data-tone="red">
            Link already used
          </span>
          <h1>This sign-in link was already used</h1>
          <p>Each link signs in one browser, once. Run plumbix login in your terminal to get a new link.</p>
          <pre>$ plumbix login</pre>
        </>
      )
    case 'link_expired':
      return (
        <>
          <span className="status">Link expired</span>
          <h1>This sign-in link expired</h1>
          <p>Links expire 10 minutes after they are printed. Run plumbix login in your terminal to get a new link.</p>
          <pre>$ plumbix login</pre>
        </>
      )
    case 'link_invalid':
    case 'signed_out':
      return (
        <>
          <span className="status">Not signed in</span>
          <h1>Sign in from your terminal</h1>
          <p>Run plumbix login where the server runs and open the link it prints in this browser.</p>
          <pre>$ plumbix login</pre>
        </>
      )
    case 'error':
      return (
        <>
          <span className="status" data-tone="red">
            Error
          </span>
          <h1>Plumbix did not answer as expected</h1>
          <p>{standing.message}</p>
        </>
      )
  }
}
