import { describe, expect, it } from 'vitest'
// @ts-expect-error node's types are not installed in this package; vitest runs on node, and the css option would empty an import of the sheet
import { readFileSync } from 'node:fs'

// vitest runs the test on node, whose process is not in this package's types either.
declare const process: { cwd(): string }

// The stylesheet is one file for every page, so a generic class name used
// in two places leaks from one into the other: WU-058's two-line rows of
// the changes list styled a bare `.line`, which widened the iteration
// timeline's one-pixel connector on the change page into a grey box.
describe('app.css', () => {
  const css: string = readFileSync(`${process.cwd()}/src/app.css`, 'utf8')
  const selectors = css
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('}')
    .map((rule: string) => rule.split('{')[0]?.trim() ?? '')
    .filter(Boolean)
    .flatMap((group: string) => group.split(',').map((sel) => sel.trim()))

  it('scopes the changes-list line rules to their rows and header, away from the iteration timeline', () => {
    const bare = selectors.filter((s) => /^\.line(-\d)?$/.test(s))
    expect(bare).toEqual([])
    // The list's rows, its header and its loading skeleton all carry the two lines.
    for (const scope of ['.row', '.cols', '.skeleton-row']) {
      for (const line of ['.line', '.line-1', '.line-2']) expect(selectors).toContain(`${scope} ${line}`)
    }
    expect(css).toMatch(/\.iteration \.line \{[^}]*width: 1px/)
  })
})
