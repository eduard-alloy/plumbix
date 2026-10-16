// One store for the whole app: repos, changes, runs per change, alerts and
// the live connection. Loaded once, then kept fresh by refetching whatever
// an event names. Nothing here knows about screens.
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, onUnauthorized, Unauthorized } from './api'
import { connectEvents, describe, type Connection } from './events'
import type { Alert, Change, PlumbixEvent, Repo, Run } from './model'

export interface StoreState {
  loaded: boolean
  signedOut: boolean
  repos: Repo[]
  changes: Record<string, Change>
  runs: Record<string, Run[]>
  alerts: Alert[]
  lastEvent: { text: string; at: number } | null
  connection: Connection
}

export interface StoreActions {
  reloadAll: () => Promise<void>
  refreshChange: (id: string) => Promise<void>
  refreshAlerts: () => Promise<void>
  setChange: (c: Change) => void
  setSignedOut: (v: boolean) => void
}

const initial: StoreState = { loaded: false, signedOut: false, repos: [], changes: {}, runs: {}, alerts: [], lastEvent: null, connection: 'reconnecting' }

const StoreContext = createContext<{ state: StoreState; actions: StoreActions } | null>(null)

/** refreshDelay batches the refetches a burst of events would cause. */
const refreshDelay = 120

export function StoreProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<StoreState>(initial)
  const pending = useRef(new Map<string, ReturnType<typeof setTimeout>>())

  const setChange = useCallback((c: Change) => {
    setState((s) => ({ ...s, changes: { ...s.changes, [c.id]: c } }))
  }, [])

  const setSignedOut = useCallback((v: boolean) => setState((s) => ({ ...s, signedOut: v })), [])

  const refreshChange = useCallback(async (id: string) => {
    try {
      const [c, runs] = await Promise.all([api.change(id), api.runs(id)])
      setState((s) => ({ ...s, changes: { ...s.changes, [c.id]: c }, runs: { ...s.runs, [c.id]: runs } }))
    } catch (e) {
      if (e instanceof Unauthorized) return
      // A change that vanished or a hiccup: the next event tries again.
    }
  }, [])

  const refreshAlerts = useCallback(async () => {
    try {
      const alerts = await api.alerts()
      setState((s) => ({ ...s, alerts }))
    } catch {
      // handled by the unauthorized listener or the next event
    }
  }, [])

  const reloadAll = useCallback(async () => {
    try {
      const [repos, changes, alerts] = await Promise.all([api.repos(), api.changes(), api.alerts()])
      const runs: Record<string, Run[]> = {}
      await Promise.all(
        changes.map(async (c) => {
          runs[c.id] = await api.runs(c.id)
        }),
      )
      setState((s) => ({ ...s, loaded: true, signedOut: false, repos, changes: Object.fromEntries(changes.map((c) => [c.id, c])), runs, alerts }))
    } catch (e) {
      if (e instanceof Unauthorized) setState((s) => ({ ...s, signedOut: true }))
    }
  }, [])

  useEffect(() => onUnauthorized(() => setState((s) => ({ ...s, signedOut: true }))), [])

  useEffect(() => {
    void reloadAll()
  }, [reloadAll])

  // The live stream, while signed in.
  useEffect(() => {
    if (state.signedOut || !state.loaded) return
    const timers = pending.current
    const onEvent = (e: PlumbixEvent) => {
      setState((s) => ({ ...s, lastEvent: { text: describe(e), at: Date.now() } }))
      if (e.type === 'alert.raised' || e.type === 'alert.resolved') {
        void refreshAlerts()
        return
      }
      if (!e.change_id) return
      const id = e.change_id
      clearTimeout(timers.get(id))
      timers.set(
        id,
        setTimeout(() => {
          timers.delete(id)
          void refreshChange(id)
        }, refreshDelay),
      )
    }
    const close = connectEvents(onEvent, (connection) => setState((s) => (s.connection === connection ? s : { ...s, connection })))
    return () => {
      close()
      for (const t of timers.values()) clearTimeout(t)
      timers.clear()
    }
  }, [state.signedOut, state.loaded, refreshAlerts, refreshChange])

  const actions = useMemo<StoreActions>(() => ({ reloadAll, refreshChange, refreshAlerts, setChange, setSignedOut }), [reloadAll, refreshChange, refreshAlerts, setChange, setSignedOut])
  const value = useMemo(() => ({ state, actions }), [state, actions])
  return <StoreContext.Provider value={value}>{children}</StoreContext.Provider>
}

export function useStore(): { state: StoreState; actions: StoreActions } {
  const ctx = useContext(StoreContext)
  if (!ctx) throw new Error('useStore outside StoreProvider')
  return ctx
}

/** useNow re-renders once a second so elapsed times tick. */
export function useNow(): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(t)
  }, [])
  return now
}
