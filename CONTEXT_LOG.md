# Plumbix Context Log

| Date | Session | Status | Note |
|------|---------|--------|------|
| 2026-10-15 | Discovery | ✅ | Q00-Q14 done. TRACE Slim adopted. PROJECT.md, CONSTRAINTS.md, LICENSE created. Repo on GitHub. Q03 revised: GitHub + Azure DevOps in v1. |
| 2026-10-15 | Architecture | ✅ | ARCHITECTURE.md baseline: components, contracts, flows, decisions A1-A11. AI validator approved (WU-006), .env for secrets. Next: spec WU-001. |
| 2026-10-15 | WU-001 | ✅ | Skeleton: plumbix serve (loopback only), health endpoint, config precedence, make check. 17 tests pass. Next: WU-002 pipeline format. |
| 2026-10-16 | WU-002 | ✅ | Pipeline format v1: node-walking parser with path+line errors, JSON Schema, contract test (26 files), check endpoint. Next: WU-003 container runner. |
| 2026-10-16 | WU-003 | ✅⚠️ | Docker runner + scheduler, parallel steps, timeouts, cleanup. Security finding: Docker Desktop containers reach host loopback API. Next: owner decision on auth, then WU-004. |
| 2026-10-16 | Kai journey | ✅ | Created Kai plan: workstream F8QK66, milestones, WU-001..012 tasks, security decision task Z0GM6N. Recorded 3 superseding decisions. CLAUDE.md now requires keeping Kai in sync. |
| 2026-10-16 | WU-013 | ✅ | API token (option A): token file 0600, bearer auth on all routes but health, container probe gets 401 on every route. Next: WU-004. |
| 2026-10-16 | WU-004 | ✅ | Repos and changes: SQLite store, rules with full problem lists, service, API, OpenAPI contract test, CLI (repo/submit/change). 74 tests. Next: WU-005. |
| 2026-10-16 | WU-005 | ✅ | Verdict + input contracts with contract tests, Combine rules, reference validator (host tests + Docker image run read-only). 105 tests. Next: WU-006 AI validator. |
| 2026-10-16 | WU-006 | ✅ | AI validator: SDK call with structured output + fallbacks, untrusted block, normalization, secrets loader, image; live: injection not obeyed. 128 tests. Next: WU-007. |
