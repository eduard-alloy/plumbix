// Formatting shared by every screen. Scales follow web/design/README.md.

/** duration renders seconds as "42s" or "1m 12s". */
export function duration(seconds: number): string {
  const s = Math.round(seconds)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
}

/** ago renders an age in minutes on the list scale: now, 4m, 2h, yesterday, 3d. */
export function ago(minutes: number): string {
  const m = Math.floor(minutes)
  if (m < 1) return 'now'
  if (m < 60) return `${m}m`
  if (m < 1440) return `${Math.floor(m / 60)}h`
  if (m < 2880) return 'yesterday'
  return `${Math.floor(m / 1440)}d`
}

/** agoLong is the sentence form used in the change header. */
export function agoLong(minutes: number): string {
  const m = Math.floor(minutes)
  if (m < 1) return 'just now'
  if (m < 60) return `${m} min ago`
  if (m < 1440) return `${Math.floor(m / 60)} h ago`
  if (m < 2880) return 'yesterday'
  return `${Math.floor(m / 1440)} days ago`
}

/** minutesBetween counts whole minutes from an ISO time to now, never negative. */
export function minutesBetween(iso: string, now: Date): number {
  return Math.max(0, Math.floor((now.getTime() - Date.parse(iso)) / 60000))
}

/** elapsedSeconds counts seconds from an ISO time to now, never negative. */
export function elapsedSeconds(iso: string, now: Date): number {
  return Math.max(0, Math.floor((now.getTime() - Date.parse(iso)) / 1000))
}

/** shortSha keeps the first seven characters. */
export function shortSha(sha: string): string {
  return sha.slice(0, 7)
}

/** clock renders an ISO time as HH:MM in the browser's zone. */
export function clock(iso: string): string {
  const d = new Date(iso)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

/** dateTime renders an ISO time as "2026-10-16 09:14" in the browser's zone. */
export function dateTime(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** plural appends an s unless n is one. */
export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}
