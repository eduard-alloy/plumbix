// A small pathname router: the app has five screens and no nested routes.
import { useCallback, useSyncExternalStore } from 'react'

export type Route = { screen: 'changes' } | { screen: 'change'; id: string } | { screen: 'login' } | { screen: 'alerts' } | { screen: 'repos' }

export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean)
  if (parts.length === 0 || (parts[0] === 'changes' && parts.length === 1)) return { screen: 'changes' }
  if (parts[0] === 'changes' && parts.length === 2) return { screen: 'change', id: parts[1] }
  if (parts[0] === 'login') return { screen: 'login' }
  if (parts[0] === 'alerts') return { screen: 'alerts' }
  if (parts[0] === 'repos') return { screen: 'repos' }
  return { screen: 'changes' }
}

export function pathFor(r: Route): string {
  switch (r.screen) {
    case 'changes':
      return '/'
    case 'change':
      return `/changes/${r.id}`
    case 'login':
      return '/login'
    case 'alerts':
      return '/alerts'
    case 'repos':
      return '/repos'
  }
}

const NAVIGATE = 'plumbix:navigate'

/** navigate pushes a new path and tells every useRoute hook. */
export function navigate(to: Route | string): void {
  const path = typeof to === 'string' ? to : pathFor(to)
  if (window.location.pathname + window.location.search !== path) {
    window.history.pushState(null, '', path)
  }
  window.dispatchEvent(new Event(NAVIGATE))
}

function subscribe(fn: () => void): () => void {
  window.addEventListener('popstate', fn)
  window.addEventListener(NAVIGATE, fn)
  return () => {
    window.removeEventListener('popstate', fn)
    window.removeEventListener(NAVIGATE, fn)
  }
}

function snapshot(): string {
  return window.location.pathname
}

/** useRoute returns the current route and a navigate function. */
export function useRoute(): [Route, (to: Route | string) => void] {
  const pathname = useSyncExternalStore(subscribe, snapshot, snapshot)
  const go = useCallback((to: Route | string) => navigate(to), [])
  return [parseRoute(pathname), go]
}
