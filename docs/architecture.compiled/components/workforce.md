# Workforce

## Purpose & Boundaries
Context `workforce` (database `passage_workforce`; waves 25 time, 42-43). Deployables `workforce` (Go: payroll calculation) and `workforce-ts` (HR, absence, self-service, expenses). Owns workers, positions, jobs, employment, absence, benefits, payroll, expense reports. Posts payroll run to GL via `ledger.Post`. Does NOT post AP: expense approval triggers procurement to create the reimbursement vendor invoice.

## Interfaces
Prefix `/v1/workforce/`: `me` (self-service), `absence-requests/{id}:submit|:approve`, `positions/{id}:fill`, `payroll-runs` (202), `payroll-runs/{id}:calculate` (202; country-pack engine or provider adapter), `:approve`, `:post` (ledger.Post; gap-free `PAYR`; emits `payrollrun.posted.v1`), `pay-statements/{id}` (own, or `ViewSensitivePayroll`), `expense-reports/{id}:submit|:approve` (approve emits `expensereport.approved.v1` — the ONLY trigger of the reimbursement invoice; `:reimburse` deleted), `card-feeds:import` (202), `GET /reconciliation/{check}` (payroll; aggregate only, never P3). Generated CRUD: workers, positions, jobs, employments (effective-dated), absence types/requests, benefit plans/enrollments, pay rules, pay statements (read), expense reports, expense policies.

## Data Ownership
EN-072..074: worker (personnel_number UQ; status candidate|active|on_leave|terminated), worker_secure (P3: national_id_enc, date_of_birth_enc, bank_account_enc, salary_enc, health_info_enc; CDC-excluded; needs `ViewSensitiveWorker`, every read audited), job, position (position_code), employment (effective-dated; GiST exclusion per worker excl. contractor), payroll_run (F; status calculating|calculated|approved|posted|paid; totals in payroll_run_secure), pay_statement(+line) with *_secure tables, pay_rule, expense_report (reimbursement_invoice_id REF procurement) + expense_line, absence(+absence_secure reason_enc). Indexes IX-WF-01..10.

## Events Produced
`workforce.payrollrun.posted.v1` (aggregates only: totals gross/netPay/employeeTaxes/employerTaxes/deductions, journalEntryId, paymentBatchId; no P3), `workforce.expensereport.approved.v1` (workerPartyId, total, no P3).

## Events Consumed
`workforce-user-link` (platform.user.provisioned), `workforce-reimbursement-link` (procurement.vendorinvoice.posted filter sourceDocument.type=expense_report -> sets reimbursement_invoice_id), `workforce-party-ref`, `workforce-partymerge`, `workforce-periodcache`, `workforce-approvals`, `workforce-numbersequence`; ref `time_registration_summary` from manufacturing.

## Dependencies
internal: ledger, procurement, party, manufacturing (time), projects, platform, treasury. external: payroll/tax providers (ADP-style; provider failure blocks only that run), card-feed providers.

## Gotchas & Constraints
- Salary, national id, health/absence reason are P3: masked, encrypted (pgcrypto, per-tenant Vault transit key), no SQL aggregation; payroll totals computed in service after decryption (OQ-16); lakehouse sees payroll aggregates only.
- Net = gross - deductions - taxes (property); 200 IRS Pub 15-T scenarios; EU payroll reference country DE vs UK undecided (Open Q32).
- Offline kiosk time registration commands replay under worker `sub`.
- Fiscal documents are pseudonymized, not deleted, on erasure.

## Source Sections
- Backend 5.15 (880-897), 3.4 (341-342); Data EN-072..074 (3133-3158); Events 3.9 (4075, 4078), consumer rows (4228, 4261, 4315); Open Questions 29, 32 (7808, 7811)
