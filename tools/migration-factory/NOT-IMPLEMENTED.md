# NOT-IMPLEMENTED: migration factory (PSG-49, wave 49)

Slice 1 covers the pack lifecycle, control totals, validation and quarantine, idempotent loading
through `legacy_xref`, the parallel-run classifier, the cutover plan with its sequencing and rollback
rules, the Postgres store (row-level security, outbox events, idempotency keys, plan versions), the
open_items and cutover API, the web screens, and the chart. Every other deliverable and acceptance
criterion is listed here with its status.

Status values: `later slice of PSG-49` (planned in this wave) or `handoff` (needs an owner outside
this wave's paths).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Transactional packs: open_items domain, validation and control totals | Partly built | The open_items rules, totals and API are built and stored. The run takes the extract in the request body (16 MiB limit); "loaded" means recorded in `legacy_xref` with the id the factory assigns. Posting the opening open items into payables and receivables (against the opening-balance clearing account, BR-49.2) is `handoff` to waves 13/14: their bulk opening-item API does not exist yet. |
| D1e | open_items control total in accounting currency, and the hash of (legacy id, amount) as a compared total (FR-49.2) | later slice of PSG-49 | The extract record has no accounting-currency amount yet. The hash is computed and stored as the run's `input_hash`, not compared as a control total. |
| D1a | Packs gl_balances, onhand, open_orders, wip_prod, fixed_assets, project_wip, payroll_ytd, bank_balances | later slice of PSG-49 | Need their extractors, mapping and per-pack reconciliation. The API returns MIG-0005 for them today. |
| D1b | AX and SAP extractors (AX OData/SQL, SAP RFC/ODP) | handoff | Extraction runs through the wave-35 integration hub (`integration-ts`), and the legacy connections are Vault secrets. Neither is in this wave's paths. |
| D1c | Great Expectations suites per pack (FR-49.11, FR-49.11 coverage ≥ 95 %) | later slice of PSG-49 | Slice 1 has the rules in Go (referential, domain, uniqueness). The GE suite files and the coverage check come later. |
| D2 | Control totals stored on `control_total` with timestamp and tolerance | Built | Count, amount per currency and amount per (company, account, currency). Configured tolerances (`migration.tolerance.<pack>`) are later: every total compares at 0.00 today. |
| D3 | Historical load into Iceberg `history.<system>.*` with `history.unified.*` views | later slice of PSG-49 | Depends on the lakehouse (wave 46). This wave owns the loader, which is not started. |
| D4 | Parallel-run harness that replays documents through public posting APIs | later slice of PSG-49 | Slice 1 has the diff classifier and sign-off gate only. The replay cursor and the `parallel_run` and `variance` tables are later. |
| D5 | Cutover runbook generator | Built | `GeneratePlan` covers the timeline, packs, rehearsal duration overrides, go/no-go, and the abort step at `cutover.abort_offset_h`. Plans and steps are stored with an optimistic version. PDF/CSV exports (9.49.15) are later. |
| D5a | 84-step plan fixture (`fx-cutover-plan`) | later slice of PSG-49 | Slice 1 generates about 17 steps. The per-entity expansion comes with the fixture. |
| D6 | Legacy coexistence adapters, entity by entity (FR-49.7) | later slice of PSG-49 for the factory side; handoff for the adapters | AX OData/DMF and SAP IDoc/BAPI adapters sit in the wave-35 integration hub. The `coexistence.entities` flag and BR-49.10 xref dedupe are later. |
| D7 | Project beginning balance journals (FR-49.12, `project_opening_line`) | later slice of PSG-49 | Needs the wave-41 project model. |
| D8 | Rehearsal environment provisioning, Terraform + Helm + seed (FR-49.8, AC-49.10) | handoff | Pipeline and Terraform live in `deploy/`, which this wave does not own. The chart is here. |
| D9 | Rollback plan per cutover, with re-keying files (FR-49.9) | Partly built | Rollback records the two-approver execution once, writes `dev.passage.cutover.plan.rolledback.v1`, and the plan reports when the abort deadline has passed (`abortDue`) or is within the hour (CUT-1604). Re-keying file export and the automatic execution at the deadline (BR-49.9) are later. |
| D10 | Idempotent re-runs with `legacy_xref` uniqueness (FR-49.10) | Built | Natural-key primary key per tenant, append-only for the application role, runs of one (system, company) serialized by an advisory lock. Every run is stored with its input hash. |
| D11 | Deploy: migration factory workspace and rehearsal environment | Partly built | The web module, Dockerfile and chart are here. The umbrella chart entry, the Argo CD app and the gateway route sending `/api/v1/migration/*` and `/api/v1/cutover/*` to this service are `handoff` to `deploy/` and the gateway (wave 82). |

## Acceptance criteria

| AC | Criterion | Status | Reason |
|---|---|---|---|
| AC1 | Synthetic AX dataset (3 entities, full year), all control totals MATCH | later slice of PSG-49 | Fixture `fx-ax-synth-year` (1.1M rows) is not generated. The rule logic is tested. |
| AC2 | Synthetic SAP dataset, all control totals MATCH | later slice of PSG-49 | Fixture `fx-sap-synth-year` is not generated. |
| AC3 | Cutover rehearsal inside the planned window (< 48 h) | later slice of PSG-49 | A rehearsal needs the runner and measured durations. The plan's open-access step is at T+30h, and a test checks that. |
| AC4 | Rollback rehearsal restores legacy coexistence | later slice of PSG-49 | Needs the coexistence adapters (D6). |
| AC5 | Every deliverable has code or a reason in this file | Met | This file. |
| AC6 | `go vet`, `go test`, web vitest and tsc, OpenAPI matches code, chart lints | Met for the built surface | AsyncAPI is `later slice of PSG-49`, below. |

## Acceptance criteria in PRD section 9.49.8

| AC | Status | Reason |
|---|---|---|
| AC-49.1 / AC-49.2 | later slice of PSG-49 | Need all packs and the synthetic datasets. |
| AC-49.3 | Met | `TestPackRun_QuarantinesBadRecordAndLoadsRest` (database test). |
| AC-49.4 | Met | `TestPackRun_CleanExtractReconcilesAndRerunLoadsNothing` and `TestRunOpenItems_ConcurrentRunsLoadEachRecordOnce` (database tests). |
| AC-49.5 | later slice of PSG-49 | Needs the replay harness (D4). |
| AC-49.6 | later slice of PSG-49 | Needs the rehearsal run (AC3). |
| AC-49.7 | later slice of PSG-49 | Needs the rollback re-keying export (D9). |
| AC-49.8 | later slice of PSG-49 | Needs `GET /api/v1/migration/xref` and the legacy reference panel on the invoice page in the sales module. |
| AC-49.9 | later slice of PSG-49 | Needs the history load (D3). |
| AC-49.10 | handoff | Pipeline provisioning (D8). |
| AC-49.11 | later slice of PSG-49 | Needs the project opening lines (D7). |

## API endpoints in PRD section 9.49.5 not yet served

| Endpoint | Status | Reason |
|---|---|---|
| `POST /api/v1/migration/runs/{id}/quarantine/{rec}/release` | later slice of PSG-49 | Needs the quarantine store. |
| `POST /api/v1/migration/history/loads` | later slice of PSG-49 | Needs D3. |
| `GET /api/v1/migration/xref?system=&table=&id=` | later slice of PSG-49 | Needs the durable `legacy_xref` store. |
| `POST /api/v1/migration/parallel-runs` | later slice of PSG-49 | Needs D4. |
| `POST /api/v1/migration/parallel-runs/{id}/variances/{v}/explain` | later slice of PSG-49 | Needs D4. The classifier's `Explain` is built. |
| `POST /api/v1/cutover/plans/{id}/steps/{s}/complete` | Built | Served and stored. |
| Go/no-go decision endpoint (record decision with approvers) | later slice of PSG-49 | Completing the go/no-go meeting step stands in for it; `dev.passage.cutover.decision.recorded.v1` comes with the endpoint. |
| `GET /api/v1/migration/runs/{id}/projects/{project}/opening` | later slice of PSG-49 | Needs D7. |
| Quarantine record store for release | later slice of PSG-49 | Quarantined records are stored on `pack_quarantine` with the rule, reason and the record; release and reject come with the endpoint above. |

## Events (PRD section 9.49.5)

| Event | Status | Reason |
|---|---|---|
| `dev.passage.migration.pack.reconciled.v1` | Built | Written for every run after reconciliation, with the match and mismatch counts. |
| `dev.passage.migration.parallelrun.diffed.v1` | later slice of PSG-49 | Needs D4. |
| `dev.passage.cutover.step.completed.v1` | Built | |
| `dev.passage.cutover.plan.rolledback.v1` (not in the PRD table) | Built | Added so the coexistence adapters (FR-49.7) can resume legacy write access when a rollback executes. |
| `dev.passage.cutover.decision.recorded.v1` | later slice of PSG-49 | Needs the go/no-go decision endpoint. |

`events/asyncapi.yaml` documents the three built events; a test checks it against `internal/events`.

## Persistence, security and platform

| Item | Status | Reason |
|---|---|---|
| Postgres store, migrations, RLS (`tenant_id` first, `dbx.RLSPolicy`) | Built | `migrations/0001_init.sql`: `pack_run`, `control_total`, `pack_quarantine`, `legacy_xref`, `cutover_plan`, `cutover_step`, `outbox`, `idempotency_keys`. `parallel_run`, `variance` and `project_opening_line` come with D4 and D7. |
| Transactional outbox writes | Built | Same transaction as the state change. |
| Metrics endpoint for the ServiceMonitor | later slice of PSG-49 | The chart's ServiceMonitor toggle is off by default; no `/metrics` is served yet. |
| OPA decision point replacing `DutyPolicy` | handoff | Owned by edge-identity and platform (wave 2). The routes already use the kernel `authz.RequireDuty` contract. |
| SoD checks (MIG-001: run and sign off own variances; go/no-go owner check, 9.49.17) | later slice of PSG-49 | Needs the review workflow and the variance store. |
| Tenant parameters (`migration.*`, `cutover.abort_offset_h`, `coexistence.*`) | later slice of PSG-49 | Requests carry the values today. Slice 1 has no parameter store. |
| Encryption and retention of production extracts (PRD 9.49.20, privacy) | handoff | Data-platform and deployment-cicd. Extracts are not persisted in slice 1. |
| Masked rehearsal extracts (`MASK_PII`, FR-48.26) | handoff | Wave 48 owns the masking profile. The chart sets `MASK_PII=true` as the default. |
| Freeze time from the cutover plan (MIG-1501 uses the request's `freezeTs`) | later slice of PSG-49 | Pack runs are not linked to a cutover plan yet; the link comes with runbook-driven pack steps. |
| Step completion by the step's named owner (9.49.5 "step owner") | later slice of PSG-49 | Steps carry an owner role name, and the route checks the `CutoverStepOwner` duty; matching the caller to the named owner needs owner assignment to people. The completing user is recorded on the step. |
| Rollback approvers as signed-in principals | later slice of PSG-49 | Approvers are names in the request; the requesting CutoverManager is recorded as `rolled_back_by`. A second-approver confirmation flow (FR-48.13 e-signature) is later. |

## Reports, documents and jobs

| Item | Status | Reason |
|---|---|---|
| Reconciliation certificate, opening TB sign-off, parallel-run sign-off, runbook record PDFs (9.49.15) | later slice of PSG-49 | Needs e-signature from FR-48.13 (wave 48). |
| Nightly delta sync, weekly post-go-live reconciliation (9.49.16) | later slice of PSG-49 | Needs the store and the coexistence adapters. |
| Alerts for MISMATCH, quarantine > 1 %, overrun > 125 % (9.49.10) | later slice of PSG-49 | Needs the metrics export. |

## Known PRD inconsistencies

- **"T+1d first postings" versus "T+30h user access".** FR-49.6 lists T+30h for user access and T+1d
  for first postings. The timeline is ordered by offset, so T+1d would fall before user access.
  Slice 1 places first postings at T+2d, after user access. The PRD owner should confirm this.
- **Open question 1 (history years 7 versus 10 for DE GoBD)** is not decided in this slice. The
  default of 7 is recorded in the chart values. Decision due at wave 50.
- **Open question 2 (payroll YTD outside US and DE)** is not decided. The `payroll_ytd` pack is
  not built.
- **GAP-36 (bills of exchange)** is deferred to wave 36. The `instrument_type` column on
  `open_items` is not in the slice-1 record shape.

## Web module (`apps/web/src/modules/migration-factory`)

| Item | Status | Reason |
|---|---|---|
| Quarantine screen and release action (9.49.6) | later slice of PSG-49 | Depends on the release endpoint. |
| Parallel-run review screen (9.49.6) | later slice of PSG-49 | Depends on D4. |
| Legacy reference panel on any record | later slice of PSG-49 | Depends on the xref endpoint and on record pages in other modules. |
| Control-total board auto-refresh every 10 s during a run | later slice of PSG-49 | A slice-1 run completes inside its request, so the board is final when it opens. Refresh comes with asynchronous (Temporal) runs. |
| Runbook live timer (elapsed against planned while a step runs) | later slice of PSG-49 | Steps record the actual duration on completion and colour amber at 110 % and red at 125 %; a start action and a running timer are later. |
| Drill from a mismatch to records, and export (9.49.6) | later slice of PSG-49 | Needs the per-record staging view. |
| Screens built | Built | Pack runs (list with filters, start form, detail with control-total board and quarantine) and cutover runbooks (list, generate form, detail with go/no-go checklist, step completion, abort warning and two-approver rollback), all on the live API. |

Web "Needs": none. The module uses `fetch`, React and the shell UI primitives only.
