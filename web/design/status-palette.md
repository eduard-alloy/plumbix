# Status palette

Tokens: `status-palette.css` (light in `:root`, dark under `[data-theme="dark"]`).

## Badge spec
- Pill, height 38px, radius 999px, 1.5px border, padding 0 14px 0 12px.
- Left: icon (Material Symbols Rounded, 22px). Center: label (Exo 2 600, 15px). Right: 8px dot in base color.
- Colors: `background: var(--status-X-bg); color: var(--status-X-fg); border-color: var(--status-X-border)`.
- Dark theme glow: `box-shadow: 0 0 14px <base 40%>, inset 0 0 12px <base 22%>`; dot glow `0 0 8px <base>`.
- merged and blocker use the "strong fill" tokens (denser tint), same pill shape.
- Family headings: Rajdhani 700, uppercase, letter-spacing .14em, base color of the family's first status.
- Dark page background #070b12, card #0c121c.

## Mapping
| Family | Status | Token | Base | Icon | Fill |
|---|---|---|---|---|---|
| Active | open | `--status-open` | #3B82F6 | radio_button_unchecked | tint |
| Active | running | `--status-running` | #6366F1 | play_arrow | tint |
| Active | queued | `--status-queued` | #64748B | schedule | tint |
| Active | pending | `--status-pending` | #94A3B8 | hourglass_empty | tint |
| Success | approved | `--status-approved` | #22C55E | check_circle | tint |
| Success | succeeded | `--status-succeeded` | #22C55E | check_circle | tint |
| Success | met | `--status-met` | #22C55E | task_alt | tint |
| Success | finished | `--status-finished` | #4ADE80 | flag | tint |
| Success | merged | `--status-merged` | #84CC16 | merge | strong fill |
| Attention | needs human | `--status-needs-human` | #F59E0B | person | tint |
| Attention | escalated | `--status-escalated` | #F97316 | arrow_circle_up | tint |
| Attention | errored | `--status-errored` | #EF4444 | error | tint |
| Caution | fix requested | `--status-fix-requested` | #EAB308 | build | tint |
| Caution | major | `--status-major` | #FACC15 | warning | tint |
| Failure | rejected | `--status-rejected` | #EF4444 | cancel | tint |
| Failure | failed | `--status-failed` | #EF4444 | close | tint |
| Failure | unmet | `--status-unmet` | #EF4444 | do_not_disturb_on | tint |
| Failure | timed out | `--status-timed-out` | #F97316 | schedule | tint |
| Failure | blocker | `--status-blocker` | #DC2626 | skull | strong fill |
| Stopped | closed | `--status-closed` | #64748B | power_settings_new | tint |
| Stopped | cancelled | `--status-cancelled` | #94A3B8 | cancel | tint |
| Stopped | skipped | `--status-skipped` | #CBD5E1 | skip_next | tint |
| Neutral | unknown | `--status-unknown` | #94A3B8 | help | tint |
| Neutral | minor | `--status-minor` | #A1A1AA | do_not_disturb_on | tint |
