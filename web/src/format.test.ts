import { describe, expect, it } from 'vitest'
import { ago, agoLong, duration, minutesBetween, shortSha, elapsedSeconds } from './format'

describe('duration', () => {
  it('renders seconds under a minute and m ss above', () => {
    expect(duration(0)).toBe('0s')
    expect(duration(42)).toBe('42s')
    expect(duration(59.6)).toBe('1m 00s')
    expect(duration(72)).toBe('1m 12s')
    expect(duration(118)).toBe('1m 58s')
    expect(duration(3600)).toBe('60m 00s')
  })
})

describe('ago', () => {
  it('uses the design scale: now, minutes, hours, yesterday, days', () => {
    expect(ago(0)).toBe('now')
    expect(ago(4)).toBe('4m')
    expect(ago(59)).toBe('59m')
    expect(ago(60)).toBe('1h')
    expect(ago(180)).toBe('3h')
    expect(ago(1500)).toBe('yesterday')
    expect(ago(2900)).toBe('2d')
    expect(ago(4300)).toBe('2d')
  })
  it('has a long form for the change header', () => {
    expect(agoLong(0)).toBe('just now')
    expect(agoLong(4)).toBe('4 min ago')
    expect(agoLong(180)).toBe('3 h ago')
    expect(agoLong(1500)).toBe('yesterday')
    expect(agoLong(4300)).toBe('2 days ago')
  })
})

describe('time helpers', () => {
  const now = new Date('2026-10-16T12:00:00Z')
  it('counts whole minutes between an ISO time and now, never negative', () => {
    expect(minutesBetween('2026-10-16T11:56:10Z', now)).toBe(3)
    expect(minutesBetween('2026-10-16T12:00:30Z', now)).toBe(0)
  })
  it('counts elapsed seconds since a start', () => {
    expect(elapsedSeconds('2026-10-16T11:58:48Z', now)).toBe(72)
  })
  it('shortens SHAs to seven characters', () => {
    expect(shortSha('9f3c1e2a81d7c0f2e19')).toBe('9f3c1e2')
    expect(shortSha('')).toBe('')
  })
})
