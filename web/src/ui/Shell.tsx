// The app frame: sidebar with navigation and the live indicator, and the
// one scrolling main area. Global keys live here.
import { useEffect, type ReactNode } from 'react'
import { useRoute } from '../router'
import { useNow, useStore } from '../store'
import { duration } from '../format'
import { NEEDS_YOU } from '../model'
import { CLOSE_LOG } from './LogPanel'

export const FOCUS_SEARCH = 'plumbix:focus-search'

/** typing reports whether a text control has focus; keys then belong to it. */
function typing(): HTMLElement | null {
  const el = document.activeElement as HTMLElement | null
  const tag = el?.tagName?.toLowerCase()
  return tag === 'input' || tag === 'textarea' || tag === 'select' ? el : null
}

export function Shell({ children }: { children: ReactNode }) {
  const [route, go] = useRoute()
  const { state } = useStore()
  const now = useNow()
  const needs = Object.values(state.changes).filter((c) => NEEDS_YOU.includes(c.state)).length
  const openAlerts = state.alerts.filter((a) => !a.resolved_at).length

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const logOpen = document.querySelector<HTMLInputElement>('.log-panel input')
      if (e.key === '/' && !typing()) {
        e.preventDefault()
        if (logOpen) {
          logOpen.focus()
        } else if (route.screen === 'changes') {
          window.dispatchEvent(new Event(FOCUS_SEARCH))
        } else {
          go('/')
          setTimeout(() => window.dispatchEvent(new Event(FOCUS_SEARCH)), 0)
        }
        return
      }
      if (e.key === 'Escape') {
        const el = typing()
        if (el) {
          el.blur()
          return
        }
        if (logOpen) {
          window.dispatchEvent(new Event(CLOSE_LOG))
          return
        }
        if (route.screen === 'change') go('/')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [route.screen, go])

  const nav: { key: string; label: string; count: string; path: string; on: boolean }[] = [
    { key: 'changes', label: 'Changes', count: state.loaded ? String(needs) : '', path: '/', on: route.screen === 'changes' || route.screen === 'change' },
    { key: 'alerts', label: 'Alerts', count: state.loaded && openAlerts ? String(openAlerts) : '', path: '/alerts', on: route.screen === 'alerts' },
    { key: 'repos', label: 'Repositories', count: state.loaded ? String(state.repos.length) : '', path: '/repos', on: route.screen === 'repos' },
    { key: 'settings', label: 'Settings', count: '', path: '/settings', on: route.screen === 'settings' },
  ]
  const last = state.lastEvent ? `${state.lastEvent.text} · ${duration(Math.max(0, (now.getTime() - state.lastEvent.at) / 1000))} ago` : ''

  return (
    <div className="shell">
      <nav aria-label="Main" className="sidebar">
        <div className="brand">
          <span className="brand-mark" />
          <span className="brand-name">plumbix</span>
        </div>
        <div className="nav">
          {nav.map((n) => (
            <button key={n.key} className="nav-item" aria-current={n.on ? 'page' : undefined} onClick={() => go(n.path)}>
              <span className="nav-label">{n.label}</span>
              <span className="nav-count">{n.count}</span>
            </button>
          ))}
        </div>
        <div className="spacer" />
        <div className="live">
          <div className="live-row">
            <span className="live-dot" data-state={state.connection} />
            {state.connection === 'live' ? 'Live' : 'Reconnecting'}
            <span className="live-host">{window.location.host}</span>
          </div>
          <div className="live-last" title={last}>
            {last}
          </div>
        </div>
      </nav>
      <main className="main" id="main">
        {children}
      </main>
    </div>
  )
}
