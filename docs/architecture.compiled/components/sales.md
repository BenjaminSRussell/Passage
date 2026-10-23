# Sales

## Purpose & Boundaries
Context `sales` (database `passage_sales`; waves 26-30). Deployables `sales` (Go: pricing, ATP, credit, settlement, rev rec) and `sales-ts` (order intake adapters, customer portal; hosts the intercompany IC saga worker). Owns conditions, quotations, orders, shipments, invoices, revenue schedules, AR, cash application, collections, RMA, rebates. Posts customer invoice, credit note, revenue schedule lines and cash application itself (sync `ledger.Post`); COGS is posted by inventory.

## Interfaces
Prefix `/v1/sales/`: `prices:resolve` (explain=true, p95 < 20 ms 10-line order), `quotations/{id}:send|:convert`, `sales-orders/{id}:confirm` (ATP, credit, holds; emits `salesorder.confirmed.v1`), `:hold`, `:release-hold` (credit holds need `ReleaseCreditHold`), `:release`, `:cancel`, `atp:check` (lock-free), `credit-checks` (Svc), `shipments/{id}:pick|:pack|:post` (gap-free `PS`; emits `shipment.posted.v1`), `invoice-runs` (202), `customer-invoices/{id}:post` (ledger.Post + AR open item; emits `invoice.posted.v1`) / `:credit` (separate credit-note sequence `CN`), `revenue-recognition-runs`, `revenue-contracts/{id}:allocate` (by SSP), `cash-applications:auto-match` (202), `cash-applications/{id}:apply` (emits `payment.applied.v1`), `lockbox-files` (BAI2), `dunning-runs`, `collection-cases/{id}:dispute`, `return-orders/{id}:authorize|:receive|:credit`, `rebate-agreements/{id}:settle`, `order-intake/{channel}` (Shopify, EDI 850 mapped, API), `GET /reconciliation/{check}` (ar, deferred-revenue).
- Customer portal `/v1/sales/portal/*` (customer scope): invoices, orders, payments (card/ACH via provider; card data never touches Passage), returns (RMA `requested`), cases (until wave 45 `/v1/projects/portal/cases`), statements `?asOf=`.

## Data Ownership
EN-056..060: condition_type, access_sequence(+step), pricing_procedure(+step), `price_condition` (access_key_hash is the ONLY lookup column; IX-SL-01 hot path), quotation, `sales_order` (status draft|quoted|open|confirmed|partially_shipped|shipped|invoiced|closed|cancelled; hold_status none|credit|manual|compliance; channel ui|api|edi|portal|ecommerce|agent) + line (supply_type stock|make_to_order|drop_ship|purchase_to_order), hold, atp_result, `shipment`(+line; F; cogs_journal_entry_id, cogs_gl_seq, gl_posting_status, cogs_amount set by consumer `sales-shipment-cogs`), `customer_invoice` (F; invoice_type invoice|credit_note|debit_note|proforma; rendered_document_key + rendered_sha256 object-lock copy; e_invoice_status), performance_obligation, revenue_schedule(+line), ar_open_item, cash_application (F, gap-free receipt), collection_case, return_order, rebate_agreement/accrual. Indexes IX-SL-01..27. Ref tables `sales_ref.*` (customer, product, on_hand_summary, atp_supply).

## Events Produced
`sales.salesorder.confirmed.v1`, `sales.shipment.posted.v1` (COGS posted by inventory on consuming), `sales.invoice.posted.v1` (sourceDocument for project proposal/service work order), `sales.payment.received.v1` (agent trigger), `sales.payment.applied.v1`, `sales.dunningletter.issued.v1`, `sales.pricecondition.changed.v1`, `sales.returnorder.received.v1`.

## Events Consumed
`sales-shipping` (warehouse.load.shipped -> calls own shipments/{id}:post), `sales-shipment-cogs` (inventory.transaction.posted referenceType=shipment; G), `sales-billing` (shipment.posted, returnorder.received), `sales-ar`/`sales-revrec`/`sales-collections` (invoice.posted, payment.applied), `sales-project-invoice` (projects.invoiceproposal.approved), `sales-service-invoice` (projects.serviceworkorder.completed), `sales-credit` (customer.creditchanged), `sales-atp`, `sales-ref-onhand-summary`, `sales-reservations`, `sales-fulfillment`, `sales-qualityhold`, `sales-pricecache`, `sales-party-ref`, `sales-product-ref`, `sales-partymerge`, `sales-fx`, `sales-reversal`, `sales-ar-bank`, `sales-periodcache`, `sales-approvals`, `sales-numbersequence`, `ic-saga-bridge`.

## Dependencies
internal: ledger, inventory, warehouse, party, product, projects, treasury, integration (EDI/e-invoice). external: tax provider, payment processor (portal), e-invoice access points, carrier adapters.

## Gotchas & Constraints
- Idempotency key for engine-created docs: `<consumer-id>:<event id>`; invoice creation from proposals keyed that way.
- Credit/hold semantics, ATP never over-promises (promised <= available); NFR-P4 500 orders/min p99 < 500 ms.
- Intercompany saga S4 (hosted in sales-ts): workflow id `ic-mirror:<tenantid>:<salesOrderId>`; pairId UUIDv5 of source doc id; timers 5 day mirror PO, 48 h pair-match; posted docs immutable, corrections via credit note mirrored.
- Shipment link-back: inventory never writes the shipment; sales consumer writes cogs_* columns (F allow-list). Billing and ASN do not wait for COGS.
- Pivot S2: `shipment.posted` — no backward compensation; correction via return or reversal packing slip (R4-3 proposes `shipment.reversed.v1`).
- Cash application: model never lifts a set to >= 85 auto-apply band (BR-30.1, assumed companion PRD).

## Source Sections
- Backend 5.12 (790-831), 3.4 (337-338); Data EN-056..060 (2972-3036); Events 3.8 (4054-4065), 9.2 S2 (4489-4532), 9.4 S4 (4586-4645); consumer rows (4219-4223, 4299-4302, 4313, 4316, 4322)
