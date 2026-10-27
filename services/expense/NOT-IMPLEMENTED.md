# NOT-IMPLEMENTED: expense (PSG-43, wave 43)

Slices 1-2 deliver the pure policy engine (typed, versioned rules with PER_LINE / PER_DAY
basis and duplicate detection), oldest-first cash-advance settlement with advance status,
requisition variance, one-to-one card-to-receipt matching, per diem computation, cash
advance lifecycle and aging, requisition window and closure, and CSV/OFX card feed parsing.
These are exposed as stateless previews (nothing is persisted, so no Idempotency-Key),
and the expense web module has a screen for each slice-2 preview (per diem, cash-advance
aging, requisition check, card feed preview) beside the slice-1 line check.
Everything below is not built yet.

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
| Per diem table load (GSA/BMF) and rate lookup by location and date; per diem wired into policy (BR-43.6, FR-43.12) | later slice of PSG-43 | The computation (reductions, 75% partial days, rounding) is built and takes the rate as input. Pack tables are not loaded and the policy engine does not call per diem yet |
| Per diem conversion of the rate currency to the line-date rate (BR-43.6) | handoff | Owned by wave 12 (FX); amounts are returned in the rate currency |
| Policy violations event `dev.passage.expense.policy.violated.v1` | later slice of PSG-43 | No persisted state change at BLOCK yet |
| Card feed import persistence: file-hash idempotency (EXP-CARD-002), quarantine, hourly SFTP pull, `POST /card-transactions/import`, assignment of card_last4 to a worker, OFX FITID de-duplication across files (FR-43.13, `expense-card-import`) | later slice of PSG-43 | Parsing (CSV, OFX) and the file hash are built as a stateless preview behind `expense.card.import`; nothing is stored and the SFTP schedule is not wired |
| Visa VCF 4.0 card feed parser (FR-43.13) | later slice of PSG-43 | Only CSV and OFX are parsed; the VCF sample format is not in the fixtures yet |
| Card matching persistence, unassigned pool, stored one-line-per-card-txn invariant (EXP-CARD-001/002), match endpoint | later slice of PSG-43 | `Matches` and one-to-one `MatchReceipts` exist in the domain; no storage or endpoint |
| Match accuracy fixture `card-feed-1000` (AC7, >= 95% auto-match) | later slice of PSG-43 | Fixture not under `testdata/` |
| Receipt upload and OCR, PAN masking before storage, draft lines (FR-43.13, `POST /receipts`) | later slice of PSG-43 | Object store and OCR worker (`expense.ocr` flag) not built |
| Reimbursement: AP vendor invoices for employee and card-issuer vendors, tax recovery lines (FR-43.14, AC8) | later slice of PSG-43 | Needs wave-18 AP integration; not started |
| Cash advances: request, approve, reject, pay as AP prepayment, return, payroll recovery, write-off approval, limit EXP-ADV-001, past-due block persisted, `/cash-advances*` endpoints, reminders, `cash-advance-aging` job (FR-43.15) | later slice of PSG-43 | Lifecycle edges, derived settlement status (including returned, recovered and written-off amounts), return cap, and aging/blocking are pure functions with an aging preview; stored advances, endpoints, reminders and the job are not built |
| Cash-advance settlement posting to AP at report approval (BR-43.9, AC9) | later slice of PSG-43 | Preview math is built; posting needs wave 18 |
| FX conversion at approval rate and realized FX posting (BR-43.9) | handoff | Owned by wave 12 (FX); expense will call it when persisted reports exist |
| Travel requisitions: persisted create, submit, approve, link report, close, `/travel-requisitions*` endpoints (FR-43.16) | later slice of PSG-43 | Window check, out-of-range dates, and closure rules are built as pure functions; storage, endpoints, and the `travel-requisition-close` job are not built |
| Requisition as stored state: EXP-TR-002 category requirement, linked report closure (BR-43.13) | later slice of PSG-43 | Variance, window and closure functions are built; the category requirement and stored link are not |
| Approval workflow, approver chain, delegation (8.43.5 `approve`, wave-4 workflow) | handoff | Owned by the wave-4 workflow service; expense will submit to it |
| Events `dev.passage.expense.report.approved.v1`, `policy.violated.v1`, `cash-advance.paid.v1` | later slice of PSG-43 | No persisted state change yet; `events/asyncapi.yaml` declares none |
| Idempotency-Key on writes, If-Match on custom methods (CONVENTIONS) | later slice of PSG-43 | No write endpoints exist yet |
| Mobile offline draft lines and receipt capture (8.43.6) | later slice of PSG-43 | Mobile app not started |
| Expense documents: PDF report, receipts appendix, advance request, requisition PDF (8.43.15) | later slice of PSG-43 | No renderer |
| Expense reports list, policy violations KPI, unassigned card txns, cash advance aging, requisition vs actual inquiries (8.43.14) | later slice of PSG-43 | Need persisted projections |
| Expense web module: report entry, MSS approval, policy admin, advance request and settlement view, requisition entry and approval (8.43.6) | later slice of PSG-43 | Web module provides the line check and the slice-2 preview screens (per diem, advance aging, requisition check, card feed preview); the stored-entity screens need the persisted APIs |
| Migration from Concur/AX exports (8.43.11) | later slice of PSG-43 | Needs import pipeline |
| Performance targets (policy p95 < 100 ms, 50/s) | later slice of PSG-43 | Not load-tested; evaluation is O(rules x lines) pure code |
