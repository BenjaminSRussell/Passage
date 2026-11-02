# NOT-IMPLEMENTED: payroll (PSG-43, wave 43)

Slices 1 and 2 deliver the pure US gross-to-net engine (2025 Pub 15-T Worksheet 1A for every
filing status, 2020+ and pre-2020 W-4), the calculation preview endpoint, arrears,
supplemental tax, retro delta math and run-lifecycle guards (slice 1), then stored pay runs,
review/finalize with separation of duties, GL posting through a stored posting profile and GL
reconciliation (slice 2, AC4). Everything below is not built.

## PRD discrepancy (for the PRD owner)

BR-43.1 / AC-43.1 state federal withholding 577.43 and net 2,711.13 for a worker on a
2020+ W-4, Single, with no Step 2 to 4 entries. Pub 15-T Worksheet 1A line 1g subtracts
8,600 (Single) from the annualized wage of every 2020+ W-4 whose Step 2 box is not
checked, which the PRD arithmetic skips: the correct figures are adjusted annual wage
89,160.00, federal withholding 504.66 and net 2,783.90. The PRD's 577.43 is what Pub
15-T gives for a pre-2020 W-4 with zero allowances. The PRD also gives the Single 22%
row as 54,875 to 115,125; the 2025 table row is 54,875 to 109,750. The engine follows
Pub 15-T (ticket AC1); the tests assert 504.66 for the 2020+ W-4 and reproduce the PRD
figures for the pre-2020 W-4 (`TestCalculate_BR431PRDArithmeticOnPreTwentyTwentyW4`).
The PRD owner should correct BR-43.1 and AC-43.1.

| Deliverable / AC | Status | Reason |
|---|---|---|
| Pay groups, cycles, periods, cutoff (FR-43.1) | later slice of PSG-43 | Needs persistence (pay_group, pay_period) and cutoff validation against HR/time data |
| Pay run aggregate: Temporal calculation workflow, pay (FR-43.2, FR-43.7, 8.43.4) | later slice of PSG-43 | Create, review, finalize and post are built (`pay_run`, `pay_statement`); calculation runs inline in the request, there is no Temporal workflow and no payment step |
| Rule-set persistence, signed JSON packs, publish with ETag and second-approver SoD (FR-43.2, PAY-RULE-002) | later slice of PSG-43 | Rules are compiled-in for this slice; publish endpoint and pack loader are not built |
| 6 reference states in the 200-scenario fixture (AC-43.3 "6 states") | later slice of PSG-43 | `payroll-us-200` is built (`internal/domain/testdata/payroll-us-200.csv` from `gen_payroll_fixtures.py`, `TestPayrollUS200_GrossToNetMatchesPub15T`, 200/200 to 0.01 on federal, FICA, net and employer lines). Only the Colorado flat rate is in the pack, so each scenario carries CO or no state; five more state tables are not encoded |
| Tax years other than 2025; rule-set packs as data rather than compiled-in tables | later slice of PSG-43 | `PackFor` knows US 2025 only and fails closed (PAY-RULE-001) for any other year |
| State income tax beyond Colorado (6 reference states) and SUI (FR-43.5) | later slice of PSG-43 | Only the CO flat rate is encoded; an unknown state fails closed |
| Retro: detection of backdated changes, recalculation of stored FINALIZED statements under their bound rule-set versions (FR-43.8) | later slice of PSG-43 | `payroll-retro-25` is built (`TestPayrollRetro25_RecalculationMatchesExpectedDeltas`): prior and recalculated periods are calculated in memory under US-FED-2025.1 and `RetroDelta` books the deltas the current run taxes. Detecting the change and replaying stored statements need persistence |
| Earnings: REGULAR, OVERTIME 1.5x/2.0x, PTO, HOLIDAY, COMMISSION, SHIFT_DIFF from time registrations (FR-43.3) | later slice of PSG-43 | Earnings are supplied in the request; time-registration integration (wave 25) is not built |
| Pre-tax limits per period/annual/percent and employer match cap across the run (FR-43.4) | later slice of PSG-43 | Pre-tax amounts come from the request (a Section 125 flag reduces FICA/FUTA wages, a 401(k) does not); the match is computed per statement; annual-limit tracking needs persistence |
| DE reference pack: Lohnsteuer, Soli, Kirchensteuer, KV/PV/RV/AV (FR-43.6, payroll-de-20) | later slice of PSG-43 | Behind flag `payroll.de-pack`; not started |
| Pay statements: YTD, immutability after FINALIZED enforced by trigger, PDF rendering (FR-43.7, 8.43.15) | later slice of PSG-43 | Statements are stored with their hash (insert-only for the app role); no YTD roll-up, no renderer |
| GL posting by code x dimension (CODE_X_DIMENSION granularity) (FR-43.9) | later slice of PSG-43 | SUMMARY posting is built: `POST /runs/{id}/post` sends the balanced journal to the ledger under `payroll-run:<id>`, accounts come from the per-legal-entity `pay_posting_account` profile (never the request), and `GET /gl-reconciliation` ties POSTED runs to ledger balances (AC-43.5, AC4). Not built: posting by earning/deduction code x position dimension, profile versioning (a changed profile changes what reconciliation expects for runs already posted), and a profile-change audit trail beyond `updated_by` |
| NACHA / SEPA payment batches (FR-43.9) | handoff | Owned by wave 36 (bank files); payroll will call it when persisted runs exist |
| Provider adapter export/import, ADP/Gusto-style (FR-43.10) | later slice of PSG-43 | Flag `payroll.provider-adapter`; not started |
| Off-cycle runs: BONUS (built as a calculation), FINAL_PAY, MANUAL_CHECK, CHECK_REPLACEMENT, REVERSAL (FR-43.18) | later slice of PSG-43 | Only the supplemental calculation exists; subtypes, run creation, final-pay accrued leave (wave 42) and check void/reissue (wave 36) are not built |
| Arrears ledger persistence and recovery across periods (FR-43.17, 8.43.4 ArrearsLedger) | later slice of PSG-43 | Arrears allocation is pure; ledger table and recovery across runs are not built |
| Overpayment claims: creation, recovery schedule, write-off (FR-43.19, PAY-CLM-001) | later slice of PSG-43 | Not started |
| Statutory wage aggregates 941/940/SUI and GL reconciliation; `GET /statutory-aggregates` (FR-43.20, AC14) | later slice of PSG-43 | Not started; rendering and filing are wave 48 |
| Year-end form structures W-2 / 1099 / DE Lohnsteuerbescheinigung (deliverable 4) | later slice of PSG-43 | Not started |
| Workers' compensation class codes and premium base (FR-43.21, deliverable 8) | later slice of PSG-43 | Not started |
| Payroll audit and reconciliation reports (deliverable 5, 8.43.14) | later slice of PSG-43 | Needs persisted registers |
| Payroll workspace, pay run detail, statement viewer, rule-set editor, off-cycle workbench, claims, statutory screens (8.43.6) | later slice of PSG-43 | Web module provides the calculation preview only |
| Garnishments with court-order interfaces | out of scope | Stated out of scope in PRD 8.43.1 |
| Events `payroll.run.posted.v1`, `payroll.statement.finalized.v1`, `payroll.claim.created.v1`, `payroll.statutory-aggregate.closed.v1` | later slice of PSG-43 | Run state changes do not write outbox events yet; `events/asyncapi.yaml` declares none |
| Remaining persistence tables (pay groups, arrears, claims, rule sets, worker profile) | later slice of PSG-43 | Migration 0003 adds `pay_run`, `pay_statement` and `pay_posting_account` with tenant and legal-entity RLS; run create is idempotent on its own scoped key rather than `idempotency_keys` |
| Rule-set signature verification (`payroll-ruleset-load`) (8.43.16) | later slice of PSG-43 | Depends on the pack loader |
| Scheduled jobs: `payroll-cutoff-reminder`, `payroll-retro-detect`, `payroll-statutory-aggregate` (8.43.16) | later slice of PSG-43 | No jobs runner in this slice |
| Performance target 10,000 workers < 10 min (8.43.19) | later slice of PSG-43 | Calculation is per worker; sharded Temporal workflow not built |
| Direct-deposit bank details encrypted with the `hr_pii` key pattern (8.43.10, open question 2 defaults to payroll) | later slice of PSG-43 | Needs the worker payroll profile tables |
| Worker payroll profile: stored W-4, state forms, garnishments (8.43.6) | later slice of PSG-43 | The W-4 is accepted per request in this slice |
| Pay run cutoff, unapproved time policy validation (PAY-RUN-001/002/003) | later slice of PSG-43 | Needs pay run aggregate and time data |
