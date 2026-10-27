# NOT-IMPLEMENTED: expense (PSG-43, wave 43)

Slices 1-2 deliver the pure policy engine (typed, versioned rules with PER_LINE / PER_DAY
basis and duplicate detection), oldest-first cash-advance settlement with advance status,
requisition variance, one-to-one card-to-receipt matching, per diem computation, cash
advance lifecycle and aging, requisition window and closure, and CSV/OFX card feed parsing.
Slice 3 adds the travel-requisition-required category gate (EXP-TR-002) in report
evaluation, the BR-43.5 lodging cap and mileage amount with caller-supplied rates, and a
batch card-to-receipt match endpoint with unmatched lists and the auto-match rate.
These are exposed as stateless previews (nothing is persisted, so no Idempotency-Key),
and the expense web module has a screen for each preview (line check with the requisition
gate, per diem, cash-advance aging, requisition check, card feed preview, lodging cap,
mileage, card matching).
Slice 4 persists expense reports and lines: create, add line with policy evaluation of
the whole report at commit, delete line, submit (refused while any BLOCK or unsatisfied
REQUIRE_* applies, with `dev.passage.expense.policy.violated.v1` written to the outbox
for each BLOCK), list with status and worker filters, and get; the stored totals keep
total = reimbursable + company-paid; writes take an Idempotency-Key and submit and
delete honour If-Match; the tables have tenant and legal-entity row-level security. The
web module has report list, create, detail (add line, remove line, submit) screens.
Everything below is not built yet.

| Deliverable / AC | Status | Reason |
|---|---|---|
| Split lines, hotel itemization, editing a stored line in place, line project/WBS, city/country, card_txn_id, receipt_id, tax code, personal flag, attendee list (FR-43.11, 8.43.4) | later slice of PSG-43 | Slice 4 stores the line fields the policy engine reads; a line is corrected by deleting and re-adding it |
| Approve, reject, return (SUBMITTED → APPROVED / REJECTED / RETURNED → DRAFT) and the approver chain on the report (8.43.7) | later slice of PSG-43 | The status column allows them, but no transition beyond DRAFT → SUBMITTED is built; approval needs the wave-4 workflow (row below) |
| Own-items scoping for EssExpense, read-all for ExpenseAuditor, MssApprove on own items blocked (8.43.17) | later slice of PSG-43 | The principal carries no worker id yet (wave 2 identity, wave 42 worker); any holder of `expense.report.write` sees and changes every report in its tenant and legal-entity scope |
| Worker and legal entity checked against HR (wave 42) and org (wave 6) on report create | later slice of PSG-43 | Ids are validated as UUIDs and the legal entity against the caller's scope only |
| Expense report number sequence EXP-REP per legal entity (8.43.13) | later slice of PSG-43 | Reports are identified by UUID |
| Idempotency-Key expiry after 24 hours | later slice of PSG-43 | Keys are kept with their rows (report, line) or in `idempotency_keys` (submit) with no purge job |
| CEL condition language, rule scope (legal entities, worker groups), PER_REPORT / PER_TRIP basis, rule-set admin with simulate-against-history (BR-43.5, 8.43.6 Policy admin) | later slice of PSG-43 | Rules are typed predicates in code. CEL (cel-go) is not a platform dependency and needs a README justification when added |
| Policy fixture `expense-policy-500` (AC-43.6, 500/500 exact outcomes) | later slice of PSG-43 | Fixture not under `testdata/`; the default rules are covered by table tests |
| Per-diem lodging and mileage rate tables from the country pack, looked up by city, vehicle type and date; lodging cap and mileage as rules inside report evaluation (BR-43.5 examples) | later slice of PSG-43 | The cap and the amount are built as previews that take the rate as input; the lines carry no nights, city or distance yet and the pack tables are not loaded |
| Duplicate detection across a worker's other reports (BR-43.5) | later slice of PSG-43 | Duplicates are detected within one report, stored or previewed; the cross-report query over stored lines is not written |
| Line currency, exchange rate and amount_accounting (8.43.4) | later slice of PSG-43 | Preview amounts are taken as accounting-currency amounts; conversion needs wave 12 FX |
| Per diem table load (GSA/BMF) and rate lookup by location and date; per diem wired into policy (BR-43.6, FR-43.12) | later slice of PSG-43 | The computation (reductions, 75% partial days, rounding) is built and takes the rate as input. Pack tables are not loaded and the policy engine does not call per diem yet |
| Per diem conversion of the rate currency to the line-date rate (BR-43.6) | handoff | Owned by wave 12 (FX); amounts are returned in the rate currency |
| Card feed import persistence: file-hash idempotency (EXP-CARD-002), quarantine, hourly SFTP pull, `POST /card-transactions/import`, assignment of card_last4 to a worker, OFX FITID de-duplication across files (FR-43.13, `expense-card-import`) | later slice of PSG-43 | Parsing (CSV, OFX) and the file hash are built as a stateless preview behind `expense.card.import`; nothing is stored and the SFTP schedule is not wired |
| Visa VCF 4.0 card feed parser (FR-43.13) | later slice of PSG-43 | Only CSV and OFX are parsed; the VCF sample format is not in the fixtures yet |
| Card matching persistence, unassigned pool, stored one-line-per-card-txn invariant across requests (EXP-CARD-001/002), tolerance as a stored tenant parameter | later slice of PSG-43 | The match endpoint pairs one to one within a request and takes the tolerance as input; nothing is stored, so the invariant does not span requests |
| Match accuracy fixture `card-feed-1000` (AC7, >= 95% auto-match) | later slice of PSG-43 | Fixture not under `testdata/` |
| Receipt upload and OCR, PAN masking before storage, draft lines (FR-43.13, `POST /receipts`) | later slice of PSG-43 | Object store and OCR worker (`expense.ocr` flag) not built |
| Reimbursement: AP vendor invoices for employee and card-issuer vendors, tax recovery lines (FR-43.14, AC8) | later slice of PSG-43 | Needs wave-18 AP integration; not started |
| Cash advances: request, approve, reject, pay as AP prepayment, return, payroll recovery, write-off approval, limit EXP-ADV-001, past-due block persisted, `/cash-advances*` endpoints, reminders, `cash-advance-aging` job (FR-43.15) | later slice of PSG-43 | Lifecycle edges, derived settlement status (including returned, recovered and written-off amounts), return cap, and aging/blocking are pure functions with an aging preview; stored advances, endpoints, reminders and the job are not built |
| Cash-advance settlement posting to AP at report approval (BR-43.9, AC9) | later slice of PSG-43 | Preview math is built; posting needs wave 18 |
| FX conversion at approval rate and realized FX posting (BR-43.9) | handoff | Owned by wave 12 (FX); expense will call it when persisted reports exist |
| Travel requisitions: persisted create, submit, approve, link report, close, `/travel-requisitions*` endpoints (FR-43.16) | later slice of PSG-43 | Window check, out-of-range dates, and closure rules are built as pure functions; storage, endpoints, and the `travel-requisition-close` job are not built |
| Requisition as stored state: "travel requisition required" as a stored legal-entity/category parameter, stored report-to-requisition link, linked report closure (BR-43.13) | later slice of PSG-43 | Variance, window, closure and the EXP-TR-002 category gate are built; the gate takes the categories and the link as caller input, so stored reports are evaluated without it |
| Approval workflow, approver chain, delegation (8.43.5 `approve`, wave-4 workflow) | handoff | Owned by the wave-4 workflow service; expense will submit to it |
| Events `dev.passage.expense.report.approved.v1`, `cash-advance.paid.v1` | later slice of PSG-43 | No approval or advance payment is persisted yet; `policy.violated.v1` is published |
| Mobile offline draft lines and receipt capture (8.43.6) | later slice of PSG-43 | Mobile app not started |
| Expense documents: PDF report, receipts appendix, advance request, requisition PDF (8.43.15) | later slice of PSG-43 | No renderer |
| Policy violations KPI, unassigned card txns, cash advance aging, requisition vs actual inquiries; report list filters beyond status and worker (8.43.14) | later slice of PSG-43 | The report list with status and worker filters is built; the others need persisted card, advance and requisition data |
| Expense web module: receipt attach and card match on the report, MSS approval, policy admin, advance request and settlement view, requisition entry and approval (8.43.6) | later slice of PSG-43 | Web module provides report list, create and detail (add line, remove line, submit, policy badges) plus the preview screens; the other screens need their persisted APIs |
| Migration from Concur/AX exports (8.43.11) | later slice of PSG-43 | Needs import pipeline |
| Performance targets (policy p95 < 100 ms, 50/s) | later slice of PSG-43 | Not load-tested; evaluation is O(rules x lines) pure code |
