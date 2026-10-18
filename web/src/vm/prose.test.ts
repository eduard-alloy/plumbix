import { describe, expect, it } from 'vitest'
import { inlines, prose } from './prose'

describe('prose', () => {
  it('splits paragraphs on blank lines and joins wrapped lines', () => {
    const b = prose('First line\nstill first.\n\nSecond.')
    expect(b).toEqual([
      { kind: 'p', inline: [{ text: 'First line still first.', code: false }] },
      { kind: 'p', inline: [{ text: 'Second.', code: false }] },
    ])
  })

  it('recognises bullet and numbered lists', () => {
    expect(prose('- one\n* two\n• three')).toEqual([{ kind: 'ul', items: [[{ text: 'one', code: false }], [{ text: 'two', code: false }], [{ text: 'three', code: false }]] }])
    expect(prose('1. one\n2) two\n(3) three')).toEqual([{ kind: 'ol', items: [[{ text: 'one', code: false }], [{ text: 'two', code: false }], [{ text: 'three', code: false }]] }])
  })

  it('turns "(1) ... (2) ..." inside a paragraph into a lead-in and a numbered list', () => {
    const b = prose('Three parts. (1) Running steps: the hook fires. (2) Progress lines: `POST /log`. (3) The loop.')
    expect(b).toEqual([
      { kind: 'p', inline: [{ text: 'Three parts.', code: false }] },
      {
        kind: 'ol',
        items: [
          [{ text: 'Running steps: the hook fires.', code: false }],
          [
            { text: 'Progress lines: ', code: false },
            { text: 'POST /log', code: true },
            { text: '.', code: false },
          ],
          [{ text: 'The loop.', code: false }],
        ],
      },
    ])
    // Out-of-order or lone markers are left as text.
    expect(prose('see (2) and (1)')).toEqual([{ kind: 'p', inline: [{ text: 'see (2) and (1)', code: false }] }])
    expect(prose('only (1) here')).toEqual([{ kind: 'p', inline: [{ text: 'only (1) here', code: false }] }])
  })

  it('marks code spans and leaves an unmatched backtick alone', () => {
    expect(inlines('run `go test` now')).toEqual([
      { text: 'run ', code: false },
      { text: 'go test', code: true },
      { text: ' now', code: false },
    ])
    expect(inlines('a `b')).toEqual([
      { text: 'a ', code: false },
      { text: 'b', code: false },
    ])
  })
})
