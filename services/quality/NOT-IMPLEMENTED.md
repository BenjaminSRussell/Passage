# NOT-IMPLEMENTED: quality (PSG-33, wave 33)

Slice 1 builds the sampling, association, test-group, instrument, quality-order,
result and usage-decision core, with web screens for each. Everything below is a ticket
deliverable, acceptance criterion or PRD rule that this slice does not build, or builds
only in part. Status is `later slice of PSG-33` (this service, a later slice) or
`handoff` (needs another owner or a path outside this wave).

## Ticket deliverables (section numbers refer to the ticket)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Test groups, sampling plans, associations | Built, with gaps | Domain, store, API and web screens. Gaps: full Z1.4 Table I and II-B/C (see AC3); test group versions are immutable but not effective-dated (FR-33.1 note); no outcome catalog, so attribute outcomes are UUIDs the caller supplies; a test has no identity across group versions. |
| 2 | Quality orders, results (variable/attribute), usage decision, automatic inventory blocking and release | Partly built | Orders, results, evaluation and decisions are built. Hold placement and release on the inventory ledger (FR-33.5, BR-33.5) is a **handoff** to the inventory / ledger owner (wave 16 `PlaceHold` API): `block_ref` is not stored, and the ledger legs of partial decisions (BR-33.7: release accepted qty, move rejected qty to quarantine as REJECTED) are not written. Consumers can act on `usagedecision.posted`. Decision follow-ups (FR-33.6: return-to-vendor draft on wave 18, scrap journal on wave 16) are **handoffs** to those owners. |
| 3 | Nonconformance, correction/CAPA (8D) with root cause, cost of quality | later slice of PSG-33 | NCR (FR-33.7), 8D Temporal workflow (FR-33.8, BR-33.9) and cost-of-quality tagging (FR-33.12) are not built. The automatic NCR on a reject decision (`auto_ncr_on_reject`) is therefore absent, and a closed order cannot yet be amended through an NCR (BR-33.4); it answers QM-0109. |
| 4 | Certificates of analysis, supplier scorecards, SPC charts | later slice of PSG-33 | CoA (FR-33.9, BR-33.10), scorecard (FR-33.10) and SPC (FR-33.11) are not built. |
| 5 | Device/instrument calibration tracking | Partly built | Registration, due-date computation, recording a calibration (`:calibrate`), the `calibrationStatus` shown on lists (DUE_SOON at 14 days) and the `INSTRUMENT_OVERDUE` refusal (BR-33.8) are built. The QM-0103 warning as a message or alert and the daily instrument due scan job are not. `instrument_warn_days` is fixed at 14, not a site parameter. |
| 6 | Product compliance (regulated/restricted lists, BOM roll-up), SDS with distribution | later slice of PSG-33 | FR-33.13 and FR-33.14 are not built. The BOM roll-up needs wave 9 data; SDS dispatch needs the wave-28 shipment hook. |
| 7 | Incoming vendor certificates gating acceptance; source inspection | later slice of PSG-33 | FR-33.15 (BR-33.13, QM-0125) and FR-33.19 (BR-33.16) are not built. `SOURCE_INSPECTION` is accepted as a trigger value only. |
| 8 | Physical samples (retention), multiple specification sets, in-process inspection points | later slice of PSG-33 | FR-33.20, FR-33.21 (BR-33.17) and FR-33.22 (BR-33.18) are not built. |
| 9 | Stability studies, audit management, FMEA/control plans | later slice of PSG-33 | FR-33.16, FR-33.17 and FR-33.18 (BR-33.14, BR-33.15) are not built. |
| Deploy | Quality workspace enabled for demo plants; CoA generation on shipments | handoff | Enabling `quality.enabled` for demo sites and the shipment CoA hook belong to `deploy/` and the wave-28 shipment owner, outside this wave's paths. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 trigger fixtures (60 receipts, 40 RAFs, 73 orders) | later slice of PSG-33 | The `testdata/quality-triggers-100.json` fixture is not written. Precedence (BR-33.3) is covered by domain unit tests, and one order per source line and trigger by a database test. |
| AC2 hold until usage decision, property test over pick/ship/consume | handoff | The property test runs against the inventory ledger and wms; neither exists in this wave's paths. The quality side only guarantees that a decision is required to close the order. |
| AC3 AQL sizing matches Z1.4 tables | later slice of PSG-33 | The AQL 1.0 / 2.5 / 4.0 excerpt of BR-33.1 is built and tested cell by cell, including the AC-33.2 lots. Full Table I code letters for levels I, III and S-1 to S-4, Tables II-B/C (tightened and reduced), the other AQL columns and the `z14-tables.json` transcription check are not. Those plans are refused when saved (COM-0001 on `samplingPlan.*`) or sized (QM-0114). |
| AC4 CAPA workflow with effectiveness check (Temporal) | later slice of PSG-33 | No Temporal workflow exists in this slice (FR-33.8). |
| AC5 every deliverable has code or a reason here | Met | This file. |
| AC6 `go vet`/`go test`, web `vitest`/`tsc`, OpenAPI/AsyncAPI match code, chart lints | Met for the built surface | `TestSpecs_MatchRoutes` checks every route, method and duty against `openapi.yaml` and every event type against `asyncapi.yaml`. |

## PRD rules and other items

| Item | Status | Reason |
|---|---|---|
| Switching rules (BR-33.2, AC-33.8) | later slice of PSG-33 | `SwitchState` implements the rules and is tested, but it is not applied when an order is created: lot outcomes per (item, vendor, test group) are not stored, and tightened/reduced AQL plans need Tables II-B/C. Orders use the association's NORMAL severity. |
| `CreateQualityOrderFromTrigger` checks that the source line is in the required state (section 6.33.5) | handoff | The receiving, production and shipment services own their line states and will call this API from their hooks (waves 18, 21, 28). This API trusts the caller's trigger. |
| `PostUsageDecision` with `allow_incomplete` and a deviation reason | later slice of PSG-33 | Every required test needs a result before a decision (QM-0105); there is no incomplete override. |
| OPEN status (sample drawn) and CANCELLED (source reversed, hold released) | later slice of PSG-33 | No endpoint draws the sample or cancels an order; orders move from CREATED to RESULTS_IN_PROGRESS on their first results. |
| Destructive tests reduce the sample and post scrap at decision (FR-33.1, BR-33.5) | later slice of PSG-33 | `destructive` is stored on the test only. The scrap posting is a wave-16 handoff. |
| `four_eyes_decision`, signatures (21 CFR Part 11 re-authentication) | later slice of PSG-33 | Requires the platform signature facility. |
| Reconciliation: held qty in ledger = open lot qty; quarantine = rejects minus dispositions | handoff | Depends on the ledger hold (deliverable 2). |
| Alerts and scheduled jobs (scorecard monthly, instrument due scan daily, SPC hourly, orders pending decision over 5 days) | later slice of PSG-33 | Jobs depend on the features above. |
| Results throughput test (1,000 results < 2 s) | later slice of PSG-33 | Needs a DB-backed benchmark; not written. |
| CoA PDF golden snapshot | later slice of PSG-33 | No CoA in this slice. |
| Inquiries in section 6.33.14 beyond the quality order worklist; SPC dashboard; scorecard UI | later slice of PSG-33 | The web module has the order worklist, order detail with results entry and usage decision, test groups, instruments, associations and the sampling calculator. |
| Keyboard-first results grid (Tab/Enter down a sample column) | later slice of PSG-33 | Results are entered one sample at a time, a row per test, with an immediate out-of-limit hint. |
| Mobile results step (wave 32) | handoff | Owned by the mobile wave. |
| Events `dev.passage.quality.nonconformance.created.v1`, `dev.passage.quality.capa.closed.v1` | later slice of PSG-33 | Not published until NCR and CAPA exist. `usagedecision.posted` carries `decisions[]` (one per line) rather than a single `decision`, because a split decision has several. |
| Migration from AX / SAP (section 6.33.11) | later slice of PSG-33 | Migration pack belongs to the migration-framework wave. |
| Chart name | note | The chart is `charts/quality` (convention) rather than `passage-quality`; the umbrella in `deploy/` must map the name. The PRD's separate worker deployment arrives with the Temporal workflows. |
