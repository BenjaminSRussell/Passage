# Procurement

## Purpose & Boundaries
Context `procurement` (database `passage_procurement`; waves 17-20). Deployables `procurement` (Go: matching, settlement, payment runs) and `procurement-ts` (RFQ, supplier portal, invoice capture). Owns requisition -> payment documents, match results, AP open items. Sole generator of payment files (pain.001 / NACHA / check / positive-pay) and owner of the "enter payment" duty; treasury only signs and transmits. Creates the expense-reimbursement vendor invoice (workforce does not post AP).

## Interfaces
Generated CRUD for requisitions, RFQs, agreements, POs (+lines, delivery schedules), product receipts, vendor invoices, AP open items, payments, tolerance rules, approval policies. Custom (`/v1/procurement/`):
- `purchase-requisitions/{id}:submit|:convert`; `rfqs/{id}:send|:award`; `purchase-agreements/{id}:confirm`; `vendor-prices:resolve`.
- `purchase-orders/{id}:confirm` (budget check first; emits `purchaseorder.confirmed.v1`), `:revise`, `/versions`, `:cancel`, `:close`.
- `product-receipts/{id}:post` — posts GR/IR accrual at PO price via `ledger.Post` (key `post:procurement:productreceipt:{id}:grir-accrual`; Dr GR/IR-clearing-asset / Cr GR/IR), then finalizes in one local tx; emits `productreceipt.posted.v1`.
- `vendor-invoices:capture` (202, OCR provider open Q10), `vendor-invoices/{id}:match|:post|:hold|:release-hold`, `match-results/{id}:resolve`, `ers-runs`, `settlements` (+`:reverse`), `prepayments`, `payment-proposals` (+`:approve` amount band + preparer != approver in OPA, `:execute`), `payments/{id}:post|:void`, `positive-pay-files`, `payment-files` (+`/content`, `:acknowledge` pain.002 Svc:`paymentfile.ack`).
- Supplier portal `/v1/procurement/portal/*` (supplier scope): purchase-orders (+`:acknowledge`), asns, invoices, payments, profile PATCH (bank details route to approval; R4-10 proposes moving the bank-detail write to party), scorecard.
- `GET /v1/procurement/reconciliation/{check}` (grir, ap).

## Data Ownership
EN-046..052: purchase_requisition(+line), rfq(+line,+response), purchase_agreement(+line), purchase_order (version int; status draft|in_review|approved|confirmed|received|invoiced|closed|cancelled) + line + delivery_schedule + purchase_order_revision (I), `product_receipt`(+line; F profile; gap-free `USMF-PR-2026-000455`; journal_entry_id/gl_seq = GR/IR accrual; valuation_journal_entry_id/valuation_gl_seq set by consumer; gl_posting_status pending|posted|rejected|not_required), `vendor_invoice` (F; internal gap-free `...-VINV-...`; external vendor_invoice_number; UNIQUE (tenant_id, vendor_id, vendor_invoice_number) WHERE status <> 'reversed' = duplicate guard `unique_violation`), match_result (IL; two_way|three_way), ap_open_item (only open_amount, settlement_status mutate), payment_proposal(+line), payment (F; `...-PAY-...` gap-free), settlement (IL), payment_file (UQ file_hash). Indexes IX-PC-01..31.

## Events Produced
`procurement.purchaseorder.confirmed.v1` (intercompany?), `procurement.productreceipt.posted.v1`, `.reversed.v1` [P], `procurement.vendorinvoice.captured.v1`, `procurement.vendorinvoice.posted.v1` (matchResultId, journalEntryId, apOpenItemId, sourceDocument for expense_report), `procurement.payment.posted.v1`.

## Events Consumed
`procurement-receipt-valuation` (inventory.transaction posted/rejected with referenceType=productreceipt; sets gl_posting_status, valuation ids; G), `procurement-plannedorders` (plannedorder.firmed -> POs), `procurement-expense-invoice` (workforce.expensereport.approved -> create+post vendor invoice, employee as vendor party role), `procurement-party-ref`, `procurement-partymerge`, `procurement-fx`, `procurement-reversal`, `procurement-scorecard` (qualityorder.decided), `procurement-ap-bank`, `procurement-receiving` (warehouse.work.completed), `procurement-product-ref`, `procurement-periodcache`, `procurement-approvals`, `procurement-numbersequence`.

## Dependencies
internal: ledger (sync), inventory (valuation events), party, product, platform, treasury (payment submission), workforce (reimbursement). external: OCR/invoice capture provider (adapter only; fallback manual keying), bank files.

## Gotchas & Constraints
- A vendor invoice may post (clearing GR/IR) before inventory valuation finishes; GR/IR-clearing-asset may be transiently debit-heavy; matching reads receipt quantities from procurement, not GR/IR balance.
- Payment: SoD between `payment-proposals/{id}:approve` and `:execute` is human; `system:treasury-signer` holds duty `Release payments` (key custody, not SoD).
- Posting key `post:procurement:vendorinvoice:{id}:invoice`; set journal_entry_id synchronously.
- Receipt reversal -> `:reverse` (new reversal receipt + ledger reverse key `...:grir-accrual-reversal`), never deletion.
- Fiscal immutability allow-list for product_receipt adds valuation_journal_entry_id, valuation_gl_seq, gl_posting_status.
- Step-up (MFA) for payment.approve/release and vendorbank.write.

## Source Sections
- Backend 5.10 (711-755), 3.4 rows (336, 342); Data EN-046..052 (2859-2915); Events 3.6 (4031-4040), 9.1 (4446-4487), consumer rows (4207-4211, 4297, 4314); AX/SAP TX-OPEN-AP/PO (3569-3570, 3574, 3604, 3607)
