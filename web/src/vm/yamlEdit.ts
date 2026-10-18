// The text writer of the pipeline editor (WU-036, A34): the served file is
// parsed into a document that keeps comments, key order, scalar style and
// flow or block collections, the edited model is applied to it as targeted
// edits, and the document's text is what the server receives. An unchanged
// model saves byte-identical; a one-field change is a one-line diff.
import { isMap, isScalar, isSeq, parseDocument, YAMLMap, YAMLSeq, type Document, type Node } from 'yaml'
import { desired, plain, stageLists, STEP_KEYS, toYaml, validators, type PModel, type PStep, type StepKey, type StepValue } from './pipeline'

/** Printing: no line folding, no padding inside flow lists, the style the shipped pipeline and the writer use. */
const PRINT = { lineWidth: 0, flowCollectionPadding: false }

const same = (a: unknown, b: unknown): boolean => JSON.stringify(a) === JSON.stringify(b)

/** applyModel edits original so it describes p, touching only what differs. A file the library cannot read is written from scratch. */
export function applyModel(original: string, p: PModel): string {
  const parsed = parseDocument(original, { prettyErrors: false })
  if (parsed.errors.length || !isMap(parsed.contents)) return toYaml(p).join('\n') + '\n'
  // The parsed document's node types carry source ranges; edits make plain nodes.
  const doc = parsed as unknown as Document
  const root = doc.contents as YAMLMap
  if (!root.has('version')) root.items.unshift(doc.createPair('version', 1))
  let steps: unknown = root.get('steps', true)
  if (!isSeq(steps)) {
    steps = new YAMLSeq()
    root.set('steps', steps)
  }
  const seq = steps as YAMLSeq<Node>
  // A `steps: []` placeholder is a flow list; steps are block maps.
  if (seq.items.length === 0) seq.flow = false
  const byName = new Map<string, YAMLMap>()
  for (const item of seq.items) if (isMap(item)) byName.set(String(item.get('name')), item)
  const plainIds = plain(p).map((s) => s.id)
  const order = [...plain(p), ...validators(p)]
  // A served step's text belongs to the model step that came from it (src),
  // even after a rename; a step the editor created takes a served step's
  // text only when no other step claims it, and each node is used once, so
  // renaming build to compile and adding a new build never edits one node
  // twice.
  const claimed = new Set(order.map((s) => s.src).filter(Boolean))
  const used = new Set<YAMLMap>()
  seq.items = order.map((s) => {
    const node = s.src ? byName.get(s.src) : claimed.has(s.id) ? undefined : byName.get(s.id)
    if (!node || used.has(node)) return newStep(doc, s, plainIds)
    used.add(node)
    return updateStep(doc, node, s, plainIds)
  })
  const stages = stageLists(p)
  if (!stages.length) root.delete('stages')
  else {
    let sm: unknown = root.get('stages', true)
    if (!isMap(sm)) {
      sm = new YAMLMap()
      root.set('stages', sm)
    }
    const map = sm as YAMLMap
    for (const [g, ids] of stages) {
      const cur = map.get(g, true)
      if (cur && same(toPlain(cur), ids)) continue
      map.set(g, listNode(doc, ids, cur))
    }
    for (const key of map.items.map((pair) => String(pair.key))) if (!stages.some(([g]) => g === key)) map.delete(key)
  }
  return doc.toString(PRINT)
}

/** updateStep brings one existing step map to the model: the name, each owned key compared and replaced in place only when different, owned keys no longer wanted removed, unknown keys untouched. */
function updateStep(doc: Document, node: YAMLMap, s: PStep, plainIds: string[]): YAMLMap {
  if (node.get('name') !== s.id) node.set('name', s.id)
  const want = desired(s, plainIds)
  for (const key of STEP_KEYS) {
    const v = want[key]
    if (v === undefined) {
      if (node.has(key)) node.delete(key)
      continue
    }
    const cur = node.get(key, true)
    if (cur !== undefined && same(toPlain(cur), v)) continue
    node.set(key, valueNode(doc, key, v, cur))
  }
  return node
}

/** newStep writes a step the file never had, in the writer's key order. */
function newStep(doc: Document, s: PStep, plainIds: string[]): YAMLMap {
  const node = new YAMLMap()
  node.set('name', s.id)
  const want = desired(s, plainIds)
  for (const key of STEP_KEYS) if (want[key] !== undefined) node.set(key, valueNode(doc, key, want[key] as StepValue, undefined))
  return node
}

/** valueNode makes the node for a value, keeping the style of the node it replaces: a list stays block or flow, `when.paths` likewise. */
function valueNode(doc: Document, key: StepKey, v: StepValue, cur: unknown): Node {
  if (Array.isArray(v)) return listNode(doc, v, cur, key === 'run' ? false : undefined)
  if (key === 'when') {
    const paths = (v as { paths: string[] }).paths
    const prev = isMap(cur) ? cur.get('paths', true) : undefined
    const node = doc.createNode({}) as YAMLMap
    node.set('paths', listNode(doc, paths, prev))
    return node
  }
  return doc.createNode(v)
}

/** listNode makes a list, flow unless the list it replaces is block (or the default says block). */
function listNode(doc: Document, items: string[], cur: unknown, defaultFlow?: boolean): Node {
  const node = doc.createNode(items) as YAMLSeq
  node.flow = isSeq(cur) ? !!cur.flow : defaultFlow ?? true
  return node
}

/** toPlain is a node's value as plain data, for comparison with what the writer wants. */
function toPlain(node: unknown): unknown {
  if (isScalar(node)) return node.value
  if (isSeq(node) || isMap(node)) return node.toJSON()
  return node
}
