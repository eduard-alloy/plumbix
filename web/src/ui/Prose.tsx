// Prose lays out the blocks vm/prose produces: paragraphs, lists, code spans.
import type { Block, Inline } from '../vm/prose'

function Spans({ inline }: { inline: Inline[] }) {
  return (
    <>
      {inline.map((s, i) =>
        s.code ? (
          <code key={i}>{s.text}</code>
        ) : (
          <span key={i}>{s.text}</span>
        ),
      )}
    </>
  )
}

export function Prose({ blocks, className }: { blocks: Block[]; className?: string }) {
  return (
    <div className={className}>
      {blocks.map((b, i) => {
        if (b.kind === 'p') {
          return (
            <p key={i}>
              <Spans inline={b.inline} />
            </p>
          )
        }
        const items = b.items.map((it, j) => (
          <li key={j}>
            <Spans inline={it} />
          </li>
        ))
        return b.kind === 'ul' ? <ul key={i}>{items}</ul> : <ol key={i}>{items}</ol>
      })}
    </div>
  )
}
