// The pipeline editor's model: the file as steps the inspector edits, the
// operations the toolbar offers, the band layout the canvas draws, the YAML
// the server receives, and the summary the edit bar shows. Pure functions;
// every operation returns a new model and never mutates its input.
import type { PipelineFile, PipelineStep } from '../model'
import { applyModel } from './yamlEdit'

/** EnvRow is one environment line; a secret row is exposed under its own name from Settings. */
export interface EnvRow {
  k: string
  v: string
  secret: boolean
}

/** PStep is one step in the editor. Validators are steps with the flag on. */
export interface PStep {
  id: string
  image: string
  run: string[]
  needs: string[]
  timeout: string
  retries: number
  allowFail: boolean
  env: EnvRow[]
  evidence: string[]
  /** cache lists absolute container paths kept between runs (WU-029). */
  cache: string[]
  paths: string[]
  group: string | null
  validator: boolean
  /** advisory: a validator whose verdict is recorded but never counted (WU-030). */
  advisory: boolean
  /** external: a validator answered outside the pipeline by plumbix review --watch; it has no image or commands (WU-023). */
  external: boolean
  /** reviewer names the reviewer profile; '' for the default (WU-028). */
  reviewer: string
  /** effort overrides the profile's reasoning effort; '' keeps it (WU-034). */
  effort: '' | 'low' | 'medium' | 'high'
  /** src is the step's name in the served file, kept through a rename so a save edits that step's text in place (WU-036); absent on a step the editor created. */
  src?: string
  /** Last run duration in seconds and its status, from the newest finished run on the branch. */
  last: number | null
  lastStatus: string
}

export interface PModel {
  steps: PStep[]
  /** Stage names in file order; stages with no steps left are dropped on output. */
  stageOrder: string[]
}

export const clone = <T>(o: T): T => JSON.parse(JSON.stringify(o)) as T

export function fmtSeconds(s: number): string {
  s = Math.round(s)
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
}

/** fromApi turns the served file into the editor's model; null when the file has no parsed pipeline. */
export function fromApi(f: PipelineFile): PModel | null {
  if (!f.pipeline) return null
  const last = f.last_run?.steps ?? {}
  const steps = f.pipeline.steps.map((s: PipelineStep): PStep => ({
    id: s.name,
    image: s.image,
    run: s.commands.length ? [...s.commands] : [],
    needs: [...s.needs],
    timeout: s.timeout ?? '',
    retries: s.retries,
    allowFail: s.allow_failure,
    env: [...Object.entries(s.env).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => ({ k, v, secret: false })), ...s.secrets.map((k) => ({ k, v: '', secret: true }))],
    evidence: [...s.artifacts],
    cache: [...(s.cache ?? [])],
    paths: [...s.paths],
    group: s.stage || null,
    validator: s.validator,
    advisory: !!s.advisory,
    external: !!s.external,
    reviewer: s.reviewer ?? '',
    effort: s.effort ?? '',
    src: s.name,
    last: last[s.name] ? last[s.name].duration_ms / 1000 : null,
    lastStatus: last[s.name]?.status ?? '',
  }))
  return { steps, stageOrder: f.pipeline.stages.map((g) => g.name) }
}

export const plain = (p: PModel): PStep[] => p.steps.filter((s) => !s.validator)
export const validators = (p: PModel): PStep[] => p.steps.filter((s) => s.validator)
export const find = (p: PModel, id: string): PStep | undefined => p.steps.find((s) => s.id === id)

/** levels gives each step its start wave: 0 for no needs, else one more than its deepest need. */
export function levels(p: PModel): Record<string, number> {
  const by = Object.fromEntries(p.steps.map((s) => [s.id, s]))
  const memo: Record<string, number> = {}
  const lv = (id: string, seen: Set<string>): number => {
    if (memo[id] != null) return memo[id]
    const s = by[id]
    if (!s || seen.has(id)) return 0
    seen.add(id)
    const v = s.needs.length ? 1 + Math.max(...s.needs.map((n) => lv(n, seen))) : 0
    memo[id] = v
    return v
  }
  for (const s of p.steps) lv(s.id, new Set())
  return memo
}

/** ordered sorts steps by wave, keeping file order inside a wave. */
export function ordered(steps: PStep[], L: Record<string, number>): PStep[] {
  const idx = Object.fromEntries(steps.map((s, i) => [s.id, i]))
  return [...steps].sort((a, b) => L[a.id] - L[b.id] || idx[a.id] - idx[b.id])
}

/** dependents returns id and every step that, directly or not, needs it. */
export function dependents(p: PModel, id: string): Set<string> {
  const out = new Set([id])
  let grew = true
  while (grew) {
    grew = false
    for (const s of p.steps) {
      if (!out.has(s.id) && s.needs.some((n) => out.has(n))) {
        out.add(s.id)
        grew = true
      }
    }
  }
  return out
}

/** needOptions lists the steps a step may add as a need without a cycle: not itself, not downstream, not a validator, not already needed. */
export function needOptions(p: PModel, id: string): string[] {
  const blocked = dependents(p, id)
  const s = find(p, id)
  return plain(p).filter((x) => !blocked.has(x.id) && !(s?.needs ?? []).includes(x.id)).map((x) => x.id)
}

export function uniqueId(p: PModel, base: string): string {
  const clean = base.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'step'
  let n = clean
  for (let i = 2; find(p, n); i++) n = `${clean}-${i}`
  return n
}

export function newStep(id: string, extra: Partial<PStep> = {}): PStep {
  return { id, image: 'alpine:3', run: ['echo "replace me"'], needs: [], timeout: '', retries: 0, allowFail: false, env: [], evidence: [], cache: [], paths: [], group: null, validator: false, advisory: false, external: false, reviewer: '', effort: '', last: null, lastStatus: '', ...extra }
}

/** detach rewires every step that needs id to id's own needs, so chains survive a removal. */
function detach(p: PModel, id: string): void {
  const s = find(p, id)
  const inherited = s && !s.validator ? s.needs : []
  for (const x of p.steps) {
    if (x.needs.includes(id)) x.needs = Array.from(new Set(x.needs.filter((n) => n !== id).concat(inherited)))
  }
}

export function addStep(p: PModel, needs: string[], group: string | null): { model: PModel; id: string } {
  const m = clone(p)
  const id = uniqueId(m, 'step')
  m.steps.push(newStep(id, { needs: [...needs], group }))
  if (group && !m.stageOrder.includes(group)) m.stageOrder.push(group)
  return { model: m, id }
}

export function removeSteps(p: PModel, ids: string[]): PModel {
  const m = clone(p)
  for (const id of ids) {
    detach(m, id)
    m.steps = m.steps.filter((x) => x.id !== id)
  }
  return m
}

export function renameStep(p: PModel, id: string, to: string): { model: PModel; id: string } {
  const nv = to.toLowerCase().replace(/[^a-z0-9-]/g, '')
  if (!nv || (nv !== id && find(p, nv))) return { model: p, id }
  const m = clone(p)
  for (const x of m.steps) x.needs = x.needs.map((n) => (n === id ? nv : n))
  find(m, id)!.id = nv
  return { model: m, id: nv }
}

/** setField changes one field; needs are only accepted when they keep the graph acyclic. */
export function setField<K extends keyof PStep>(p: PModel, id: string, field: K, value: PStep[K]): PModel {
  const m = clone(p)
  const s = find(m, id)
  if (!s) return p
  if (field === 'needs') {
    const blocked = dependents(p, id)
    const wanted = (value as string[]).filter((n) => n !== id && !blocked.has(n) && find(p, n) && !find(p, n)!.validator)
    s.needs = Array.from(new Set(wanted))
    return m
  }
  if (field === 'group') {
    const g = (value as string | null) || null
    s.group = s.validator ? null : g
    if (g && !m.stageOrder.includes(g)) m.stageOrder.push(g)
    return m
  }
  s[field] = value
  return m
}

/** toggleValidator moves a step into or out of the validator phase. Into: it loses needs, paths, stage and allow_failure, and its dependents are rewired. */
export function toggleValidator(p: PModel, id: string): PModel {
  const m = clone(p)
  const s = find(m, id)
  if (!s) return p
  if (!s.validator) {
    detach(m, id)
    s.validator = true
    s.needs = []
    s.paths = []
    s.cache = []
    s.group = null
    s.allowFail = false
    // Validators sit after every step in the file.
    m.steps = m.steps.filter((x) => x.id !== id).concat(s)
  } else {
    s.validator = false
    s.advisory = false
    s.external = false
    s.reviewer = ''
    s.effort = ''
  }
  return m
}

/** The image a validator gets back when it leaves the review loop and had none. */
export const AI_VALIDATOR_IMAGE = 'plumbix-ai-validator:dev'

/** toggleExternal moves a validator between the pipeline and the review loop (WU-037). External: no commands, env, secrets, retries or effort (the writer leaves out the image). Back: an image again. The reviewer is cleared both ways, since a profile answers one family only (A29). A plain step is unchanged. */
export function toggleExternal(p: PModel, id: string): PModel {
  const s0 = find(p, id)
  if (!s0 || !s0.validator) return p
  const m = clone(p)
  const s = find(m, id)!
  s.reviewer = ''
  if (!s.external) {
    s.external = true
    s.run = []
    s.env = []
    s.retries = 0
    s.effort = ''
  } else {
    s.external = false
    if (!s.image) s.image = AI_VALIDATOR_IMAGE
  }
  return m
}

/** mergeSteps joins two or more steps into one container run: commands in order, union of needs minus internal ones, env and evidence, longest timeout, highest retries. */
export function mergeSteps(p: PModel, selected: string[]): { model: PModel; id: string } | null {
  const L = levels(p)
  const ids = ordered(plain(p).filter((s) => selected.includes(s.id)), L).map((s) => s.id)
  if (ids.length < 2) return null
  const m = clone(p)
  const parts = ids.map((id) => find(m, id)!)
  const id = uniqueId(m, ids.join('-').slice(0, 40))
  const env: EnvRow[] = []
  for (const s of parts) for (const e of s.env) if (!env.some((x) => x.k === e.k)) env.push({ ...e })
  const merged = newStep(id, {
    image: parts[0].image,
    run: parts.flatMap((s) => s.run),
    needs: Array.from(new Set(parts.flatMap((s) => s.needs))).filter((n) => !ids.includes(n)),
    timeout: parts.map((s) => s.timeout).filter(Boolean).sort((a, b) => seconds(a) - seconds(b)).pop() ?? '',
    retries: Math.max(...parts.map((s) => s.retries)),
    allowFail: parts.every((s) => s.allowFail),
    env,
    evidence: Array.from(new Set(parts.flatMap((s) => s.evidence))),
    cache: Array.from(new Set(parts.flatMap((s) => s.cache))),
    paths: parts.every((s) => s.paths.length) ? Array.from(new Set(parts.flatMap((s) => s.paths))) : [],
    group: parts[0].group,
    last: parts.reduce((a, s) => a + (s.last || 0), 0) || null,
  })
  const at = m.steps.findIndex((s) => s.id === ids[0])
  m.steps = m.steps.filter((s) => !ids.includes(s.id))
  m.steps.splice(at, 0, merged)
  for (const s of m.steps) {
    if (s.id !== id && s.needs.some((n) => ids.includes(n))) s.needs = Array.from(new Set(s.needs.map((n) => (ids.includes(n) ? id : n))))
  }
  return { model: m, id }
}

export function groupSteps(p: PModel, selected: string[], name: string): PModel {
  const g = name.trim().toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '') || 'stage'
  const m = clone(p)
  for (const s of m.steps) if (selected.includes(s.id) && !s.validator) s.group = g
  if (!m.stageOrder.includes(g)) m.stageOrder.push(g)
  return m
}

/** seconds parses a Go duration such as 1h30m or 90s; unknown text is 0. */
export function seconds(d: string): number {
  let total = 0
  for (const m of d.matchAll(/(\d+(?:\.\d+)?)(h|m|s|ms)/g)) {
    const v = parseFloat(m[1])
    total += m[2] === 'h' ? v * 3600 : m[2] === 'm' ? v * 60 : m[2] === 's' ? v : v / 1000
  }
  return total
}

// ---- YAML ----

const PLAIN_YAML = /^[A-Za-z0-9_./][A-Za-z0-9_./: \-=+@$%,~^|<>?()']*$/
const RESERVED = new Set(['true', 'false', 'yes', 'no', 'on', 'off', 'null', '~', ''])

/** yamlStr quotes a scalar when YAML would read it as something else. */
export function yamlStr(s: string): string {
  if (PLAIN_YAML.test(s) && !s.includes(': ') && !s.includes(' #') && !RESERVED.has(s.toLowerCase()) && !/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(s) && !/^0x|^0o|^[-+]?\.inf|^\.nan/i.test(s) && !s.endsWith(' ') && !s.startsWith('-')) return s
  return JSON.stringify(s)
}

const list = (xs: string[]) => `[${xs.map(yamlStr).join(', ')}]`

/** StepKeys are the keys the editor owns on a step, in the order it writes new ones; anything else in a file is left alone (WU-036). */
export const STEP_KEYS = ['image', 'validator', 'external', 'run', 'needs', 'env', 'secrets', 'artifacts', 'cache', 'timeout', 'retries', 'allow_failure', 'reviewer', 'advisory', 'effort', 'when'] as const
export type StepKey = (typeof STEP_KEYS)[number]
export type StepValue = string | number | boolean | string[] | Record<string, string> | { paths: string[] }

/** desired lists the keys a step writes, as plain values, in STEP_KEYS order; toYaml and the text writer both use it. */
export function desired(s: PStep, plainIds: string[]): Partial<Record<StepKey, StepValue>> {
  const d: Partial<Record<StepKey, StepValue>> = {}
  // An external validator runs no container: no image, no commands, no
  // env, secrets, cache or retries; a reviewer outside answers it.
  if (!s.external) d.image = s.image
  if (s.validator) d.validator = true
  if (s.external) d.external = true
  if (!s.external && s.run.length === 1) d.run = s.run[0]
  else if (!s.external && s.run.length > 1) d.run = [...s.run]
  const needs = s.validator ? plainIds : s.needs
  if (needs.length) d.needs = [...needs]
  const env = s.external ? [] : s.env.filter((e) => !e.secret && e.k)
  if (env.length) d.env = Object.fromEntries(env.map((e) => [e.k, e.v]))
  const secrets = s.external ? [] : s.env.filter((e) => e.secret && e.k).map((e) => e.k)
  if (secrets.length) d.secrets = secrets
  if (s.evidence.length) d.artifacts = [...s.evidence]
  if (s.cache.length && !s.validator) d.cache = [...s.cache]
  if (s.timeout) d.timeout = s.timeout
  if (s.retries && !s.external) d.retries = s.retries
  if (s.allowFail && !s.validator) d.allow_failure = true
  if (s.reviewer && s.validator) d.reviewer = s.reviewer
  if (s.advisory && s.validator) d.advisory = true
  if (s.effort && s.validator && !s.external) d.effort = s.effort
  if (s.paths.length && !s.validator) d.when = { paths: [...s.paths] }
  return d
}

/** stageLists gives the stages to write: name and member ids in file order, empty stages dropped. */
export function stageLists(p: PModel): [string, string[]][] {
  return p.stageOrder.map((g) => [g, plain(p).filter((s) => s.group === g).map((s) => s.id)] as [string, string[]]).filter(([, ids]) => ids.length)
}

/** toYaml writes the model as a format v1 file from scratch. Validators go last and need every plain step. The text writer (yamlEdit) is preferred when the served file is at hand. */
export function toYaml(p: PModel): string[] {
  const out = ['version: 1', 'steps:']
  const plainIds = plain(p).map((s) => s.id)
  const block = (s: PStep) => {
    out.push(`  - name: ${s.id}`)
    const d = desired(s, plainIds)
    for (const key of STEP_KEYS) {
      const v = d[key]
      if (v === undefined) continue
      if (key === 'run' && Array.isArray(v)) {
        out.push('    run:')
        for (const r of v) out.push(`      - ${yamlStr(r)}`)
      } else if (key === 'env') {
        out.push('    env:')
        for (const [k, val] of Object.entries(v as Record<string, string>)) out.push(`      ${k}: ${yamlStr(val)}`)
      } else if (key === 'when') {
        out.push('    when:')
        out.push(`      paths: ${list((v as { paths: string[] }).paths)}`)
      } else if (Array.isArray(v)) out.push(`    ${key}: ${list(v)}`)
      else if (typeof v === 'string') out.push(`    ${key}: ${yamlStr(v)}`)
      else out.push(`    ${key}: ${String(v)}`)
    }
  }
  for (const s of plain(p)) block(s)
  for (const s of validators(p)) block(s)
  const stages = stageLists(p)
  if (stages.length) {
    out.push('stages:')
    for (const [g, ids] of stages) out.push(`  ${g}: ${list(ids)}`)
  }
  return out
}

/** renderYaml is the text the server receives: the served file edited in place when there is one (WU-036), else the file written from scratch. */
export function renderYaml(p: PModel, original: string | null): string {
  return original ? applyModel(original, p) : toYaml(p).join('\n') + '\n'
}

function renderLines(p: PModel, original: string | null): string[] {
  return renderYaml(p, original).replace(/\n$/, '').split('\n')
}

/** YamlLine is one line of the YAML view, marked when it is not in the original. */
export interface YamlLine {
  n: number
  t: string
  added: boolean
}

export function yamlDiff(p: PModel, base: PModel | null, original: string | null = null): YamlLine[] {
  const pool: Record<string, number> = {}
  if (base) for (const l of renderLines(base, original)) pool[l] = (pool[l] || 0) + 1
  return renderLines(p, original).map((t, i) => {
    const added = !(pool[t] > 0)
    if (!added) pool[t]--
    return { n: i + 1, t, added }
  })
}

// ---- diff summary ----

export function diffSummary(a: PModel, b: PModel): string[] {
  const out: string[] = []
  const A = Object.fromEntries(a.steps.map((s) => [s.id, s]))
  const B = Object.fromEntries(b.steps.map((s) => [s.id, s]))
  for (const id of Object.keys(B)) if (!A[id]) out.push(`added ${id}${B[id].needs.length ? ` after ${B[id].needs.join(', ')}` : ''}`)
  for (const id of Object.keys(A)) if (!B[id]) out.push(`removed ${id}`)
  for (const id of Object.keys(B)) {
    const sa = A[id]
    const sb = B[id]
    if (!sa) continue
    if (sa.validator !== sb.validator) {
      out.push(sb.validator ? `${id} is now a validator` : `${id} is no longer a validator`)
      continue
    }
    const ch: string[] = []
    if (!sb.validator && sa.needs.join() !== sb.needs.join()) ch.push(sb.needs.length ? `needs ${sb.needs.join(', ')}` : 'needs nothing')
    if (sa.image !== sb.image && !sb.external) ch.push(`image ${sb.image}`)
    if (sa.timeout !== sb.timeout) ch.push(sb.timeout ? `timeout ${sb.timeout}` : 'default timeout')
    if (sa.retries !== sb.retries) ch.push(`retries ${sb.retries}`)
    if (sa.run.join('\n') !== sb.run.join('\n')) ch.push('commands')
    if (sa.allowFail !== sb.allowFail) ch.push(sb.allowFail ? 'allowed to fail' : 'must pass')
    if (sa.external !== sb.external) ch.push(sb.external ? 'external' : 'runs in the pipeline')
    if (sa.advisory !== sb.advisory) ch.push(sb.advisory ? 'advisory' : 'counts')
    if (sa.reviewer !== sb.reviewer) ch.push(sb.reviewer ? `reviewer ${sb.reviewer}` : 'default reviewer')
    if (sa.effort !== sb.effort) ch.push(sb.effort ? `effort ${sb.effort}` : "profile's effort")
    if (JSON.stringify(sa.env) !== JSON.stringify(sb.env)) ch.push('env')
    if (sa.evidence.join() !== sb.evidence.join()) ch.push('evidence')
    if (sa.cache.join() !== sb.cache.join()) ch.push(sb.cache.length ? `caches ${sb.cache.join(', ')}` : 'no caches')
    if (sa.paths.join() !== sb.paths.join()) ch.push(sb.paths.length ? `only if ${sb.paths.join(', ')}` : 'always runs')
    if ((sa.group || '') !== (sb.group || '')) ch.push(sb.group ? `in stage ${sb.group}` : 'no stage')
    if (ch.length) out.push(`${id}: ${ch.join(', ')}`)
  }
  return out
}

/** saveDefaults proposes the branch, title and intent for the save form. */
export function saveDefaults(repoName: string, summary: string[]): { branch: string; title: string; intent: string } {
  const slug = (summary[0] || 'update').replace(/[^a-z0-9]+/gi, '-').toLowerCase().slice(0, 30).replace(/-$/, '')
  return {
    branch: `pipeline/${slug}`,
    title: `Pipeline: ${summary.slice(0, 2).join('; ')}`,
    intent: `Change the ${repoName} pipeline: ${summary.join('; ')}. No other file changes.`,
  }
}

// ---- band layout (design 2a) ----

export const CW = 360
export const CH = 60
export const BGX = 40
export const BGY = 10
const HUES = [200, 330, 70, 150, 250, 20]

export interface Hue {
  fg: string
  bg: string
  chip: string
  line: string
}

export function hueOf(i: number): Hue {
  const h = HUES[i % HUES.length]
  return {
    fg: `light-dark(oklch(0.47 0.1 ${h}), oklch(0.8 0.09 ${h}))`,
    bg: `light-dark(oklch(0.983 0.007 ${h}), oklch(0.19 0.012 ${h}))`,
    chip: `light-dark(oklch(0.93 0.04 ${h}), oklch(0.3 0.05 ${h}))`,
    line: `light-dark(oklch(0.7 0.09 ${h}), oklch(0.58 0.09 ${h}))`,
  }
}

export const VALIDATOR_HUE: Hue = { fg: 'var(--text-2)', bg: 'var(--surface)', chip: 'var(--surface-2)', line: 'var(--border-strong)' }

export interface Chip {
  t: string
  fg: string
  bc: string
  bg: string
  title?: string
}

export interface Card {
  id: string
  x: number
  y: number
  image: string
  cmd: string
  last: string
  hasLast: boolean
  lastStatus: string
  sel: boolean
  chips: Chip[]
  validator: boolean
}

export interface Edge {
  d: string
  hot: boolean
  ex: number
  ey: number
}

export interface Band {
  key: string
  n: string
  name: string
  sub: string
  est: string
  hue: Hue
  w: number
  h: number
  edges: Edge[]
  cards: Card[]
  validators: boolean
}

interface Placed {
  col: number
  row: number
  lp: string[]
}

/** layoutBand places a band's steps on the grid: chains on one row, forks on new rows, loose steps in the gaps. */
export function layoutBand(members: PStep[], sel: Set<string>, chainAll: boolean): { place: Record<string, Placed>; edges: Edge[]; w: number; h: number; est: number } {
  const ids = new Set(members.map((s) => s.id))
  const place: Record<string, Placed> = {}
  const occ: Set<number>[] = []
  const edges: Edge[] = []
  const LP: Record<string, string[]> = Object.fromEntries(members.map((s, i) => [s.id, chainAll ? (i ? [members[i - 1].id] : []) : s.needs.filter((n) => ids.has(n))]))
  const kids = (id: string) => members.filter((s) => LP[s.id].includes(id))
  const GRID = 3
  const put = (id: string, col: number, row: number) => {
    while (occ.length <= row) occ.push(new Set())
    occ[row].add(col)
    place[id] = { col, row, lp: LP[id] }
  }
  const walk = (id: string) => {
    for (const k of kids(id)) {
      if (place[k.id] || !LP[k.id].every((n) => place[n])) continue
      const col = 1 + Math.max(...LP[k.id].map((n) => place[n].col))
      const pr = place[LP[k.id][0]].row
      put(k.id, col, occ[pr].has(col) ? occ.length : pr)
      walk(k.id)
    }
  }
  const roots = members.filter((s) => !LP[s.id].length)
  for (const s of roots) {
    if (kids(s.id).length) {
      put(s.id, 0, occ.length)
      walk(s.id)
    }
  }
  for (const s of members) {
    if (!place[s.id] && LP[s.id].length) {
      const col = 1 + Math.max(0, ...LP[s.id].filter((n) => place[n]).map((n) => place[n].col))
      put(s.id, col, occ.length)
      walk(s.id)
    }
  }
  const width = Math.max(GRID, ...Object.values(place).map((q) => q.col + 1))
  for (const s of roots) {
    if (place[s.id]) continue
    let r = 0
    let c = -1
    for (; r < occ.length; r++) {
      for (let k = 0; k < width; k++) {
        if (!occ[r].has(k)) {
          c = k
          break
        }
      }
      if (c >= 0) break
    }
    if (c < 0) {
      r = occ.length
      c = 0
    }
    put(s.id, c, r)
  }
  for (const s of members) {
    const q = place[s.id]
    const x = q.col * (CW + BGX)
    const y = q.row * (CH + BGY)
    for (const n of q.lp) {
      const a = place[n]
      const ax = a.col * (CW + BGX) + CW
      const ay = a.row * (CH + BGY) + CH / 2
      const by = y + CH / 2
      const hot = sel.has(s.id) || sel.has(n)
      edges.push({ d: ay === by ? `M${ax} ${ay} L${x - 3} ${by}` : `M${ax} ${ay} C${ax + 24} ${ay} ${x - 24} ${by} ${x - 3} ${by}`, hot, ex: x - 3, ey: by })
    }
  }
  const cols = Math.max(0, ...members.map((s) => place[s.id].col)) + 1
  const memo: Record<string, number> = {}
  const chain = (id: string): number => (memo[id] ??= (members.find((x) => x.id === id)!.last || 0) + (place[id].lp.length ? Math.max(...place[id].lp.map(chain)) : 0))
  const est = members.length ? Math.max(...members.map((s) => chain(s.id))) : 0
  return { place, edges, w: Math.max(1, cols) * (CW + BGX) - BGX, h: Math.max(1, occ.length) * (CH + BGY) - BGY, est }
}

const approxWidth = (t: string) => t.length * 6.7 + 16

/** cardOf builds a card with the chips that fit; the rest collapse into +N. */
function cardOf(s: PStep, q: Placed, bandOf: Record<string, number>, sel: Set<string>): Card {
  const plainChip = (t: string): Chip => ({ t, fg: 'var(--text-2)', bc: 'var(--border)', bg: 'transparent' })
  const chips: Chip[] = []
  if (!s.validator) {
    for (const n of s.needs) {
      if (bandOf[n] !== bandOf[s.id] && bandOf[n] != null) {
        const h2 = hueOf(bandOf[n])
        chips.push({ t: `after ${n}`, fg: h2.fg, bc: h2.line, bg: h2.chip })
      }
    }
  }
  if (s.timeout) chips.push(plainChip(s.timeout))
  if (s.retries) chips.push(plainChip(`retries ${s.retries}`))
  if (s.allowFail) chips.push(plainChip('allowed to fail'))
  if (s.external) chips.push(plainChip('external'))
  if (s.advisory) chips.push(plainChip('advisory'))
  if (s.reviewer) chips.push(plainChip(s.reviewer))
  if (s.effort) chips.push(plainChip(`effort ${s.effort}`))
  if (s.paths.length) chips.push(plainChip(`if ${s.paths.join(', ')}`))
  if (s.evidence.length) chips.push(plainChip(`${s.evidence.length} evidence`))
  if (s.cache.length) chips.push(plainChip(`${s.cache.length} cache${s.cache.length > 1 ? 's' : ''}`))
  const secrets = s.env.filter((e) => e.secret).length
  if (secrets) chips.push(plainChip(`${secrets} secret${secrets > 1 ? 's' : ''}`))
  const budget = 210
  const shown: Chip[] = []
  let used = 0
  chips.forEach((c, i) => {
    const rest = chips.length - i - 1
    const more = rest ? approxWidth(`+${rest}`) : 0
    if (shown.length === i && used + approxWidth(c.t) + more <= budget) {
      shown.push(c)
      used += approxWidth(c.t)
    }
  })
  if (shown.length < chips.length) {
    const hidden = chips.slice(shown.length)
    shown.push({ ...plainChip(`+${hidden.length}`), title: hidden.map((c) => c.t).join(' · ') })
  }
  return {
    id: s.id,
    x: q.col * (CW + BGX),
    y: q.row * (CH + BGY),
    image: s.image,
    cmd: s.run.length ? `$ ${s.run[0]}${s.run.length > 1 ? `  +${s.run.length - 1}` : ''}` : '',
    hasLast: s.last != null,
    last: s.last != null ? fmtSeconds(s.last) : 'not run yet',
    lastStatus: s.lastStatus,
    sel: sel.has(s.id),
    chips: shown,
    validator: s.validator,
  }
}

/** bands groups steps into stage bands: named stages and unnamed waves ordered by first wave, validators last. */
export function bands(p: PModel, sel: Set<string>): Band[] {
  const L = levels(p)
  const ord = ordered(plain(p), L)
  const bmap: Record<string, PStep[]> = {}
  for (const s of ord) {
    const k = s.group ? `g:${s.group}` : `~${L[s.id]}`
    ;(bmap[k] ??= []).push(s)
  }
  const minL = (k: string) => Math.min(...bmap[k].map((s) => L[s.id]))
  const keys = Object.keys(bmap).sort((a, b) => {
    const d = minL(a) - minL(b)
    if (d) return d
    if ((a[0] === '~') !== (b[0] === '~')) return a[0] === '~' ? -1 : 1
    return a < b ? -1 : 1
  })
  const bandOf: Record<string, number> = {}
  keys.forEach((k, i) => bmap[k].forEach((s) => (bandOf[s.id] = i)))
  const out: Band[] = keys.map((k, i) => {
    const members = bmap[k]
    const hue = hueOf(i)
    const lay = layoutBand(members, sel, false)
    const after = Array.from(new Set(members.flatMap((s) => s.needs.filter((n) => bandOf[n] !== i && bandOf[n] != null).map((n) => bandOf[n] + 1)))).sort((a, b) => a - b)
    const named = k.startsWith('g:')
    return {
      key: k,
      n: String(i + 1),
      name: named ? k.slice(2) : members.slice(0, 2).map((s) => s.id).join(' + ') + (members.length > 2 ? ` +${members.length - 2}` : ''),
      sub: `${members.length} step${members.length === 1 ? '' : 's'}${named ? '' : ' · unnamed'} · ${after.length ? `starts after stage ${after.join(', ')}` : 'starts first'}`,
      est: `~${fmtSeconds(lay.est)}`,
      hue,
      w: lay.w,
      h: lay.h,
      edges: lay.edges,
      cards: members.map((s) => cardOf(s, lay.place[s.id], bandOf, sel)),
      validators: false,
    }
  })
  const vals = validators(p)
  if (vals.length) {
    const lay = layoutBand(vals, sel, true)
    out.push({
      key: 'validators',
      n: 'V',
      name: 'validators',
      sub: 'always last · run in this order after every stage',
      est: `~${fmtSeconds(lay.est)}`,
      hue: VALIDATOR_HUE,
      w: lay.w,
      h: lay.h,
      edges: lay.edges,
      cards: vals.map((s) => cardOf(s, lay.place[s.id], bandOf, sel)),
      validators: true,
    })
  }
  return out
}
