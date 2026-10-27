# NOT-IMPLEMENTED — revrec (PSG-29, wave 29, slices 1–2)

Slice 1 built the pure ASC 606 allocation and ratable schedule with a stateless
preview. Slice 2 built persisted revenue contracts (obligations, schedule lines,
BIL-0009 warning, at most one RESIDUAL), milestone satisfaction, idempotent period
recognition runs, the deferred revenue roll-forward (AC-29.4 reproduced against
Postgres), the `contract.allocated` and `schedule.recognized` events through the
outbox, and the revenue contract / roll-forward screens of the billing web module.
Everything below is outstanding. Status is `later slice of PSG-29` (same service,
later build) or `handoff` (needs another owner).

| # | Ticket item / acceptance criterion | Status | Reason |
|---|---|---|---|
| 1 | D2 `:allocate` custom method and SSP adjustments on an existing contract (`POST /revrec/contracts/{id}:allocate`, RevRecMaintain; contract modifications) | later slice of PSG-29 | Contracts are allocated once, at creation (`POST /revrec/contracts`); re-allocation must also re-plan unrecognized lines and post a catch-up, which belongs with the modification and credit-note work |
| 2 | D2 Contracts created from `invoice.posted` (revrec consumes billing lines → obligations; one contract per order or bundle template per product) | later slice of PSG-29 | Needs the event inbox (kernel) and the order/bundle references on invoice lines (waves 26, 28 and the bundle slice); contracts are created through the API until then |
| 3 | D2 Deferred revenue postings: Dr 2400 / Cr 4000 journal per recognized line (`journal_posting_id`) | handoff | The ledger posts from `schedule.recognized` (wave 11); `journal_posting_id` stays null until it answers |
| 4 | D2 Usage recognition (FR-29.8 USAGE) | later slice of PSG-29 | Usage ingestion is billing's subscription slice; a USAGE obligation is stored with no schedule line and keeps its contract OPEN |
| 5 | §5.29.9 reconciliation: roll-forward = GL 2400 (nightly check 02:30, alert on diff ≠ 0) and the `GL 2400 diff` column | handoff | Needs GL 2400 balances from the ledger (wave 11); the roll-forward itself is built and tested |
| 6 | FR-29.9 roll-forward per contract (drill-down by contract and to schedules by period) and by fiscal period | later slice of PSG-29 | The roll-forward is per legal entity, currency and calendar month; contract detail shows each contract's schedule lines |
| 7 | D2 SSP source order resolution (BR-29.2: EXPLICIT, LIST, PCT_OF_LIST, RESIDUAL lookup) | handoff | Price lookup needs the pricing context (PRICE access, wave 26). The API accepts the resolved SSP and its source label and does not look them up; the residual SSP is not derived from the price |
| 8 | D2 Credit-note reversal of recognized and deferred amounts (BR-29.6, AC-29.6; schedules REVERSED, roll-forward adjustments) | later slice of PSG-29 | Needs credit notes in billing; `REVERSED` exists in the schema and adjustments are reported as 0.00 |
| 9 | D2 Expense deferral recognition (FR-29.15, `kind = EXPENSE`, 1300/1310 split, BIL-0017) | handoff | Owned by a later revrec slice; AP inputs come from wave 19 |
| 10 | AC-29.2 first postings to the cent (Dr 1200, Cr 4000 7,200.00, Cr 2400 3,600.00) | met in part | Allocation, schedules and the 1,350.00 milestone recognition are tested; the invoice postings are billing and ledger work |
| 11 | AC-29.3 40/40 ASC 606 fixtures against the accountant-reviewed workbook `fixtures/revrec/asc606-40.xlsx` | handoff | The workbook is produced by the accountant reviewer, not in this repo; the tests cover the BR-29.1 example and the residual cases only |
| 12 | §5.29.9 fixture `asc606-40` under `testdata/` | handoff | Same as item 11 |
| 13 | §5.29.4 `revenue_contract.allocation_method` beyond proportional | later slice of PSG-29 | Only proportional allocation exists |
| 14 | §5.29.4 `revenue_schedule.period_id` from the fiscal calendar; partitioning of schedules by period | later slice of PSG-29 + handoff | Lines carry their date; events send the calendar month as `period_id` until the fiscal calendar (wave 14) exists |
| 15 | §5.29.10 / §5.29.16 recognition jobs (period-close trigger from wave 14, nightly 01:00 preview), resumable runs split by contract range | later slice of PSG-29 + handoff | Runs are started through the API; the period-close trigger is wave 14 and the job runner is the platform's |
| 16 | §5.29.13 parameters `revrec_residual_rule` (legal-entity setting, `REVREC_RESIDUAL_RULE`) | partially met | The residual rule is a per-request field defaulting to LARGEST; the legal-entity setting is not read yet |
| 17 | §5.29.13 `ratable_basis` DAILY vs MONTHLY | later slice of PSG-29 | Only the day-weighted monthly schedule exists |
| 18 | §5.29.19 recognition run performance (1M schedule lines in 30 min) | later slice of PSG-29 | The run is one set-based UPDATE per entity and period; it has not been measured at volume |
| 19 | §5.29.10 SLOs and alerts (run > 30 min, roll-forward diff ≠ 0) | handoff | Counters are served on the admin port (`/metrics`); duration histograms and PrometheusRule alerts belong to the observability stack (wave 0) |
| 20 | §5.29.11 migration from D365 / RAR (RevRecSchedule, FARR_D_*) | handoff | The migration pack lives in the migration framework (`integration` DB) |
| 21 | §5.29.15 revenue contract summary (PDF, CSV) and roll-forward workpaper (PDF, XLSX) | handoff | Document rendering is docgen (wave 5) |
| 22 | §5.29.10 flag `revrec.usage` | later slice of PSG-29 | No flag client in the platform kernel yet; it lands with item 4 |
| 23 | Revenue contracts list filters by obligation method (§5.29.14) and cursor pagination | later slice of PSG-29 | The list filters by legal entity, customer and status and returns the newest `limit` (≤ 200) |
| 24 | Satisfying a milestone through `Idempotency-Key` (§5.29.5) | met with a different mechanism | A retry with the same date returns the recorded satisfaction (`Idempotent-Replayed: true`) and another date is 409 REV-0004, so no key table is needed |
| 25 | Closed-period control: refuse a milestone date or a run period end in a closed AR/GL period (BR-29.4 applied to recognition) | handoff | Needs the period-control client (wave 14); until then a late milestone date can change an earlier month's roll-forward |
