// Pure helpers for the step log panel.

/** ansi matches what a terminal would act on and a log reader should not
 * see: CSI sequences (colours, cursor and erase controls, private modes),
 * OSC strings (titles, hyperlinks) ended by BEL or ST, charset selects
 * (ESC ( B), and any other escape with its one following character
 * (ESC 7, ESC 8, ESC =, ESC >, ESC M, and the C1 range). A sequence cut off
 * at the end of the text is dropped as well. */
// eslint-disable-next-line no-control-regex
const ansi = /\x1b\[[0-?]*[ -\/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][@-~]|\x1b\[[0-?]*$|\x1b[0-~]|\x1b$/g

/** stripAnsi removes terminal escape sequences from log text. */
export function stripAnsi(text: string): string {
  return text.replace(ansi, '')
}

/** splitLines breaks a log into lines, tolerating CRLF and one final newline. */
export function splitLines(text: string): string[] {
  if (text === '') return []
  const lines = stripAnsi(text).replace(/\r\n/g, '\n').split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines
}

/** lineTone picks the colour class of a line: failures red, commands dim. */
export function lineTone(line: string): 'red' | 'dim' | '' {
  if (/FAIL|Error|errored|failed in|failed \(|expected .* got/.test(line)) return 'red'
  if (line.startsWith('$ ')) return 'dim'
  return ''
}

export interface Part {
  text: string
  match: number | null
}

/** highlight splits a line around case-insensitive matches of q, numbering
 * each match from `from`, and returns the next free number. */
export function highlight(line: string, q: string, from: number): { parts: Part[]; next: number } {
  const needle = q.trim().toLowerCase()
  if (!needle) return { parts: [{ text: line, match: null }], next: from }
  const lower = line.toLowerCase()
  const parts: Part[] = []
  let p = 0
  let n = from
  for (let k = lower.indexOf(needle, p); k !== -1; k = lower.indexOf(needle, p)) {
    if (k > p) parts.push({ text: line.slice(p, k), match: null })
    parts.push({ text: line.slice(k, k + needle.length), match: n++ })
    p = k + needle.length
  }
  if (p < line.length) parts.push({ text: line.slice(p), match: null })
  if (parts.length === 0) parts.push({ text: line, match: null })
  return { parts, next: n }
}
