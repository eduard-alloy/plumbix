import { describe, expect, it } from 'vitest'
import { highlight, lineTone, splitLines } from './log'

describe('splitLines', () => {
  it('splits on newlines, drops one trailing newline, keeps blank lines inside', () => {
    expect(splitLines('a\nb\n\nc\n')).toEqual(['a', 'b', '', 'c'])
    expect(splitLines('')).toEqual([])
    expect(splitLines('only')).toEqual(['only'])
    expect(splitLines('crlf\r\nline\r\n')).toEqual(['crlf', 'line'])
  })

  it('drops terminal escape sequences: colours, cursor moves, titles, and a lone escape', () => {
    // vitest's dot reporter, as a container prints it with colours on.
    expect(splitLines('\x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.12\x1b[39m\n\x1b[33m\x1b[39m\x1b[32m·\x1b[39m\x1b[32m·\x1b[39m\n')).toEqual([' RUN  v4.1.12', '··'])
    expect(splitLines('\x1b[2K\x1b[1Gprogress 50%\x1b[0K\n')).toEqual(['progress 50%'])
    expect(splitLines('\x1b]0;window title\x07plain\n\x1b]8;;https://x\x1b\\link\x1b]8;;\x1b\\\n')).toEqual(['plain', 'link'])
    expect(splitLines('a\x1bMb\x1b(Bc')).toEqual(['abc'])
    expect(splitLines('\x1b7save\x1b8restore\x1b=keypad\x1b>off')).toEqual(['saverestorekeypadoff'])
    expect(splitLines('\x1b[38;5;208m256-colour\x1b[0m \x1b[38;2;255;128;0mtruecolor\x1b[0m and \x1b[?25lhidden cursor\x1b[?25h')).toEqual(['256-colour truecolor and hidden cursor'])
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
