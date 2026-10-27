# NOT-IMPLEMENTED — revrec (PSG-29, wave 29, slice 1)

Slice 1 builds the pure ASC 606 allocation and ratable schedule with a stateless
preview endpoint. Everything below is outstanding. Status is `later slice of PSG-29`
(same service, later build) or `handoff` (needs another owner).

| # | Ticket item / acceptance criterion | Status | Reason |
|---|---|---|---|
| 1 | D2 Revenue contracts and performance obligations persisted per order (FR-29.7, `revenue_contract`, `performance_obligation`) | later slice of PSG-29 | Store, migrations and RLS for contracts not yet built; slice 1 is a preview |
| 2 | D2 `:allocate` custom method and SSP adjustments (`POST /revrec/contracts/{id}:allocate`, RevRecMaintain) | later slice of PSG-29 | Depends on item 1 |
| 3 | D2 Milestone satisfaction `POST /revrec/milestones/{ob}:satisfy` (FR-29.8 MILESTONE) | later slice of PSG-29 | Depends on item 1 |
| 4 | D2 Recognition runs `POST /revrec/recognition-runs` and nightly preview (FR-29.8, `revenue_schedule`, Dr 2400 / Cr 4000) | later slice of PSG-29 (runs) + handoff (ledger postings via ledger.Post, wave 11) | Runs need the store and the ledger client |
| 5 | D2 Usage recognition (FR-29.8 USAGE) | later slice of PSG-29 | Usage ingestion is billing (wave 29 subscription slice) |
| 6 | D2 Roll-forward `GET /revrec/roll-forward?period=` tied to GL 2400 (FR-29.9, AC-29.4) | later slice of PSG-29 | Needs persisted schedules and the GL 2400 reconciliation |
| 7 | D2 SSP source order resolution (BR-29.2: EXPLICIT, LIST, PCT_OF_LIST, RESIDUAL lookup) | handoff | Price lookup needs the pricing context (PRICE access, wave 26). The preview accepts the resolved SSP and source label; it does not look them up |
| 8 | D2 BIL-0009 warning "SSP missing; residual method used" | later slice of PSG-29 | The warning belongs with the persisted contract |
| 9 | D2 Deferred revenue postings and the `invoice.posted` consumer (revrec consumes billing lines → obligations) | later slice of PSG-29 | Consumer needs the inbox (kernel) and the store |
| 10 | D2 Credit-note reversal of recognized and deferred amounts (BR-29.6) | later slice of PSG-29 | Depends on items 1 and 4 |
| 11 | D2 Expense deferral recognition (FR-29.15, `kind = EXPENSE`) | handoff | Owned by the revrec schedule run in a later slice; AP inputs from wave 19 |
| 12 | AC-29.2 bundle allocation and first postings to the cent | met in part | The allocation (7,200.00 / 1,350.00 / 2,250.00) and the support schedule are tested; the invoice postings (Dr 1200, Cr 4000, Cr 2400) are billing and ledger work |
| 13 | AC-29.3 40/40 ASC 606 fixtures against the accountant-reviewed workbook `fixtures/revrec/asc606-40.xlsx` | handoff | The workbook is produced by the accountant reviewer, not in this repo; the slice tests the BR-29.1 example and residual cases only |
| 14 | §5.29.9 fixture `asc606-40` under `testdata/` | handoff | Same as item 13 |
| 15 | §5.29.9 reconciliation: roll-forward = GL 2400 (nightly) | later slice of PSG-29 | Needs items 4 and 6 |
| 16 | §5.29.4 `revenue_contract` allocation methods beyond proportional (`allocation_method` field) | later slice of PSG-29 | Only proportional allocation exists |
| 17 | §5.29.10 recognition jobs (period-close trigger from wave 14, nightly 01:00 preview) | later slice of PSG-29 | Jobs need item 4 |
| 18 | §5.29.13 parameters `revrec_residual_rule` (LARGEST default, env/legal entity) | partially met | Residual rule is a per-request field defaulting to LARGEST; the legal-entity setting is not read yet |
| 19 | §5.29.13 `ratable_basis` DAILY vs MONTHLY | later slice of PSG-29 | Only the DAILY-weighted monthly schedule exists |
| 20 | §5.29.19 recognition run performance (1M schedule lines, 30 min) | later slice of PSG-29 | No run exists yet |
| 21 | §5.29.10 SLOs and alerts (run > 30 min, roll-forward diff ≠ 0) | later slice of PSG-29 + handoff | A preview counter is served on the admin port (`/metrics`); run metrics need item 4 and alert rules belong to the observability stack (wave 0) |
| 22 | §5.29.11 migration from D365 / RAR (RevRecSchedule, FARR_D_*) | handoff | Migration pack lives in the migration framework (`integration` DB) |
| 23 | Persistence and event contract (`contract.allocated.v1`, `schedule.recognized.v1`) | later slice of PSG-29 | No events are emitted in slice 1; `events/asyncapi.yaml` is explicitly empty |
| 24 | Revenue contract / roll-forward screens (§5.29.6: view allocation, adjust SSP, satisfy milestone, run period, drill) | later slice of PSG-29 | The billing web module has the allocation preview page only; the rest needs items 1 to 6 |
