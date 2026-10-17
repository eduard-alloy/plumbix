// The design-system reference: tokens, type, the state encoding table and
// the components, in light and dark side by side. Reached by URL only.
import { ENCODING, StatePill, type PillStatus } from '../ui/StatePill'

const TOKENS = ['--bg', '--surface', '--surface-2', '--hover', '--border', '--border-strong', '--text', '--text-2', '--text-3', '--accent', '--accent-bg', '--s-neutral', '--s-blue', '--s-green', '--s-amber', '--s-red', '--s-violet', '--diff-add', '--diff-del', '--hit', '--hit-strong']

const STATUSES = Object.keys(ENCODING) as PillStatus[]

function Pane({ theme }: { theme: 'light' | 'dark' }) {
  return (
    <div className="theme-pane" data-theme={theme}>
      <h2 className="ds-title">{theme === 'light' ? 'Light' : 'Dark'}</h2>

      <section className="ds-section">
        <h3>Tokens</h3>
        <div className="swatches">
          {TOKENS.map((t) => (
            <div key={t} className="swatch">
              <span className="chipbox" style={{ background: `var(${t})` }} />
              <span className="mono">{t}</span>
            </div>
          ))}
        </div>
      </section>

      <section className="ds-section">
        <h3>Type</h3>
        <div className="type-scale">
          <div style={{ font: '600 20px/26px var(--ui)', letterSpacing: '-0.01em' }}>Title 20/26 600</div>
          <div style={{ font: '14px/1.55 var(--ui)' }}>Prose 14/1.55. Intent reads like a paragraph, up to 74 characters wide.</div>
          <div style={{ font: '500 14px var(--ui)' }}>Criterion 14/500</div>
          <div style={{ font: '13px var(--ui)' }}>Body 13</div>
          <div style={{ font: '600 13px var(--ui)' }}>Section 13/600</div>
          <div className="label">Label 12/600</div>
          <div className="mono" style={{ fontSize: 13 }}>
            mono 13 · c_k7m2q9x4w1zp · 9f3c1e2
          </div>
          <div className="mono" style={{ fontSize: 12 }}>
            mono 12 · feat/retry-uploads → main
          </div>
          <div className="mono dim" style={{ fontSize: 11 }}>
            mono 11 · 1m 58s
          </div>
        </div>
      </section>

      <section className="ds-section">
        <h3>States</h3>
        <div className="encoding">
          {STATUSES.map((s) => (
            <div key={s} className="encoding-row">
              <StatePill status={s} />
              <StatePill status={s} mode="plain" />
              <StatePill status={s} mode="icon" />
              <StatePill status={s} mode="text" />
              <span className="mono dim">{s}</span>
              <span className="mono dim">{ENCODING[s].palette}</span>
            </div>
          ))}
        </div>
      </section>

      <section className="ds-section">
        <h3>Components</h3>
        <div className="ds-row">
          <button className="primary">Merge</button>
          <button className="secondary">Close</button>
          <button className="secondary small">Load diff</button>
          <button className="quiet">Resolve</button>
          <button className="link">evidence/test/log.txt</button>
        </div>
        <div className="filters ds-box">
          <div className="search">
            <input placeholder="Filter by title, id or branch" aria-label="Sample filter" readOnly />
            <span className="kbd" aria-hidden="true">
              /
            </span>
          </div>
          <div role="group" aria-label="Sample repository" className="segmented">
            <button aria-pressed="true">All repositories</button>
            <button aria-pressed="false">plumbix</button>
            <button aria-pressed="false">ledger-api</button>
          </div>
          <select aria-label="Sample state" defaultValue="all">
            <option value="all">All states</option>
          </select>
        </div>
        <div className="ds-box">
          <div className="row row-grid" style={{ height: 36 }}>
            <span className="cell">
              <StatePill status="approved" />
            </span>
            <span className="cell title">Retry failed artifact uploads</span>
            <span className="cell mono dim2">
              plumbix<span className="dim"> · </span>feat/retry-uploads<span className="dim"> → </span>main
            </span>
            <span className="cell mono dim2">3/3 met · 0 blockers</span>
            <span className="cell mono dim right">2</span>
            <span className="cell dim2">kai</span>
            <span className="cell mono dim right">4m</span>
          </div>
        </div>
        <div className="ds-box">
          <div className="criterion">
            <span className="icon">
              <StatePill status="met" mode="icon" />
            </span>
            <span className="acid">AC-1</span>
            <div className="body">
              <div className="head">
                <span className="text">A transient upload error is retried up to three times</span>
                <span className="check">check: step test</span>
              </div>
              <div className="assessment">
                <StatePill status="met" mode="text" />
                <span>TestUploadRetriesThrice covers three attempts with backoff</span>
                <span className="by">ai-review</span>
                <button className="link mono">evidence/ai-review/review.json</button>
                <button className="toggle">+1 validator</button>
              </div>
            </div>
          </div>
          <div className="finding">
            <span className="icon">
              <StatePill status="blocker" mode="icon" />
            </span>
            <div className="body">
              <span>Expiry is parsed from the token name instead of calling the API</span>
              <span className="meta">
                <StatePill status="blocker" mode="text" />
                <span className="mono">ai-review</span>
                <button className="link mono">AC-1</button>
              </span>
            </div>
            <button className="link mono loc">internal/hosts/azure/token.go:112</button>
          </div>
          <div className="step">
            <StatePill status="succeeded" mode="icon" />
            <span className="name">test</span>
            <span />
            <StatePill status="succeeded" mode="text" />
            <span className="mono dim">exit 0</span>
            <span className="mono dim2 right">1m 58s</span>
            <span className="log">Log</span>
          </div>
          <button className="iteration first last" aria-pressed="true">
            <span className="line" />
            <span className="dot">
              <StatePill status="approved" mode="icon" />
            </span>
            <span>Iteration 2</span>
            <StatePill status="approved" mode="text" />
            <span className="headline">All criteria met</span>
            <span className="mono dim">9f3c1e2</span>
            <span className="mono dim2 right">4m 00s</span>
            <span className="mono dim right">4m</span>
          </button>
        </div>
        <div role="note" className="notice" style={{ marginTop: 12 }}>
          <span className="icon">
            <StatePill status="info" mode="icon" />
          </span>
          <div className="text">
            <span className="title">Not merged: main moved</span>
            <span className="body">main moved since the verdict, which was for base a81d7c0. Bring main into the branch and push. Plumbix validates the new head.</span>
            <code className="cmd">git fetch origin && git merge origin/main && git push</code>
          </div>
          <div className="buttons">
            <button className="secondary small">Copy command</button>
            <button className="dismiss" aria-label="Dismiss sample">
              ×
            </button>
          </div>
        </div>
        <div className="empty" style={{ padding: '24px 0 0' }}>
          <h2>No repositories yet</h2>
          <p>Plumbix validates changes for repositories you register from the terminal.</p>
        </div>
      </section>
    </div>
  )
}

export function DesignPage() {
  return (
    <div className="design">
      <Pane theme="light" />
      <Pane theme="dark" />
    </div>
  )
}
