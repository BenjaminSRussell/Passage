# Testing and Reconciliation

## Purpose & Boundaries
Cross-cutting test strategy and CI gates: unit/property/golden/contract/integration/reconciliation/E2E/a11y/performance/security/observability/eval. PRD (Appendix I) wins over Moonaan defaults. Includes the `reconcile` Go module (library used by each context's `/reconciliation/{check}` endpoint, not deployed in a TS service), `reconcile-daily` (02:00 UTC on stage, and every merge to main against fixture DBs), `e2e/`, `perf/`, `fixtures/`, `contracts/`, and the wave-30 go-live simulation harness (`e2e/golive-sim/`).

## Interfaces
- Reconciliation contract: `GET /v1/<context>/reconciliation/{check}?le=&fy=&glSeq=<W>` -> `{ check, asOf, total, currency, glSeq, explainedItems[] }`. Ledger side totals lines with `gl_seq <= W`; subledgers total documents whose stored `journal_entry_id` maps to `gl_seq <= W` plus open valuation reservations as explained items; REC-08/REC-10 treat an item older than 2 business days as a break. No outbox LSN. Cumulative cut: closing balance of last closed fiscal year + current-year movements (P&L checks use movements only). Runs in `reconciliation-run` Temporal workflow in `platform-ts`; emits `ledger.reconciliation.completed.v1`.
- 19 checks REC-01..REC-19 (trial balance, balance projection, parallel ledgers, tax, statements, audit chain, inventory quantity, GR/IR, AP, inventory value, WIP, AR/deferred revenue, bank, fixed assets, cost accounting, intercompany, projects, payroll, semantic layer); tolerance 0.00/0 everywhere; registered from their wave onward.
- Proof tags: Go sub-test prefix `W14P2/<case>`, vitest/Playwright `@proof:w14-2`, pytest `pytest.mark.proof("w14-2")`; `proof-map` job vs `roadmap/waves.json`.
- Merge gates M-01..M-24, deploy gates D-01..D-15, wave-exit gates W-01..W-11 (e.g. M-06 coverage: new lines >= 80%, Go `internal/domain` >= 85%, TS/Py >= 80%, critical >= 90%, overall >= 70%).

## Data Ownership
absent — the source document does not address this (fixtures under `fixtures/seed/<demo-company>/` and `fixtures/<context>/w<NN>-<slug>/`; demo companies USMF, DEMF, GBSI, MXDS; seed tiers tiny/standard/scale).

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this (E2E helpers may observe events via test consumers).

## Dependencies
internal: every context; platform (`reconciliation-run`), deployment-cicd. external: Playwright + @axe-core/playwright, k6, Pact (in-repo pact files `contracts/pacts/<consumer>__<provider>.json`), Spectral, oasdiff, Testcontainers, rapid/fast-check/hypothesis, Lighthouse CI, Storybook, MSW, promtool, gitleaks, cosign, Grype.

## Gotchas & Constraints
- Property tests: min 1,000 generated cases on PR, 10,000 nightly; invariants declared in `PROPERTIES.md` per engine; committed shrunken counterexamples.
- Golden files: update only via `make update-golden PKG=<path>`, label `golden-update`, non-author CODEOWNERS approval; PDFs rasterised 0.1% tolerance.
- Accountant-reviewed finance fixtures (`REVIEW.yaml`) for waves 11-15, 18-20, 29, 36-41, 43; reviewer unassigned (blocks wave 11 close).
- axe tags `wcag2a, wcag2aa, wcag21a, wcag21aa`; critical/serious fail build; manual screen-reader passes per phase-exit wave; never claim "accessible" from green axe alone.
- Playwright: POM required, selector priority getByRole > getByLabel > getByText > getByTestId; every mutating call carries Idempotency-Key from testInfo.testId+step+retry; two CI retries only; quarantine max one wave.
- Break-injection suite proves each REC check detects a seeded defect; differential oracle (`decimal.js`, no shared code with Go engines) in wave-30 simulation (22-day month, ~1,000 sales orders, fault injection days 7/12/15, 2% duplicate requests).
- Health paths `/healthz` `/readyz`; Pact files in-repo (no broker, OQ-9); PII canary `PII-CANARY-<runid>` checks logs/traces/Kafka.
- Open: business-date control on job APIs (OQ-5), env beyond k3d+stage (OQ-8), paging channel (OQ-12).

## Source Sections
- Testing Strategy (5876-6747): T0-T1 (5882-5963), T2 (5967-6022), T3 (6026-6106), T4 (6110-6163), T5 (6167-6226), T6 (6228-6283), T7 (6286-6521), T8 (6524-6545), T9 (6549-6574), T10 (6578-6693), T11-T12 (6696-6747); Backend 5.20 reconciliation contract (1004)
