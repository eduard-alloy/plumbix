import { describe, expect, it } from 'vitest'
import { highlight, lineTone, splitLines } from './log'

describe('splitLines', () => {
  it('splits on newlines, drops one trailing newline, keeps blank lines inside', () => {
    expect(splitLines('a\nb\n\nc\n')).toEqual(['a', 'b', '', 'c'])
    expect(splitLines('')).toEqual([])
    expect(splitLines('only')).toEqual(['only'])
    expect(splitLines('crlf\r\nline\r\n')).toEqual(['crlf', 'line'])
  })
})

describe('lineTone', () => {
  it('marks failures red and commands dim', () => {
    expect(lineTone('--- FAIL: TestX (0.00s)')).toBe('red')
    expect(lineTone('Error: something')).toBe('red')
    expect(lineTone('step test failed in 1m 58s (exit 1)')).toBe('red')
    expect(lineTone('step build errored')).toBe('red')
    expect(lineTone('$ go test ./...')).toBe('dim')
    expect(lineTone('ok  	github.com/x	0.4s')).toBe('')
  })
})

describe('highlight', () => {
  it('splits a line into plain and matching parts, numbering matches from a running counter', () => {
    const { parts, next } = highlight('fail then FAIL again', 'fail', 3)
    expect(parts).toEqual([
      { text: 'fail', match: 3 },
      { text: ' then ', match: null },
      { text: 'FAIL', match: 4 },
      { text: ' again', match: null },
    ])
    expect(next).toBe(5)
  })
  it('returns the line whole without a query', () => {
    expect(highlight('x', '', 0)).toEqual({ parts: [{ text: 'x', match: null }], next: 0 })
  })
})
