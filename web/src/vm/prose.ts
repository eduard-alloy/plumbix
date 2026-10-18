// prose turns the plain text a person or agent wrote (an intent, a reason)
// into blocks the page can lay out: paragraphs, bullet and numbered lists,
// with `code` spans. Not Markdown: just the shapes people actually type,
// including "(1) ... (2) ..." run together in one paragraph.

export interface Inline {
  text: string
  code: boolean
}

export type Block = { kind: 'p'; inline: Inline[] } | { kind: 'ul' | 'ol'; items: Inline[][] }

const bullet = /^[-*•]\s+/
const numbered = /^(?:\d+[.)]|\(\d+\))\s+/
const inlineEnum = /\s*\((\d+)\)\s+/g

/** inlines splits text on backticks into plain and code runs. */
export function inlines(text: string): Inline[] {
  const out: Inline[] = []
  const parts = text.split('`')
  parts.forEach((t, i) => {
    if (t === '') return
    // An unmatched trailing backtick stays plain text.
    const code = i % 2 === 1 && i < parts.length - 1
    out.push({ text: code ? t : t, code })
  })
  return out
}

function listOf(kind: 'ul' | 'ol', lines: string[], marker: RegExp): Block {
  return { kind, items: lines.map((l) => inlines(l.replace(marker, '').trim())) }
}

/** prose parses one text into blocks. Blank lines separate paragraphs. */
export function prose(text: string): Block[] {
  const blocks: Block[] = []
  for (const para of text.split(/\n\s*\n/)) {
    const lines = para
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
    if (!lines.length) continue
    if (lines.every((l) => bullet.test(l))) {
      blocks.push(listOf('ul', lines, bullet))
      continue
    }
    if (lines.every((l) => numbered.test(l))) {
      blocks.push(listOf('ol', lines, numbered))
      continue
    }
    const joined = lines.join(' ')
    // "(1) ... (2) ..." written in one breath: the lead-in stays a
    // paragraph, the items become a numbered list, in the order written.
    const marks = [...joined.matchAll(inlineEnum)]
    if (marks.length >= 2 && marks.every((m, i) => Number(m[1]) === i + 1)) {
      const lead = joined.slice(0, marks[0].index).trim()
      if (lead) blocks.push({ kind: 'p', inline: inlines(lead) })
      const items = marks.map((m, i) => {
        const start = m.index! + m[0].length
        const end = i + 1 < marks.length ? marks[i + 1].index! : joined.length
        return inlines(joined.slice(start, end).trim())
      })
      blocks.push({ kind: 'ol', items })
      continue
    }
    blocks.push({ kind: 'p', inline: inlines(joined) })
  }
  return blocks
}
