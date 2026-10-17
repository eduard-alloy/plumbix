// The live connection. The browser's EventSource reconnects by itself and
// resends Last-Event-ID, so the server replays whatever was missed.
import type { EventType, PlumbixEvent } from './model'

export const EVENT_TYPES: EventType[] = [
  'change.submitted',
  'change.state_changed',
  'change.merged',
  'run.started',
  'step.finished',
  'run.finished',
  'verdict.issued',
  'alert.raised',
  'alert.resolved',
  'change.approved_by_person',
]

export type Connection = 'live' | 'reconnecting'

/** connectEvents opens the stream and returns a function that closes it. */
export function connectEvents(onEvent: (e: PlumbixEvent) => void, onState: (s: Connection) => void): () => void {
  const es = new EventSource('/api/v1/events')
  const handler = (ev: MessageEvent) => {
    try {
      onEvent(JSON.parse(ev.data) as PlumbixEvent)
    } catch {
      // A frame that is not JSON is not ours; the next one will be.
    }
  }
  for (const t of EVENT_TYPES) es.addEventListener(t, handler as EventListener)
  es.onopen = () => onState('live')
  es.onerror = () => onState('reconnecting')
  return () => es.close()
}

/** describe renders an event for the sidebar's last-event line. */
export function describe(e: PlumbixEvent): string {
  const parts = [e.type.replace(/[._]/g, ' ')]
  if (e.type === 'step.finished' && typeof e.payload.step === 'string') parts.push(e.payload.step)
  if (e.type === 'change.state_changed' && typeof e.payload.to === 'string') parts.push(e.payload.to)
  if (e.change_id) parts.push(e.change_id)
  else if (e.run_id) parts.push(e.run_id)
  else if (typeof e.payload.alert_id === 'string') parts.push(e.payload.alert_id)
  return parts.join(' · ')
}
