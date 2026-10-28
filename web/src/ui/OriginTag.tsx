// OriginTag says where a change came from (WU-057): a pull request Plumbix
// watches, or a submission through the API. The words carry the meaning;
// the tint only helps a scan. Inside a row button it is a span, since a
// link may not nest in a button; on the change page it links to the pull
// request.
import type { OriginVM } from '../vm/changes'

export function OriginTag({ origin, link = false }: { origin: OriginVM; link?: boolean }) {
  const cls = `tag origin origin-${origin.kind}`
  if (link && origin.url) {
    return (
      <a className={cls} href={origin.url} target="_blank" rel="noreferrer" title={origin.title}>
        {origin.label}
      </a>
    )
  }
  return (
    <span className={cls} title={origin.title}>
      {origin.label}
    </span>
  )
}
