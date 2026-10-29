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
Slice 5 persists cash advances (request with the EXP-ADV-001 limit, the EXP-ADV-002
past-due block and the requisition link check, approve/reject, record the AP prepayment
that paid it with `dev.passage.expense.cashadvance.paid.v1` in the outbox, record a cash
return capped at the open balance with the status re-derived), travel requisitions
(create with destinations and estimate lines, submit, approve/reject), MssApprove's
own-items block on both decisions (EXP-SOD-001, by user id), and card feed imports (CSV
and OFX into the cardholders' UNASSIGNED pools, quarantine of unreadable rows and unknown
cards, one import per file hash with EXP-CARD-002). The tables have tenant and
legal-entity row-level security and `passage_app` grants. The web module has list,
create and detail screens for advances and requisitions, and the card pool and import.
Everything below is not built yet.

| Deliverable / AC | Status | Reason |
|---|---|---|
| Split lines, hotel itemization, editing a stored line in place, line project/WBS, city/country, card_txn_id, receipt_id, tax code, personal flag, attendee list (FR-43.11, 8.43.4) | later slice of PSG-43 | Slice 4 stores the line fields the policy engine reads; a line is corrected by deleting and re-adding it |
| Approve, reject, return (SUBMITTED → APPROVED / REJECTED / RETURNED → DRAFT) and the approver chain on the report (8.43.7) | later slice of PSG-43 | The status column allows them, but no transition beyond DRAFT → SUBMITTED is built; approval needs the wave-4 workflow (row below) |
| Own-items scoping for EssExpense, read-all for ExpenseAuditor, MssApprove on own reports blocked (8.43.17) | later slice of PSG-43 | The principal carries no worker id yet (wave 2 identity, wave 42 worker); any holder of `expense.report.write` sees and changes every report, advance, requisition and card transaction in its tenant and legal-entity scope. The own-items block on advance and requisition decisions compares the requesting and deciding gateway user ids, not worker ids |
| Worker and legal entity checked against HR (wave 42) and org (wave 6) on report, advance, requisition and card import create; cardholder ids checked as workers | later slice of PSG-43 | Ids are validated as UUIDs and the legal entity against the caller's scope only |
| Number sequences EXP-REP, EXP-ADV and EXP-TR per legal entity (8.43.13) | later slice of PSG-43 | Reports, advances and requisitions are identified by UUID |
| Idempotency-Key expiry after 24 hours | later slice of PSG-43 | Keys are kept with their rows (report, line, advance, requisition, card import) or in `idempotency_keys` (submit and the advance and requisition custom methods) with no purge job |
| CEL condition language, rule scope (legal entities, worker groups), PER_REPORT / PER_TRIP basis, rule-set admin with simulate-against-history (BR-43.5, 8.43.6 Policy admin) | later slice of PSG-43 | Rules are typed predicates in code. CEL (cel-go) is not a platform dependency and needs a README justification when added |
| Policy fixture `expense-policy-500` (AC-43.6, 500/500 exact outcomes) | later slice of PSG-43 | Fixture not under `testdata/`; the default rules are covered by table tests |
| Per-diem lodging and mileage rate tables from the country pack, looked up by city, vehicle type and date; lodging cap and mileage as rules inside report evaluation (BR-43.5 examples) | later slice of PSG-43 | The cap and the amount are built as previews that take the rate as input; the lines carry no nights, city or distance yet and the pack tables are not loaded |
| Duplicate detection across a worker's other reports (BR-43.5) | later slice of PSG-43 | Duplicates are detected within one report, stored or previewed; the cross-report query over stored lines is not written |
| Line currency, exchange rate and amount_accounting (8.43.4) | later slice of PSG-43 | Preview amounts are taken as accounting-currency amounts; conversion needs wave 12 FX |
| Per diem table load (GSA/BMF) and rate lookup by location and date; per diem wired into policy (BR-43.6, FR-43.12) | later slice of PSG-43 | The computation (reductions, 75% partial days, rounding) is built and takes the rate as input. Pack tables are not loaded and the policy engine does not call per diem yet |
| Per diem conversion of the rate currency to the line-date rate (BR-43.6) | handoff | Owned by wave 12 (FX); amounts are returned in the rate currency |
| Card feed: hourly SFTP pull (`expense-card-import` job), stored card-to-cardholder register, OFX FITID de-duplication across files, re-release of quarantined rows (FR-43.13) | later slice of PSG-43 | `POST /card-transactions/import` stores the import, its quarantine and the pool; the file hash is unique per tenant. The caller supplies the last-four-to-worker map with each file because no card register exists yet, and a corrected row arrives in a new file |
| Visa VCF 4.0 card feed parser (FR-43.13) | later slice of PSG-43 | Only CSV and OFX are parsed; the VCF sample format is not in the fixtures yet |
| Card matching persistence: linking a pooled card transaction to a report line (ASSIGNED), dispute and personal marking, stored one-line-per-card-txn invariant (EXP-CARD-001), tolerance as a stored tenant parameter | later slice of PSG-43 | The unassigned pool is stored and listed, but card transactions are insert-only: lines carry no card_txn_id yet. The match endpoint pairs one to one within a request and takes the tolerance as input |
| Match accuracy fixture `card-feed-1000` (AC7, >= 95% auto-match) | later slice of PSG-43 | Fixture not under `testdata/` |
| Receipt upload and OCR, PAN masking before storage, draft lines (FR-43.13, `POST /receipts`) | later slice of PSG-43 | Object store and OCR worker (`expense.ocr` flag) not built |
| Reimbursement: AP vendor invoices for employee and card-issuer vendors, tax recovery lines (FR-43.14, AC8) | later slice of PSG-43 | Needs wave-18 AP integration; not started |
| Cash advances: creating the AP prepayment to the employee vendor (wave 18/19), payroll recovery, write-off with ExpenseAdmin approval, reminders, `cash-advance-aging` job, saving a DRAFT before submit, project_id, the cash advance aging inquiry over stored advances (FR-43.15, 8.43.14) | later slice of PSG-43 | Request, decide, pay and return are stored. `/pay` records a prepayment id that AP created; AP does not publish prepayment events yet. A request is stored SUBMITTED at once. Recovered and written-off amounts stay zero; the list shows overdue advances |
| Cash-advance settlement against reports at approval and posting to AP (BR-43.9, AC9): settled_amount on the stored advance | later slice of PSG-43 | Preview math is built; reports are not approved yet and posting needs wave 18, so settled_amount stays zero |
| Cash advance parameters per legal entity: maximum per request, settlement due days, recover through payroll (8.43.13) | later slice of PSG-43 | The defaults 2,000.00 and 30 days apply to every legal entity (and the maximum is compared in the advance currency); no parameter store yet |
| FX conversion at approval rate and realized FX posting (BR-43.9) | handoff | Owned by wave 12 (FX); expense will call it when persisted reports exist |
| Travel requisitions: policy checks on the estimate lines, project/WBS, attendees, per-destination dates, estimate line currency, linked reports and closure, `travel-requisition-close` job, requisition vs actual inquiry (FR-43.16, BR-43.13, 8.43.14) | later slice of PSG-43 | Create, submit and approve/reject are stored with destinations and estimate lines in the requisition currency. Window, variance and closure rules are pure functions and previews; no report is linked to a stored requisition, so nothing closes one yet |
| Requisition as stored state: "travel requisition required" as a stored legal-entity/category parameter, stored report-to-requisition link, linked report closure (BR-43.13) | later slice of PSG-43 | Variance, window, closure and the EXP-TR-002 category gate are built; the gate takes the categories and the link as caller input, so stored reports are evaluated without it |
| Approval workflow, approver chain, delegation (8.43.5 `approve`, wave-4 workflow) | handoff | Owned by the wave-4 workflow service; expense will submit to it |
| Event `dev.passage.expense.report.approved.v1` | later slice of PSG-43 | No report approval yet; `policy.violated.v1` and `cashadvance.paid.v1` are published (the PRD's `cash-advance.paid.v1`; the kernel event-type rule allows no hyphen in the entity) |
| Mobile offline draft lines and receipt capture (8.43.6) | later slice of PSG-43 | Mobile app not started |
| Expense documents: PDF report, receipts appendix, advance request, requisition PDF (8.43.15) | later slice of PSG-43 | No renderer |
| Policy violations KPI, requisition vs actual inquiries; list filters beyond status and worker (8.43.14) | later slice of PSG-43 | Report, advance, requisition and card transaction lists filter by status and worker; the unassigned card pool is the card list with status UNASSIGNED |
| Expense web module: receipt attach and card match on the report, MSS approval of reports, policy admin, settlement of advances against reports (8.43.6) | later slice of PSG-43 | Web module provides report list, create and detail; cash advance list, request and detail (approve/reject, record AP payment, record return); travel requisition list, create and detail (submit, approve/reject); card transaction pool and statement import; plus the preview screens. The other screens need their persisted APIs |
| Migration from Concur/AX exports (8.43.11) | later slice of PSG-43 | Needs import pipeline |
| Performance targets (policy p95 < 100 ms, 50/s) | later slice of PSG-43 | Not load-tested; evaluation is O(rules x lines) pure code |
