// The sign-in landing: what a plumbix login link opens, and what a browser
// without a session sees anywhere else.
import { useEffect, useState } from 'react'
import { api, ApiError } from '../api'
import { navigate } from '../router'
import { useStore } from '../store'
import { StatePill } from '../ui/StatePill'

type Standing = { kind: 'checking' } | { kind: 'signed_in'; expiresAt?: string } | { kind: 'signed_out' } | { kind: 'link_used' } | { kind: 'link_expired' } | { kind: 'link_invalid' } | { kind: 'error'; message: string }

export function SignInPage() {
  const { state, actions } = useStore()
  const [standing, setStanding] = useState<Standing>({ kind: 'checking' })
  const host = window.location.host

  useEffect(() => {
    const token = new URLSearchParams(window.location.search).get('t')
    if (window.location.pathname === '/login' && token) {
      window.history.replaceState(null, '', '/login')
      api
        .exchange(token)
        .then((s) => {
          setStanding({ kind: 'signed_in', expiresAt: s.expires_at })
          actions.setSignedOut(false)
          void actions.reloadAll()
        })
        .catch((e: unknown) => {
          if (e instanceof ApiError && (e.code === 'link_used' || e.code === 'link_expired' || e.code === 'link_invalid')) setStanding({ kind: e.code })
          else setStanding({ kind: 'error', message: e instanceof Error ? e.message : String(e) })
        })
      return
    }
    if (state.signedOut) {
      setStanding({ kind: 'signed_out' })
      return
    }
    api
      .session()
      .then((s) => setStanding({ kind: 'signed_in', expiresAt: s.expires_at }))
      .catch(() => setStanding({ kind: 'signed_out' }))
  }, [state.signedOut, actions])

  return (
    <div className="signin">
      <header className="topbar">
        <span className="brand-name mono">plumbix</span>
        <span className="mono dim">{host}</span>
      </header>
      <main className="signin-body">
        <Body standing={standing} host={host} />
      </main>
    </div>
  )
}

function Body({ standing, host }: { standing: Standing; host: string }) {
  switch (standing.kind) {
    case 'checking':
      return null
    case 'signed_in': {
      const until = standing.expiresAt ? ` or the session expires on ${new Date(standing.expiresAt).toLocaleDateString()}` : ' or the session expires in 30 days'
      return (
        <>
          <StatePill status="approved" label="Signed in" />
          <h1>This browser is signed in</h1>
          <p>
            Plumbix on {host} will recognise this browser until you run plumbix logout{until}.
          </p>
          <div>
            <button className="primary" onClick={() => navigate('/')}>
              Open changes
            </button>
          </div>
          <p className="foot">You can close the terminal prompt.</p>
        </>
      )
    }
    case 'link_used':
      return (
        <>
          <StatePill status="rejected" label="Link already used" />
          <h1>This sign-in link was already used</h1>
          <p>Each link signs in one browser, once. Run plumbix login in your terminal to get a new link.</p>
          <pre>
            <span className="dim">$ </span>plumbix login
          </pre>
        </>
      )
    case 'link_expired':
      return (
        <>
          <StatePill status="closed" label="Link expired" />
          <h1>This sign-in link expired</h1>
          <p>Links expire 10 minutes after they are printed. Run plumbix login in your terminal to get a new link.</p>
          <pre>
            <span className="dim">$ </span>plumbix login
          </pre>
        </>
      )
    case 'link_invalid':
    case 'signed_out':
      return (
        <>
          <StatePill status="open" label="Not signed in" />
          <h1>Sign in from your terminal</h1>
          <p>Run plumbix login where the server runs and open the link it prints in this browser.</p>
          <pre>
            <span className="dim">$ </span>plumbix login
          </pre>
        </>
      )
    case 'error':
      return (
        <>
          <StatePill status="errored" label="Error" />
          <h1>Plumbix did not answer as expected</h1>
          <p>{standing.message}</p>
        </>
      )
  }
}
