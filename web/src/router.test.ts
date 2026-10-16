import { describe, expect, it } from 'vitest'
import { parseRoute, pathFor } from './router'

describe('parseRoute', () => {
  it('maps paths to screens', () => {
    expect(parseRoute('/')).toEqual({ screen: 'changes' })
    expect(parseRoute('/changes')).toEqual({ screen: 'changes' })
    expect(parseRoute('/changes/c_k7m2q9x4w1zp')).toEqual({ screen: 'change', id: 'c_k7m2q9x4w1zp' })
    expect(parseRoute('/login')).toEqual({ screen: 'login' })
    expect(parseRoute('/alerts')).toEqual({ screen: 'alerts' })
    expect(parseRoute('/repos')).toEqual({ screen: 'repos' })
    expect(parseRoute('/design')).toEqual({ screen: 'design' })
    expect(parseRoute('/settings')).toEqual({ screen: 'settings' })
    expect(parseRoute('/nope/x')).toEqual({ screen: 'changes' })
  })
  it('builds paths back', () => {
    expect(pathFor({ screen: 'change', id: 'c_1' })).toBe('/changes/c_1')
    expect(pathFor({ screen: 'changes' })).toBe('/')
    expect(pathFor({ screen: 'alerts' })).toBe('/alerts')
  })
})
