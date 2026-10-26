# NOT-IMPLEMENTED: expense (PSG-43, wave 43)

Slice 1 delivers the pure policy engine (typed, versioned rules with PER_LINE / PER_DAY
basis and duplicate detection), oldest-first cash-advance settlement with advance status,
requisition variance and one-to-one card-to-receipt matching. Policy evaluation, advance
settlement and requisition variance are exposed as stateless previews.
Everything below is not built in this slice.

| Deliverable / AC | Status | Reason |
|---|---|---|
| Expense reports and lines: create, add line, split, itemize, submit, approve (FR-43.11, 8.43.4) | later slice of PSG-43 | Needs persisted ExpenseReport/ExpenseLine tables, RLS and idempotent writes |
| Report total invariant (Σ lines = reimbursable + company-paid) as stored state | later slice of PSG-43 | Only the preview totals are computed |
| Policy evaluation at line commit (FR-43.12) | later slice of PSG-43 | Preview evaluates at submit date only; commit-time evaluation needs persisted lines |
| CEL condition language, rule scope (legal entities, worker groups), PER_REPORT / PER_TRIP basis, rule-set admin with simulate-against-history (BR-43.5, 8.43.6 Policy admin) | later slice of PSG-43 | Rules are typed predicates in code. CEL (cel-go) is not a platform dependency and needs a README justification when added |
| Policy fixture `expense-policy-500` (AC-43.6, 500/500 exact outcomes) | later slice of PSG-43 | Fixture not under `testdata/`; the default rules are covered by table tests |
| Lodging per-night check against per-diem table x 1.5, mileage at IRS rate (BR-43.5 examples) | later slice of PSG-43 | Need per-diem and mileage tables |
| Duplicate detection across a worker's other reports (BR-43.5) | later slice of PSG-43 | Duplicates are detected within one report; cross-report needs persisted lines |
| Line currency, exchange rate and amount_accounting (8.43.4) | later slice of PSG-43 | Preview amounts are taken as accounting-currency amounts; conversion needs wave 12 FX |
| Per diem by location/date, meal reductions 20/30/50%, first/last day 75% (BR-43.6, FR-43.12) | later slice of PSG-43 | Per-diem table (GSA/BMF) not loaded |
| Policy violations event `dev.passage.expense.policy.violated.v1` | later slice of PSG-43 | No persisted state change at BLOCK yet |
| Card feed import: CSV, OFX, Visa VCF 4.0; hourly SFTP pull; file-hash idempotency; quarantine (FR-43.13, `POST /card-transactions/import`) | later slice of PSG-43 | Parsers and feed importer not built; schedule `expense-card-import` not wired |
| Card matching persistence, unassigned pool, stored one-line-per-card-txn invariant (EXP-CARD-001/002), match endpoint | later slice of PSG-43 | `Matches` and one-to-one `MatchReceipts` exist in the domain; no storage or endpoint |
| Match accuracy fixture `card-feed-1000` (AC7, >= 95% auto-match) | later slice of PSG-43 | Fixture not under `testdata/` |
| Receipt upload and OCR, PAN masking before storage, draft lines (FR-43.13, `POST /receipts`) | later slice of PSG-43 | Object store and OCR worker (`expense.ocr` flag) not built |
| Reimbursement: AP vendor invoices for employee and card-issuer vendors, tax recovery lines (FR-43.14, AC8) | later slice of PSG-43 | Needs wave-18 AP integration; not started |
| Cash advances: request, approve, reject, pay as AP prepayment, return, aging, payroll recovery, write-off, limit EXP-ADV-001 and past-due block EXP-ADV-002 (FR-43.15) | later slice of PSG-43 | Only the oldest-first settlement preview and the status rule exist; persistence, endpoints `/cash-advances*`, and `expense-aging` / `cash-advance-aging` jobs are not built |
| Cash-advance settlement posting to AP at report approval (BR-43.9, AC9) | later slice of PSG-43 | Preview math is built; posting needs wave 18 |
| FX conversion at approval rate and realized FX posting (BR-43.9) | handoff | Owned by wave 12 (FX); expense will call it when persisted reports exist |
| Travel requisitions: create, submit, approve, link report, close (FR-43.16, `/travel-requisitions*`) | later slice of PSG-43 | Only variance math exists; persistence and endpoints not built |
| Requisition variance as stored state with EXP-TR-002 category requirement (BR-43.13) | later slice of PSG-43 | Variance function is built; out-of-range travel WARN and closure rules not built |
| Approval workflow, approver chain, delegation (8.43.5 `approve`, wave-4 workflow) | handoff | Owned by the wave-4 workflow service; expense will submit to it |
| Events `dev.passage.expense.report.approved.v1`, `policy.violated.v1`, `cash-advance.paid.v1` | later slice of PSG-43 | No persisted state change yet; `events/asyncapi.yaml` declares none |
| Idempotency-Key on writes, If-Match on custom methods (CONVENTIONS) | later slice of PSG-43 | No write endpoints exist in this slice |
| Mobile offline draft lines and receipt capture (8.43.6) | later slice of PSG-43 | Mobile app not started |
| Expense documents: PDF report, receipts appendix, advance request, requisition PDF (8.43.15) | later slice of PSG-43 | No renderer |
| Expense reports list, policy violations KPI, unassigned card txns, cash advance aging, requisition vs actual inquiries (8.43.14) | later slice of PSG-43 | Need persisted projections |
| Expense web module: report entry, MSS approval, policy admin, advances, requisitions (8.43.6) | later slice of PSG-43 | Web module provides the line-check screen only |
| Migration from Concur/AX exports (8.43.11) | later slice of PSG-43 | Needs import pipeline |
| Performance targets (policy p95 < 100 ms, 50/s) | later slice of PSG-43 | Not load-tested; evaluation is O(rules x lines) pure code |
