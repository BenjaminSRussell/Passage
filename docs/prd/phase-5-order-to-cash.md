# Part 5 — Phase 5: Order-to-Cash (waves 26–30)

## 5.0 Phase overview

**Business outcome at phase exit.** At the end of wave 30 a manufacturer runs the complete buy → make → sell → collect loop on Passage. A sales rep quotes and takes an order at a deterministically resolved price, the order is promised against cumulative ATP and checked against credit, the warehouse picks, packs and ships it with a posted packing slip that moves inventory cost to the universal journal, billing posts the invoice with tax and an ASC 606 / IFRS 15 recognition schedule, cash is applied from lockbox, remittance or bank statement, collections runs dunning and disputes, returns flow through RMA to credit note, and every subledger (AR, deferred revenue, delivered-not-invoiced, rebate accrual) reconciles to the journal by automated check. Wave 30 is the first go-live candidate.

**Phase-exit sign-off criteria** (each produces an artifact in `reconciliation/phase-5-exit/`):
- One simulated 31-day month (fixture period 2027-03) is run end-to-end on `stage` for the demo entity DMF-US (USD) and DMF-DE (EUR): ≥ 200 purchase orders received (Phase 3), ≥ 150 production orders reported as finished (Phase 4), 1,000 sales orders, ≥ 900 shipments, ≥ 950 invoices including ≥ 40 bundles with revenue schedules, 600 customer payments across manual / BAI2 lockbox / camt.053, one dunning run, 30 RMAs, one rebate settlement, inventory close and period close for 2027-03.
- Daily reconciliation job passes for all 31 days with zero unexplained differences: AR open items = GL 1200; deferred revenue roll-forward = GL 2400; delivered-not-invoiced = GL 1405; inventory subledger = GL 1400 (Phase 3 check); WIP = GL 1350 (Phase 4 check); rebate accrual = GL 2150; tax payable = tax engine settlement = GL 2300; unapplied cash = GL 1205.
- Trial balance for 2027-03 is balanced; revenue in GL 4000 equals Σ posted invoice revenue lines − credit notes − deferrals; COGS in GL 5000 equals Σ packing-slip cost − return receipts.
- Pricing proof: 500 scenario fixtures and 10k-request determinism property test green; p95 resolution < 20 ms for a 10-line order.
- Order intake 500 orders/min with p99 < 500 ms; ATP never over-promises (property test over 100 fixtures).
- Cash application ≥ 90% auto-match on the 500-payment fixture set with zero false matches; dunning run produces letters for exactly the expected customer set.
- Pick-pack-ship E2E for 1,000 orders in the simulated warehouse with zero inventory mismatch; packing-slip reversal contract tests green.
- Recorded walkthrough (≤ 45 min) and a signed reconciliation pack reviewed by a practicing accountant; both linked from the wave 30 issue.

**Cross-wave data model summary**

| Entity | Owning service (Postgres db) | Key relationships |
|---|---|---|
| condition_record, pricing_procedure, rebate_agreement, price_journal | pricing (pricing_db, Go) | → party/customer (w7), product (w8), customer_pricing_group |
| sales_quotation, sales_order, sales_order_line, sales_agreement, order_hold, credit_profile | sales (sales_db, Go) | → condition_record (explain), inventory supply/demand projection (w16/w17/w21/w22), production order link (w21) |
| shipment, pick_work, container, packing_slip, carrier_shipment | fulfillment (fulfillment_db, Go) | → sales_order_line; inventory_transaction (w16); journal posting (w11) |
| invoice, invoice_line, free_text_invoice, credit_note, subscription | billing (billing_db, Go) | → packing_slip, sales_order_line; tax (w13); journal (w11) |
| revenue_contract, performance_obligation, revenue_schedule | revrec (revrec_db, Go) | → invoice_line; journal (w11) |
| open_item, payment, settlement, lockbox_batch, dunning_run, dispute, interest_note, return_order | receivables (receivables_db, Go) + collections-workflows (TS, Temporal) | → invoice, party/customer, bank statement (camt.053), journal (w11) |
| portal_user, portal_session, payment_intent | customer-portal (portal_db, TS BFF) + payments-gateway (TS) | → party contact (w7), open_item, Stripe/Adyen adapter |

**Cross-wave event catalog** (CloudEvents, Kafka topics `passage.<domain>`, schema registry Avro, outbox via Debezium)

| Event | Producer | Consumers | Payload summary |
|---|---|---|---|
| passage.pricing.condition-record.activated.v1 | pricing | sales (cache invalidation), analytics | record id, type, keys, validity, version |
| passage.pricing.rebate-accrual.posted.v1 | pricing | ledger, receivables (w30 settlement) | agreement id, period, amount, currency, journal ref |
| passage.sales.order.confirmed.v1 | sales | planning (w22), fulfillment, credit, analytics | order id, customer, lines (product, qty, confirmed ship date), totals |
| passage.sales.order-hold.applied.v1 / .released.v1 | sales | fulfillment, collections, portal | order id, hold type, reason, actor |
| passage.sales.atp.promised.v1 | sales | planning, portal | line id, requested date, confirmed date, bucket |
| passage.fulfillment.packing-slip.posted.v1 | fulfillment | billing, inventory, ledger, sales, portal | packing slip id, order lines, qty, cost, journal ref |
| passage.fulfillment.shipment.tracking-updated.v1 | carrier-gateway | portal, sales | shipment id, carrier, tracking no., status |
| passage.billing.invoice.posted.v1 | billing | receivables, revrec, ledger, portal, pricing (rebate base) | invoice id, customer, lines, tax, totals, due date |
| passage.billing.credit-note.posted.v1 | billing | receivables, revrec, ledger | credit note id, original invoice, amounts |
| passage.revrec.schedule.recognized.v1 | revrec | ledger, analytics | schedule line id, obligation, amount, period |
| passage.receivables.payment.applied.v1 | receivables | ledger, collections, sales (credit exposure), portal | payment id, settlements[], residual |
| passage.receivables.customer-balance.changed.v1 | receivables | sales (credit), portal | customer id, balance, overdue, oldest item days |
| passage.receivables.dunning-letter.issued.v1 | collections-workflows | docgen, portal, sales (hold at level 4) | customer, level, items, fee |
| passage.receivables.return-order.dispositioned.v1 | receivables | fulfillment, billing, inventory | RMA id, line, disposition, qty, restocking charge |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| Pricing semantics diverge between AX "find next" and SAP access-sequence users | High | Medium | Pricing procedure is configurable per entity; both reference procedures shipped and covered by the 500-scenario fixture set | Product |
| ATP over-promises because a supply source is missed | Medium | High | Supply/demand projection is rebuilt nightly from inventory + planning events and diffed; property test blocks release | Engineering |
| Delivered-not-invoiced balance drifts from GL at month end | Medium | High | Packing slip and invoice postings reference the same cost snapshot id; daily reconciliation from day 1 of wave 28 | Engineering |
| ASC 606 allocation disputed by auditors | Medium | High | 40 bundle fixtures reviewed by a practicing accountant; residual-cent rule documented | Product + accountant |
| False auto-match in cash application | Low | High | Auto-apply only when single candidate ≥ 85 with a 20-point margin; every auto-match reversible | Engineering |
| Go-live simulation exposes Phase 3/4 defects late | High | Medium | Dry-run simulation at wave 28 exit with sell/collect stubs; defects filed against owning waves | Both owners |

## 5.26 Wave 26: Pricing, Discounts & Trade Agreements

### 5.26.1 Scope and business context
Every order, quotation, invoice and rebate in Phase 5 depends on a price that is explainable and repeatable. Wave 26 ships the `pricing` service: condition tables with validity and quantity breaks, access sequences that resolve from the most specific key to the most general, line/multiline/total discounts, surcharges, exclusion groups, rounding rules, campaigns, rebate agreements with accrual, price journals for atomic mass updates, a simulation/explain API and commission hooks. At the end of the wave a pricing administrator maintains agreements in the Pricing workspace and any caller receives, for a customer/product/date/quantity/currency tuple, a net price plus the ordered list of conditions that produced it. Out of scope: rebate settlement and credit notes (wave 30), vendor pricing (wave 17), configurator-driven price components beyond attribute-keyed condition records (wave 8 supplies attributes), and promotions that require a storefront.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Nobody can say why a line priced at 93.40: AX PriceDiscTable "Find next" results are invisible on SalesLine; SAP needs the VA01 condition "Analysis" screen per line, read by a consultant | Resolution is a side effect inside the order form (PriceDisc class / RV61A formulas), not a queryable function | `POST /pricing/resolve?explain=true` returns the access-by-access trace, stored with the line as `price_explain_ref` | Time to answer "why this price": 20–40 min consultant trace → < 10 s self-service; 100% of lines carry a trace |
| Mass price change is a multi-hour, non-atomic job: PriceDiscAdmTrans journals post row by row and leave half-applied prices on failure; VK12 mass change has no undo | Journal posting is a row loop in X++/ABAP with no transaction boundary across the set | price_journal validates fully, posts in one transaction and reverses with one action | 100k prices: 3–6 h with manual cleanup → < 120 s atomic; rollback 1 action vs. manual re-keying |
| Overlapping trade agreements silently win by insertion order (AX takes the first PriceDiscTable row found; SAP A-table collisions blocked only per access) | No overlap validation at save time | Activation rejects overlapping validity for the same key with 409 naming the conflict | Overlap-caused pricing disputes per month: ~10 per 1,000 agreements → 0 |
| Pricing rounding differs between order, packing slip and invoice (AX rounds SalesLine.LineAmount at each form letter; SAP KPEIN pricing unit rounding) | Rounding applied by each consuming document, not by the engine | Engine rounds once per BR-26.3; consumers reuse the stored net; invoice totals property-tested | Cent-level invoice/order differences: ~0.5% of invoices → 0 |
| Pricing is unreachable from outside the ERP: a CPQ tool or storefront re-implements price lists in middleware | Pricing lives inside the form/transaction call stack | Stateless resolve API (p95 < 20 ms) and condition-record events; MCP tool exposure through the wave 46 server | External price lookups: nightly export → live, p95 < 20 ms; zero duplicated price lists |

### 5.26.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Sales price agreements | PriceDiscTable (Relation=PriceSales), form PriceDiscTable, journal PriceDiscAdmTable/PriceDiscAdmTrans | VK11/VK12/VK13, KONH/KONP, A004/A005/A305, condition type PR00 | pricing.condition_record type PRICE | full |
| Line discount | PriceDiscTable (LineDiscSales), PriceDiscGroup (LineDisc) | K004/K005/K007, KONP-KBETR | condition_record type LINE_DISC | full |
| Multiline discount | PriceDiscTable (MultiLineDiscSales), MultiLineDiscGroup | Group condition (T685A-KZBZG), K020 | condition_record type MULTILINE_DISC with group_key | full |
| Total (end) discount | PriceDiscTable (EndDiscSales), EndDiscGroup | Header conditions HI01/RA01 | condition_record type TOTAL_DISC | full |
| Price/discount groups | PriceDiscGroup, CustTable.PriceGroup/LineDisc/MultiLineDisc/EndDisc, InventTableModule.PriceGroup | KNVV-KONDA, KNVV-KDGRP, MVKE-KONDM | customer_pricing_group, product_pricing_group | full |
| Access order / find next | PriceDiscParameters activation, PriceDisc "Find next", "Search again" | V/07 access sequences (T682), V/08 procedure (T683S), exclusion T684 | pricing_procedure, access_sequence, exclusion_group | improved (explain trace) |
| Smart rounding | PriceDiscRounding, price journal rounding | Rounding condition DIFF, T001R | rounding_rule | full |
| Campaigns / promotions | Trade allowance agreements (TAMAgreement) | Sales deals VB21/VB22, promotions VB31 (KONA) | campaign | full |
| Rebates | PriceDiscRebate / TAMRebateAgreement, TAMRebateTable, RebateManagement (D365) | VBO1/VBO2 (KONA, BO01–BO06, VBOX); S/4 condition contract (WCOCO) | rebate_agreement, rebate_accrual | partial (settlement wave 30) |
| Commissions | CommissionCalc, CommissionSalesRep, CommissionTrans | VBPA partner function, ICM (no standard SD commission) | commission_rule + passage.pricing.commission.calculated.v1 hook | partial |
| Price simulation | Price simulation on quotation (D365) | VA01 condition analysis, pricing log | POST /pricing/resolve?explain=true, Simulation workspace | improved |
| Mass update | Price/discount agreement journals | VK12 mass change, MASS | price_journal (atomic, reversible) | improved |
| Currency-specific prices | PriceDiscTable.Currency | KONP-KONWA | condition_record.currency | full |

### 5.26.3 Functional requirements
- **FR-26.1 Condition types and pricing procedure** — Must. The system shall define condition types (PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC, SURCHARGE, FREIGHT, REBATE_ACCRUAL, ROUNDING) ordered in a pricing procedure per legal entity and sales channel.
  - Ship two reference procedures: `AX_STANDARD` (price → line → multiline → total) and `SAP_RVAA01`-style (PR00 → K004/K005/K007 → HI01 → DIFF).
  - Step number 10–990, step 10 increments; a step references one condition type and optional requirement predicate (CEL expression, ≤ 512 chars).
  - Procedures are versioned; a version is immutable once activated.
- **FR-26.2 Condition records** — Must. The system shall store condition records keyed by a key combination, with validity dates, currency, unit, quantity breaks and amount or percentage.
  - Key combinations supported: customer, customer_pricing_group, all_customers × product_variant, product, product_pricing_group, all_products × optional site, warehouse, sales_channel, campaign.
  - Quantity breaks: ≤ 20 breaks per record set; from_quantity ≥ 0, strictly increasing.
  - valid_from ≤ valid_to; overlapping records for the same key and currency rejected at activation (409).
- **FR-26.3 Access sequence specific → general** — Must. The system shall resolve each condition type by trying accesses in configured order and stopping at the first access that yields a candidate, unless `search_next` is set on the winner.
  - Default PRICE access order (9 accesses) is listed in BR-26.1.
  - `search_next=true` continues to the next access and keeps the lower net unit price (AX "Find next").
- **FR-26.4 Deterministic resolution with explain** — Must. The system shall return, for identical inputs, identical outputs, and shall return an explain trace listing every access tried, candidates found, the winner and why.
  - Pure function over (procedure version, records as of `pricing_date`, request); no wall-clock reads.
  - Explain trace ≤ 64 KB; stored with the order line as `price_explain_ref` (content-addressed).
- **FR-26.5 Line, multiline and total discounts** — Must. The system shall compute line discounts per line, multiline discounts over the summed quantity of lines sharing a multiline group, and total discounts over the order net after line discounts.
  - Two discount percentages per record (disc_pct_1, disc_pct_2) apply sequentially: (1−p1)(1−p2), matching AX PriceDiscTable.Percent1/Percent2.
  - Discount amount and percentage on the same record: amount first, then percentage.
- **FR-26.6 Surcharges and charges** — Should. The system shall apply fixed or percentage surcharges (e.g., fuel, small-order) as separate conditions with their own revenue account.
  - Small-order surcharge default threshold 250 USD / 200 EUR.
- **FR-26.7 Rounding rules** — Must. The system shall round unit prices and line amounts per rounding rule and currency minor unit (ISO 4217 exponent from wave 12).
  - Unit prices held at NUMERIC(19,6); line amounts rounded half-even to minor units.
  - Smart rounding for journals: rules by price band (e.g., 0–10 → end .99; 10–100 → end .95; ≥ 100 → whole).
- **FR-26.8 Exclusion groups** — Must. The system shall support exclusion groups that pick the best (lowest net) or first condition among members, and an `exclusive` flag that suppresses later discount steps.
- **FR-26.9 Price journals** — Must. The system shall apply mass creates/updates/deletes through a journal that validates fully, posts atomically and is reversible by one action.
  - 100k lines post within 120 s; reversal restores prior versions with a new journal referencing the original.
- **FR-26.10 Campaigns** — Should. The system shall group condition records under a campaign with validity, target customer/product groups, budget and a kill switch.
- **FR-26.11 Rebate agreements and accrual** — Must. The system shall define customer rebate agreements (tiered % or amount per unit, period monthly/quarterly/annual, basis invoiced net or quantity) and accrue the rebate on each posted invoice to GL 2150 / 4150 via the journal API.
  - Accrual recomputed per period on retroactive tier crossing; difference posted as adjustment.
- **FR-26.12 Commission hooks** — Could. The system shall evaluate commission rules (sales rep × customer group × product group, % of net or margin) and publish `commission.calculated` events for downstream payroll/projects.
- **FR-26.13 Simulation** — Should. The system shall allow a user to simulate a price for any input tuple without creating a document and compare two procedure versions.
- **FR-26.14 Performance** — Must. p95 < 20 ms for a 10-line resolve, p99 < 60 ms, with records cached in memory per entity and invalidated by `condition-record.activated`.

### 5.26.4 Data model

**condition_record**

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid (v7) | Y | PK |
| tenant_id, legal_entity_id | uuid | Y | RLS |
| condition_type | enum | Y | PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC, SURCHARGE, FREIGHT, REBATE_ACCRUAL |
| key_combination | smallint | Y | FK access_sequence_step |
| customer_id / customer_group_id | uuid | N | exactly one of customer, group, or null (all) |
| product_id / product_group_id / variant_dims | uuid / jsonb | N | same rule for products; variant_dims = {color,size,config} |
| site_id, warehouse_id, sales_channel | uuid/text | N | optional dims |
| currency | char(3) | Y | ISO 4217 |
| unit | text | Y | FK uom (w6) |
| from_quantity | numeric(19,6) | Y | default 0 |
| amount | numeric(19,6) | N | unit price or fixed amount |
| disc_pct_1, disc_pct_2 | numeric(7,4) | N | 0–100 |
| valid_from, valid_to | date | Y | inclusive |
| search_next, exclusive | bool | Y | default false |
| campaign_id | uuid | N | FK campaign |
| status, version, journal_id | enum, int, uuid | Y | DRAFT/ACTIVE/EXPIRED/WITHDRAWN |

**pricing_procedure / access_sequence_step**: id, legal_entity_id, name, version, status, steps[] (step_no, condition_type, requirement_cel, exclusion_group_id, statistical bool, account_key); access_sequence_step: condition_type, seq_no (1..n), key_combination, required_fields[].

**rebate_agreement**: id, customer_id or customer_group_id, product scope (group/product/all), basis (NET_AMOUNT | QUANTITY), period (MONTH | QUARTER | YEAR), tiers[] (from_value, rate_pct or amount_per_unit), currency, accrual_account (2150), expense_account (4150), valid_from/to, status, settlement_method (CREDIT_NOTE | PAYMENT, used in wave 30). **rebate_accrual**: id, agreement_id, period_id, invoice_line_id, basis_value, accrued_amount, journal_posting_id, status (ACCRUED | ADJUSTED | SETTLED | REVERSED).

**price_journal**: id, name, lines[] (op CREATE/UPDATE/DELETE, condition_record snapshot before/after), status (OPEN | VALIDATED | POSTED | REVERSED), posted_at, reversed_by_journal_id.

Invariants: no two ACTIVE records share (condition_type, key fields, currency, unit, from_quantity) with overlapping validity; every ACTIVE record has journal_id; a resolve is reproducible from (procedure_version, pricing_date, request) alone; rebate_accrual sums per period equal the GL 2150 movement for the period. Indexing: composite btree on (tenant, legal_entity, condition_type, key_combination, customer_id, product_id, currency, valid_from) plus partial indexes per key combination; records partitioned by legal_entity; cache snapshot per (legal_entity, pricing_date) with 60 s TTL and event invalidation.

### 5.26.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/pricing/resolve | Resolve prices for a document (header + ≤ 500 lines); `?explain=true` | stateless | PricingResolve |
| POST | /api/v1/pricing/condition-records | Create record (DRAFT) | Idempotency-Key | PricingMaintain |
| POST | /api/v1/pricing/condition-records/{id}:activate | Activate; overlap check | Idempotency-Key | PricingMaintain |
| GET | /api/v1/pricing/condition-records?customer=&product=&asOf= | Query records | — | PricingView |
| POST | /api/v1/pricing/journals, /journals/{id}:post, /journals/{id}:reverse | Mass update lifecycle | Idempotency-Key | PricingJournalPost |
| POST | /api/v1/pricing/procedures, /procedures/{id}/versions/{v}:activate | Procedure maintenance | Idempotency-Key | PricingAdmin |
| POST | /api/v1/pricing/rebates/agreements, GET /rebates/accruals?period= | Rebate agreements and accrual query | Idempotency-Key | RebateMaintain |
| POST | /api/v1/pricing/simulate | Simulation with optional procedure version comparison | stateless | PricingView |

Command validation: resolve requires legal_entity_id, customer_id, currency, pricing_date, lines[].product_id/quantity/unit; rejects quantity ≤ 0 and unknown units (422). Journal post requires status VALIDATED and zero validation errors.

| Event (CloudEvents type) | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.pricing.condition-record.activated.v1 | activation or journal post | id, type, keys, currency, validity, version, journal_id | sales cache, analytics |
| passage.pricing.condition-record.withdrawn.v1 | withdrawal/reversal | id, version, reason | sales cache |
| passage.pricing.journal.posted.v1 | journal post/reverse | journal_id, line_count, reversed_journal_id | audit, analytics |
| passage.pricing.rebate-accrual.posted.v1 | invoice.posted consumed | agreement_id, period_id, invoice_line_id, amount, currency, journal_posting_id | ledger, receivables (w30) |
| passage.pricing.commission.calculated.v1 | invoice.posted consumed | invoice_line_id, sales_rep_id, basis, rate, amount | projects/payroll (Phase 8) |

### 5.26.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Pricing workspace | tiles: expiring in 30 d, draft journals, campaigns active, rebate accrual this period | counts, links | PricingView |
| Trade agreements grid | create, copy, activate, withdraw, export | type, customer/group, product/group, currency, from qty, amount, %, valid from/to, status | PricingMaintain |
| Price journal | add lines, import CSV (≤ 100k rows), validate, post, reverse | op, before/after, errors | PricingJournalPost |
| Pricing procedure designer | add step, set requirement, exclusion group, activate version | step, type, requirement, exclusive | PricingAdmin |
| Price simulation / explain | enter tuple, run, compare versions, open record | per step: access tried, candidate, winner, amount | PricingView |
| Rebate agreements | create, tiers, view accruals, close period | customer, basis, tiers, accrued to date | RebateMaintain |

Interaction rules: grid supports keyboard row navigation, Ctrl+D duplicate row, bulk edit of validity and percentage on multi-select; validation runs on blur and again at activate; explain tree expands with arrow keys; column layouts are personalizable per user (wave 5 shell).

### 5.26.7 Business rules and edge cases
- **BR-26.1 Resolution algorithm.** For each procedure step in order: (1) evaluate requirement predicate; (2) for the step's condition type iterate accesses in seq_no order: PRICE default order is 1 customer × variant, 2 customer × product, 3 customer × product group, 4 customer group × product, 5 customer group × product group, 6 customer × all products, 7 all customers × product, 8 all customers × product group, 9 all customers × all products; (3) at each access select ACTIVE records whose keys match, currency = document currency (no cross-currency fallback), unit = line unit or convertible via wave 6 factors, valid_from ≤ pricing_date ≤ valid_to, from_quantity ≤ line quantity; (4) among candidates pick the greatest from_quantity, then the most dims matched, then the lowest resulting net unit price; (5) stop unless the winner has search_next, in which case continue and keep the minimum; (6) if no access yields a candidate the condition type yields nothing — for PRICE this is a hard error `PRICE_NOT_FOUND` unless the line carries a manual price; (7) exclusion groups are evaluated after all member steps: BEST keeps the lowest net, FIRST keeps the lowest step; (8) an `exclusive` winning discount suppresses all later discount steps; (9) round per BR-26.3.
- **BR-26.2 Pricing date** = requested ship date by default (AX), configurable to order date or delivery date per procedure (SAP KOMK-PRSDT).
- **BR-26.3 Rounding.** Net unit price = amount × (1−p1/100) × (1−p2/100) at 6 decimals; line net = round_half_even(qty × net unit price, minor units); document totals are sums of rounded lines; percentage total discount rounds once on the total.
- **BR-26.4 Multiline grouping** sums quantities across lines with the same multiline group and unit on the same document; a line added after resolution triggers re-resolution of the group.
- **BR-26.5 Manual price override** requires duty PricingOverride, keeps the explain trace with reason code, and is excluded from rebate basis if the agreement says `exclude_manual`.
- **BR-26.6 Concurrency.** Activation uses `SELECT … FOR UPDATE` on the key hash; overlapping activation returns 409 with the conflicting record id.
- **BR-26.7 Retroactive journal** that changes a record already referenced by a posted invoice does not re-price the invoice; it emits `condition-record.activated` with `retroactive=true` for reporting.
- **BR-26.8 Rebate tiers** are evaluated on cumulative period basis; crossing a tier recomputes the whole period's accrual, posting the delta on the next invoice or the period-end job.
- **BR-26.9 Condition record state machine:** DRAFT → ACTIVE (activate) → EXPIRED (valid_to passed, nightly) or WITHDRAWN (withdraw/reverse). Journal: OPEN → VALIDATED → POSTED → REVERSED. No transition back to DRAFT.
- **BR-26.10 Negative prices** are rejected; a 100% discount is allowed and yields 0.00 with the explain trace retained.

### 5.26.8 Acceptance criteria
- **AC-26.1** (FR-26.3) Given customer C1 in price group PG-A with records at access 2 (product, 95.00 USD) and access 4 (group × product, 90.00 USD), when resolving without search_next, then the price is 95.00 and explain shows access 4 not evaluated.
- **AC-26.2** (FR-26.3) Given the same records with search_next on the access-2 record, when resolving, then the price is 90.00 and both accesses appear in the trace.
- **AC-26.3** (FR-26.2) Given breaks 0→10.00, 100→9.50, 500→9.00 EUR, when qty = 500, then unit price 9.00; when qty = 499, then 9.50.
- **AC-26.4** (FR-26.4) Given 10,000 random requests, when each is resolved twice on two replicas, then outputs are byte-identical.
- **AC-26.5** (FR-26.5) Given line discount 10%/5% on a 100.00 USD unit price × 3, then line net = 256.50.
- **AC-26.6** (FR-26.8) Given exclusion group BEST containing LINE_DISC 8% and a campaign LINE_DISC 12%, then only the 12% applies.
- **AC-26.7** (FR-26.9) Given a 100k-line journal with one invalid line, when posted, then nothing is written and the error names the line; when corrected and posted, then reversal restores all 100k prior versions.
- **AC-26.8** (FR-26.11) Given a 2% monthly rebate on net invoiced and an invoice of 10,000.00 USD, when the invoice posts, then an accrual of 200.00 posts Dr 4150 / Cr 2150.
- **AC-26.9** (FR-26.14) Given the 500-scenario fixture under 50 concurrent callers, then p95 < 20 ms.
- **AC-26.10** (FR-26.7) Given a smart rounding rule "end .99 below 10", when a journal raises 7.41 by 5%, then the stored price is 7.99.

### 5.26.9 Test plan and fixtures
Fixtures: `pricing-500` (500 scenarios, hand-built from AX trade-agreement docs and SAP RVAA01 examples, with expected price and trace); `pricing-random-10k` (generated with seed, property); `price-journal-100k` (generated); `rebate-tiers-12` (12 agreements over 3 periods, accountant-reviewed).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Resolver scenarios | unit | pricing/resolve | 500/500 expected price and trace |
| Determinism | property | resolve | identical outputs across 10k runs and replicas |
| Overlap rejection | unit | activate | 100% of overlapping fixtures rejected with 409 |
| Journal atomicity | integration | journals | failure leaves zero rows changed |
| Rebate accrual = GL | reconciliation | rebate_accrual vs ledger | Σ accrual per period = GL 2150 movement to the cent |
| Resolve latency | performance | 10-line resolve, 50 callers | p95 < 20 ms, p99 < 60 ms |
| Sales order pricing | contract | sales ↔ pricing | Pact contract green |

Reconciliation: Σ rebate_accrual.accrued_amount per (agreement, period) = journal postings tagged `source=rebate_accrual`; journal before/after snapshots replay to current record versions.

### 5.26.10 Deployment and operations
Helm chart `passage-pricing` (Go service, 3 replicas, HPA on CPU 70%, 512 Mi); CloudNativePG cluster `pricing-db` (3 instances); Atlas migrations generated from the condition_record metadata; config: `PRICING_CACHE_TTL=60s`, `PRICING_MAX_LINES=500`, `PRICING_EXPLAIN_MAX_BYTES=65536`. Feature flags: `pricing.search_next`, `pricing.sap_procedure`, `pricing.commission_hooks`. Scheduled jobs (Temporal): nightly expiry at 00:10 entity-local, rebate period accrual recompute at period end. SLOs: resolve availability 99.95%, p95 20 ms; alerts: cache invalidation lag > 5 s, resolve error rate > 0.5%, journal post > 300 s. Runbook: cache rebuild, journal reversal, procedure version rollback. Rollback: Argo CD to previous digest; migrations are additive-only in this wave.

### 5.26.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| PriceDiscTable (Relation, AccountCode/AccountRelation, ItemCode/ItemRelation, QuantityAmount, Amount, Percent1/2, Currency, FromDate/ToDate, SearchAgain) | condition_record | AccountCode Table/GroupId/All → customer/group/all; Relation → condition_type |
| PriceDiscGroup; CustTable.PriceGroup/LineDisc/MultiLineDisc/EndDisc | customer_pricing_group, product_pricing_group | one group per AX type |
| PriceDiscAdmTable/Trans (posted) | not migrated | history only; loaded to lakehouse |
| KONH/KONP + A004/A005/A305 (KSCHL PR00/K004/K005/K007, DATAB/DATBI, KBETR, KONWA, KPEIN, KMEIN), KONM scales | condition_record | A-table → key_combination; KONM → quantity breaks; KPEIN pricing unit normalized to per-unit |
| T683S (procedure), T682 (access) | pricing_procedure, access_sequence_step | map to nearest reference procedure; manual review |
| KONA (BO01–BO06), VBOX | rebate_agreement, opening accrual | open accruals loaded as opening balance to 2150 |
| TAMRebateAgreement / D365 RebateManagement | rebate_agreement | tiers map directly |

Validation: migrated resolve of 1,000 sampled customer/product pairs equals the legacy price (AX PriceDisc::findPrice export; SAP VK13 export) to the cent. Not migrated: expired records older than 24 months (archived), commission history, pricing logs.

### 5.26.12 Dependencies, risks, open questions
- Depends on: wave 7 parties and customer groups; wave 8 products, variants, attributes; wave 6 units and conversions; wave 12 currencies and minor units; wave 11 journal posting (rebate accrual); wave 25 (phase order).
- Risks: SAP pricing-unit (KPEIN) and AX per-unit mismatch → normalize on import with 6-decimal precision; cache stampede after a 100k journal → warm cache before cutover of the journal transaction.
- Open questions: (1) Should cross-currency fallback (resolve in entity currency then convert) be allowed? Proposed default: no; decide by wave 27. (2) Are margin-based commissions needed before Phase 8? Proposed: no; decide by wave 29.

## 5.27 Wave 27: Sales Orders, Quotations, ATP & Credit

### 5.27.1 Scope and business context
Wave 27 ships the `sales` service: quotations with conversion to orders, sales agreements (blanket), order types (journal, order, subscription, returned — the returned type is completed in wave 30), order lines with delivery date control against cumulative ATP/CTP, credit management with limits, aging rules, holds and a Temporal release workflow, change management with versioning, line-level status, and a multi-channel intake API. A sales rep can quote, convert, promise a date, and see holds; a credit manager releases orders. Out of scope: picking and shipping (wave 28), invoicing (wave 29), the customer portal UI (wave 30), EDI order intake (wave 35), advanced aATP product allocation.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Promise dates are wrong after a planning run: AX ATP on SalesLine is recomputed only when the user opens the "Available ship and receipt dates" dialog; SAP VBEP confirmed dates change only on VA02 re-availability check (V_V2 rescheduling batch, nightly) | ATP is computed on demand inside the form; supply changes do not re-promise open lines | atp_bucket projection updated from inventory/planning events (lag < 30 s), re-promise on `order.changed` and supply cancellation, backward propagation per BR-27.1 | Promise accuracy (confirmed date = actual ship date): ~70% → ≥ 95%; re-promise latency nightly batch → < 30 s |
| Credit holds are opaque: AX SalesOrderHold shows a code, not the failing rule; SAP OVA8 dynamic check writes only a VBAK-CMGST status and the VKM1 list | Rules are evaluated inside posting classes and only the final status is persisted | `credit-check.evaluated` event and `credit_check_result` store every rule, value and threshold; release queue shows the failing rule | Credit analyst time per release: 5–8 min of lookup in FD32/CustTable → < 30 s; release SLA 2 business days enforced by timer |
| Order entry needs 12+ clicks and 3 forms (SalesTable → SalesLine → InventDim → ATP dialog; VA01 overview → item → schedule lines) | Form-centric UI where price, ATP and dims live on separate tabs | Single grid with inline price and ATP within 300 ms of entering product and quantity | Clicks to add a priced, promised line: 12 → 2; keystrokes-only entry supported |
| Channel orders are re-keyed or loaded by nightly AIF/IDoc batches with no idempotency (duplicate ORDERS05 creates duplicate VBAK) | Integration layer is file/batch oriented; no business-key dedup | Intake API with (channel, external_ref) idempotency, 500 orders/min, p99 < 500 ms, `order.confirmed` events for any consumer | Duplicate orders per 10k channel orders: ~5 → 0; intake latency hours → < 500 ms |
| Changing a confirmed order loses history: AX overwrites SalesLine; SAP CDHDR/CDPOS is unreadable to users | No versioned document, audit only as a change log | Order versions with field-level diff, re-pricing/ATP/credit per affected line, optional approval | Time to reconstruct "what changed and who": 15 min → < 5 s; 100% of post-confirmation changes versioned |

### 5.27.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Quotations | SalesQuotationTable/Line, SalesQuotationStatus, confirm to order | VA21/VA22, VBAK (VBTYP B), inquiries VA11 | sales_quotation, POST …:convert | full |
| Sales orders | SalesTable/SalesLine, SalesStatus (Backorder/Delivered/Invoiced/Canceled), SalesLineStatus, SalesType | VA01/VA02, VBAK/VBAP/VBEP, item categories TAN/TAS | sales_order, sales_order_line, order_schedule_line | full |
| Sales agreements | SalesAgreementHeader/AgreementLine, release orders | Scheduling agreements VA31 (VBAK VBTYP E), contracts VA41 | sales_agreement with release tracking | full |
| ATP / delivery date control | InventDim-level ATP (DlvDateControlType ATP/CTP), InventItemGroup ATP time fence, SalesLine.ConfirmedDlv | Availability check OVZ9 (checking group/rule), VBEP confirmed schedule lines, CO09 | promising module, atp_bucket projection | full (CTP partial) |
| Credit limit and holds | CustTable.CreditMax/MandatoryCreditLimit, CustParameters CreditLimitType, D365 credit management SalesOrderHold/CustCreditLimit | FD32/UKM_BP (KNKK), OVA8 automatic credit control, VKM1 release | credit_profile, order_hold type CREDIT, Temporal release | full |
| Order holds | SalesOrderHold / SalesOrderHoldCode | VBAK-LIFSK delivery block, FAKSK billing block | order_hold (CREDIT, FRAUD, MANUAL, PRICE) | full |
| Change management | D365 order change approval (SalesTable change management) | VA02 change log (CDHDR/CDPOS) | sales_order.version + change_request | improved |
| Configured products | Product configuration models (PCConfiguration) linked to SalesLine | Variant configuration (VC), configurable material KMAT | configuration_id on line (w8), production link (w21) | partial |
| Alternative items | InventTable alternative item (AltItemId) | Material substitution (VB11) | product.alternative_id used in ATP fallback | full |
| Backorders | Backorder lines, SalesBackorder | Backorder processing V_RA | backorder list workspace | full |
| Multi-channel intake | AIF SalesSalesOrderService | IDoc ORDERS05, BAPI_SALESORDER_CREATEFROMDAT2 | POST /api/v1/sales/orders (API key or OIDC), webhook | improved |

### 5.27.3 Functional requirements
- **FR-27.1 Quotations** — Must. The system shall create quotations with priced lines, expiry date (default +30 d), probability %, and convert them (whole or selected lines) to one sales order, preserving the explain trace.
- **FR-27.2 Sales orders** — Must. The system shall create orders with header (customer, invoice account, currency, delivery address, payment terms, pricing procedure) and lines (product, variant dims, quantity, unit, site/warehouse, requested ship/receipt dates, price fields).
  - Order types: JOURNAL (no stock impact), ORDER, SUBSCRIPTION (billing wave 29), RETURNED (wave 30).
  - ≤ 2,000 lines per order; number from wave 5 sequence `SO-`.
- **FR-27.3 Delivery date control** — Must. The system shall compute confirmed ship and receipt dates per line using NONE, SALES_LEAD_TIME, ATP or CTP, applying transport days and the customer's receiving calendar.
- **FR-27.4 Cumulative ATP** — Must. The system shall compute cumulative ATP per (product, variant dims, site, warehouse) from on-hand physical available + scheduled receipts − scheduled issues within the ATP time fence, with backward propagation of negative periods (BR-27.1).
  - Supply sources are policy-configurable: confirmed POs (w17), released/started production orders (w21), planned orders (w22, default excluded), transfer orders.
  - Demand sources: confirmed sales lines, production BOM lines, transfer issues, safety stock (optional).
- **FR-27.5 CTP** — Should. When ATP cannot satisfy the line, the system shall request a capable-to-promise date from scheduling (wave 23) using the BOM/route lead time and finite capacity, and create a planned order link.
- **FR-27.6 Backorders and partials** — Must. The system shall split a line into schedule lines when partial delivery is allowed and list backorders by promised date.
- **FR-27.7 Credit profile and checks** — Must. The system shall maintain a credit profile per customer per legal entity (limit, currency, limit type, review date, max dunning level, overdue tolerance) and evaluate the rules in BR-27.3 at entry, confirmation, release-to-warehouse and invoicing per policy.
- **FR-27.8 Order holds and release** — Must. The system shall apply holds (CREDIT, FRAUD, MANUAL, PRICE_REVIEW) that block confirmation/release to warehouse, and run a Temporal release workflow with approvers by duty and a 2-business-day SLA timer.
- **FR-27.9 Change management** — Must. The system shall version an order on every post-confirmation change, store a field-level diff, re-run pricing/ATP/credit for affected lines, and require approval when configured for the customer.
- **FR-27.10 Sales agreements** — Should. The system shall create blanket agreements with committed quantity or amount, validity and release tracking; orders released against them inherit price and decrement commitment.
- **FR-27.11 Configured products** — Should. The system shall accept a configuration id from the wave 8 configurator on a line and, on confirmation, create the production or planned order linkage (wave 21/22) for make-to-order items.
- **FR-27.12 Intake API** — Must. The system shall accept orders from external channels with idempotency keys, 500 orders/min, p99 < 500 ms, returning the created id and any holds.
- **FR-27.13 Alternative items** — Could. When ATP for the ordered product fails inside the requested date, the system shall offer the configured alternative product with its ATP.

### 5.27.4 Data model

**sales_order** (SalesTable)

| Field | Type | Required | Notes |
|---|---|---|---|
| id, order_number | uuid, text | Y | SO-sequence, unique per legal entity |
| tenant_id, legal_entity_id | uuid | Y | RLS |
| customer_id, invoice_account_id | uuid | Y | FK party (w7); invoice account defaults to customer |
| order_type | enum | Y | JOURNAL, ORDER, SUBSCRIPTION, RETURNED |
| status | enum | Y | DRAFT, OPEN, CONFIRMED, PARTIALLY_SHIPPED, SHIPPED, INVOICED, CLOSED, CANCELLED |
| currency, pricing_procedure_version_id | char(3), uuid | Y | |
| delivery_address_id, delivery_terms, mode_of_delivery | uuid, text, text | Y | |
| payment_terms_id, cash_discount_id | uuid | Y | w7 terms |
| requested_ship_date, requested_receipt_date | date | Y | |
| credit_check_result | jsonb | N | last evaluation (BR-27.3) |
| version, etag | int, text | Y | optimistic concurrency |
| totals (net, discount, tax, gross) | numeric(19,4) | Y | derived, stored |
| channel, external_ref, idempotency_key | text | N | unique (tenant, channel, external_ref) |

**sales_order_line** (SalesLine/VBAP) with **order_schedule_line** (VBEP): id, order_id, line_no (10-step), product_id, variant_dims jsonb, quantity, unit, site_id, warehouse_id, unit_price, disc_pct_1/2, line_net, tax_group, delivery_date_control (NONE|LEAD_TIME|ATP|CTP), requested_ship_date, confirmed_ship_date, confirmed_receipt_date, line_status (OPEN | RESERVED | PICKED | DELIVERED | INVOICED | CANCELLED), delivered_qty, invoiced_qty, remaining_qty, configuration_id, production_order_id, agreement_line_id, price_explain_ref, multiline_group, reservation_id (w16). Schedule line: line_id, seq, qty, confirmed_ship_date, source (ATP bucket id | CTP planned order id).

**credit_profile**: customer_id, legal_entity_id, credit_limit numeric(19,4), currency, limit_type (NONE | BALANCE | BALANCE_PACKING_SLIP | BALANCE_ALL), tolerance_pct (default 5), tolerance_amount (default 1,000.00), oldest_item_days_max (default 30), max_dunning_level (default 3), review_date, risk_group, mandatory_check bool, exposure jsonb (open_orders, delivered_not_invoiced, open_ar, unapplied_cash, updated_at).

**order_hold**: id, order_id, hold_type (CREDIT | FRAUD | MANUAL | PRICE_REVIEW), reason_code, status (ACTIVE | RELEASED | CLEARED), applied_by, released_by, workflow_run_id, applied_at, released_at.

**atp_bucket** (projection): product_id, variant_dims, site_id, warehouse_id, bucket_date, supply_qty, demand_qty, period_atp, cumulative_atp, source_refs jsonb, computed_at.

Invariants: Σ schedule_line.qty = line.quantity; delivered_qty ≤ quantity; remaining_qty = quantity − delivered_qty (after cancellation of remainder it is 0); order status derives from line statuses; an order with an ACTIVE hold is never CONFIRMED for release; cumulative_atp of bucket n = min over k ≥ n of Σ period_atp[0..k] (never above what later periods can support); every CONFIRMED line with ATP control has a schedule line referencing an atp_bucket as of confirmation. Indexing: lines by (order_id, line_no); open lines partial index on line_status in (OPEN, RESERVED) for ATP demand; atp_bucket PK (product, dims hash, site, warehouse, bucket_date); orders partitioned by legal_entity.

### 5.27.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/sales/quotations, …/{id}:send, …/{id}:convert | Quotation lifecycle | Idempotency-Key | SalesQuotationMaintain |
| POST | /api/v1/sales/orders | Create order (UI or channel) | Idempotency-Key, unique external_ref | SalesOrderMaintain / ChannelIntake |
| PATCH | /api/v1/sales/orders/{id} | Change (If-Match etag), creates version | etag | SalesOrderMaintain |
| POST | /api/v1/sales/orders/{id}:confirm | Price, ATP, credit, confirm | Idempotency-Key | SalesOrderConfirm |
| POST | /api/v1/sales/orders/{id}:cancel, /lines/{lineId}:cancel-remainder | Cancellation | Idempotency-Key | SalesOrderMaintain |
| POST | /api/v1/sales/atp:check | Simulate ATP for (product, dims, site, qty, date) | stateless | SalesOrderView |
| GET | /api/v1/sales/atp/buckets?product=&site=&from=&to= | Bucket view (CO09 equivalent) | — | SalesOrderView |
| POST | /api/v1/sales/orders/{id}/holds, …/holds/{holdId}:release | Apply / release hold | Idempotency-Key | SalesOrderHold / CreditRelease |
| GET/PUT | /api/v1/sales/credit-profiles/{customerId} | Credit profile | etag | CreditMaintain |
| POST | /api/v1/sales/agreements, …/{id}:release-order | Sales agreements | Idempotency-Key | SalesAgreementMaintain |

Validation: confirm rejects orders with DRAFT lines lacking price (unless manual), quantity ≤ 0, closed period for order date (wave 14 period control applies only at posting, so warn only), and customer status BLOCKED (w7).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.sales.quotation.converted.v1 | convert | quotation_id, order_id, lines[] | analytics, CRM integration |
| passage.sales.order.confirmed.v1 | confirm | order_id, customer_id, currency, lines[{line_id, product, dims, qty, site, warehouse, confirmed_ship_date}], totals, version | fulfillment, planning (w22 demand), credit, analytics |
| passage.sales.order.changed.v1 | PATCH post-confirmation | order_id, version, diff[] | fulfillment, planning |
| passage.sales.order.cancelled.v1 | cancel | order_id, line_ids, reason | planning, fulfillment, portal |
| passage.sales.atp.promised.v1 | confirm with ATP/CTP | line_id, requested, confirmed, bucket_date, source | planning, portal |
| passage.sales.order-hold.applied.v1 / released.v1 | hold lifecycle | order_id, hold_type, reason, actor, workflow_run_id | fulfillment, collections, portal |
| passage.sales.credit-check.evaluated.v1 | any check | customer_id, order_id, rules[{id, result, value, threshold}], outcome | audit, collections |

### 5.27.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Sales workspace | tiles: orders on hold, backorders, quotes expiring 7 d, intake errors | counts, lists | SalesOrderView |
| Quotation detail | add line, price, send PDF, convert, mark lost | customer, lines, expiry, probability, totals | SalesQuotationMaintain |
| Order entry (header + grid) | add line (product search with ATP badge), price, confirm, hold, cancel, copy | line, product, qty, unit, site/wh, requested/confirmed dates, price, disc, net, status | SalesOrderMaintain |
| ATP explorer | pick product/site, view buckets, simulate qty/date | bucket date, supply, demand, period ATP, cumulative ATP, sources | SalesOrderView |
| Credit hold release queue | open order, view exposure, release/reject with comment | customer, order, amount, exposure vs limit, rule failed, age | CreditRelease |
| Backorders | re-promise, split, cancel remainder | line, promised date, qty, customer priority | SalesOrderMaintain |
| Order change requests | approve/reject diff | field, old, new, author | SalesOrderApprove |

Interaction rules: Tab/Enter moves across the line grid; typing a product code and quantity triggers inline price and ATP within 300 ms; validation at line commit and again at confirm; bulk re-promise on multi-select; column sets and default site/warehouse personalizable; change to a confirmed order shows the diff before save.

### 5.27.7 Business rules and edge cases
- **BR-27.1 Cumulative ATP worked example.** Product FG-100, site S1, warehouse W11, ATP time fence 30 d, on-hand 100 on 2027-03-01. Supply: PO-2001 50 on 2027-03-04; PRD-3001 80 on 2027-03-08. Existing demand: SO-1001 60 on 03-02, SO-1002 40 on 03-05, SO-1003 70 on 03-09. Buckets open at each receipt: bucket 0 (03-01) supply 100, demand 60, period ATP 40; bucket 1 (03-04) supply 50, demand 40, period ATP 10; bucket 2 (03-08) supply 80, demand 70, period ATP 10. Cumulative ATP: 40, 50, 60. A new line of 45 requested 03-01: bucket 0 cumulative 40 < 45, first bucket with cumulative ≥ 45 is bucket 1 → confirmed ship date 2027-03-04 for complete delivery; with partial delivery allowed, schedule lines 40 on 03-01 and 5 on 03-04. After confirming 45 on 03-04, bucket 1 period ATP = −35; negative periods propagate backward, so bucket 0 cumulative becomes 5, bucket 1 → 5, bucket 2 → 15. If SO-1002 had been 60 instead of 40, bucket 1 period ATP −10 would have reduced bucket 0 cumulative to 30 before the new line.
- **BR-27.2 Date arithmetic.** confirmed_receipt_date = confirmed_ship_date + transport days (mode of delivery) rolled forward to the customer's next receiving day; requested dates in the past are set to today with a warning; dates outside the time fence are promised at the fence end with source `TIME_FENCE`.
- **BR-27.3 Credit check rules** (evaluated in order; any FAIL applies a CREDIT hold; WARN is shown only): CR-1 exposure + order amount > limit × (1 + tolerance_pct/100) and > limit + tolerance_amount → FAIL (exposure per limit_type: BALANCE = open AR + unapplied; BALANCE_PACKING_SLIP adds delivered-not-invoiced; BALANCE_ALL adds confirmed open orders); CR-2 oldest open item overdue > oldest_item_days_max → FAIL; CR-3 current dunning level ≥ max_dunning_level → FAIL; CR-4 review_date < today or profile missing while mandatory_check → FAIL; CR-5 overdue amount > 25% of open AR → WARN; CR-6 order amount > 50,000 USD / 45,000 EUR for a customer < 90 days old → FAIL (new-customer rule). Amounts convert to the profile currency at the wave 12 spot rate on the check date.
- **BR-27.4 Hold precedence.** CREDIT and FRAUD block confirmation and release; MANUAL and PRICE_REVIEW block release only. Release of a CREDIT hold is valid for 7 days; a change raising the order amount by > 5% re-triggers CR-1.
- **BR-27.5 Order state machine.** DRAFT → OPEN (first line saved) → CONFIRMED → PARTIALLY_SHIPPED → SHIPPED → INVOICED → CLOSED; CANCELLED from DRAFT/OPEN/CONFIRMED (whole) or by cancelling remainder on lines. Line: OPEN → RESERVED → PICKED → DELIVERED → INVOICED; CANCELLED from OPEN/RESERVED. Quotation: DRAFT → SENT → CONVERTED | LOST | EXPIRED | CANCELLED.
- **BR-27.6 Concurrency.** PATCH requires If-Match; concurrent confirm and change → the later call gets 409 and must re-read; ATP consumption is serialized per (product, dims, site, warehouse) key using a Postgres advisory lock so two orders cannot consume the same bucket quantity.
- **BR-27.7 Reservation.** Confirmation with ATP creates a wave 16 reservation only when `reserve_on_confirm` is set (default true for ORDER type); JOURNAL orders never reserve or consume ATP.
- **BR-27.8 Quantity rounding.** Quantities carry 6 decimals; products flagged `whole_units` reject fractional quantities; unit conversion uses wave 6 factors and rounds half-even.
- **BR-27.9 Subscription and agreement lines** are priced at confirmation and re-priced only if the agreement price changes; released orders decrement agreement commitment atomically and cannot exceed it by more than the agreement's overdelivery_pct (default 0).
- **BR-27.10 Intake duplicates.** Same (channel, external_ref) within 30 days returns the original order id with 200, not a duplicate; different payload with the same key → 409.

### 5.27.8 Acceptance criteria
- **AC-27.1** (FR-27.4) Given the BR-27.1 fixture, when a 45-unit line requests 2027-03-01 with complete delivery, then confirmed ship date = 2027-03-04 and the promised event references bucket 1.
- **AC-27.2** (FR-27.4) Given any fixture in `atp-100`, when all orders are confirmed, then no bucket's cumulative ATP is negative at any point after backward propagation, and total promised ≤ on-hand + included supply (property).
- **AC-27.3** (FR-27.7, FR-27.8) Given credit limit 10,000 USD, open AR 8,500, tolerance 5% / 1,000, when a 2,000 USD order is confirmed, then no hold is applied (exposure 10,500 is within both tolerances) and a WARN is shown; when a 3,000 USD order is confirmed, then exposure 11,500 exceeds 10,500 and 11,000, a CREDIT hold is applied, and when a CreditRelease user releases it the order becomes CONFIRMED and `order-hold.released` is emitted.
- **AC-27.4** (FR-27.11) Given a make-to-order product with configuration CFG-77, when the order is confirmed, then a production order (wave 21) exists with reference to the line and the line's confirmed date equals the production order's scheduled end + transport days.
- **AC-27.5** (FR-27.12) Given 500 orders/min for 10 min through the intake API, then p99 < 500 ms and all 5,000 orders exist exactly once.
- **AC-27.6** (FR-27.1) Given a sent quotation with 3 lines, when lines 1 and 3 are converted, then one order with 2 lines exists, prices and explain refs match the quotation, and the quotation status is CONVERTED.
- **AC-27.7** (FR-27.9) Given a confirmed order, when quantity on line 10 changes from 5 to 8, then version increments, the diff records the change, ATP re-promises line 10 only, and `order.changed` is emitted.
- **AC-27.8** (FR-27.6) Given partial delivery allowed and ATP 40 of 45, then two schedule lines exist and the backorder list shows 5 units at 2027-03-04.
- **AC-27.9** (FR-27.10) Given an agreement committing 1,000 units, when release orders total 1,000, then the next release is rejected with 422 `AGREEMENT_EXHAUSTED`.
- **AC-27.10** (FR-27.12) Given the same (channel, external_ref) sent twice, then one order exists and the second call returns 200 with the same id.

### 5.27.9 Test plan and fixtures
Fixtures: `atp-100` (100 supply/demand scenarios with expected promise dates; built from SAP OVZ9 and AX ATP doc examples); `credit-40` (profiles and AR open items incl. migrated balances); `orders-5k-intake` (generated channel payloads); `quotes-50`; `demo-plant` (wave 25 plant with FG-100…FG-120 stock and open production orders).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| ATP scenarios | unit | promising | 100/100 expected dates |
| Never over-promise | property | promising | Σ promised ≤ supply in every prefix of buckets |
| Credit rules | unit | credit | 40/40 expected outcomes incl. tolerance edges |
| Hold release workflow | E2E (Temporal) | release | hold released, SLA timer fires at 2 business days |
| Intake throughput | performance | POST /orders | 500/min, p99 < 500 ms, 0 duplicates |
| Sales ↔ pricing, inventory, production | contract | Pact | all green |
| Order entry UI | E2E (Playwright + axe) | shell | create/confirm/hold flows pass, 0 serious a11y violations |

Reconciliation: Σ open confirmed order amounts per customer = credit_profile.exposure.open_orders; reservations in wave 16 = Σ RESERVED line quantities; ATP projection rebuilt from events equals incremental projection (nightly diff = 0).

### 5.27.10 Deployment and operations
Helm `passage-sales` (Go, 3 replicas, HPA on RPS), `sales-db` CloudNativePG; Temporal namespace `sales` for hold release and change approval; config: `ATP_TIME_FENCE_DEFAULT_DAYS=30`, `ATP_INCLUDE_PLANNED_ORDERS=false`, `CREDIT_CHECK_POINTS=confirm,release`, `INTAKE_RATE_LIMIT_PER_KEY=1000/min`. Flags: `sales.ctp`, `sales.alternative_items`, `sales.change_approval`. Jobs: ATP projection rebuild 01:00 entity-local; quotation expiry hourly; credit review-date warnings daily. SLOs: order create p99 500 ms, availability 99.95%, ATP projection lag < 30 s. Alerts: projection lag > 120 s, hold queue age > 2 business days, intake 5xx > 0.1%. Runbook: projection rebuild, stuck release workflow, re-promise after supply cancellation. Rollback: previous digest; additive migrations.

### 5.27.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| SalesTable (SalesId, CustAccount, InvoiceAccount, CurrencyCode, SalesStatus, DeliveryDate, Payment) + SalesLine (ItemId, InventDimId, SalesQty, SalesUnit, SalesPrice, LinePercent, SalesStatus, RemainSalesPhysical/Financial) | sales_order, sales_order_line | only open orders (SalesStatus = Backorder or Delivered with RemainSalesFinancial > 0); InventDim → variant_dims + site/warehouse |
| SalesQuotationTable/Line (status Sent) | sales_quotation | open quotations only |
| SalesAgreementHeader/AgreementLine | sales_agreement | remaining commitment computed from released qty |
| CustTable.CreditMax, MandatoryCreditLimit, CustParameters.CreditLimitType | credit_profile | type mapping 1:1 |
| VBAK/VBAP/VBEP (open: VBUK-GBSTK ≠ C), VBKD terms, VBPA partners | sales_order, lines, schedule lines | item category TAS → drop-ship flag (w28); VBEP confirmed qty → schedule lines |
| KNKK (credit limit, KLIMK, CTLPC risk), UKM_BP S/4 | credit_profile | risk class → risk_group |
| VBAK VBTYP B (quotations), E/G (scheduling agreements, contracts) | sales_quotation, sales_agreement | |

Validation: count and Σ open order value per customer equal legacy extract; every migrated open line re-promises to a date ≤ legacy confirmed date + 1 day or is flagged. Not migrated: closed/invoiced orders (history to lakehouse), change logs (CDHDR), SAP credit horizon settings (replaced by rules in BR-27.3).

### 5.27.12 Dependencies, risks, open questions
- Depends on: wave 26 pricing; wave 16 on-hand/reservations; wave 17 PO receipts; wave 21 production orders; wave 22 planned orders and demand feed; wave 23 scheduling (CTP); wave 7 customers/terms; wave 8 configurator; wave 4 Temporal.
- Risks: ATP projection staleness during planning runs (mitigation: planning emits batch end marker; ATP marks buckets stale); credit exposure incomplete before wave 29/30 (mitigation: open AR loaded from migration into receivables_db.open_item schema created this wave).
- Open questions: (1) Default delivery date control ATP vs. SALES_LEAD_TIME for migrated customers — proposed ATP; decide by wave 28. (2) Should fraud holds call an external scoring provider? Proposed: rule-only in v1; decide by wave 30.

## 5.28 Wave 28: Fulfillment: Picking, Packing & Shipping

### 5.28.1 Scope and business context
Wave 28 ships the `fulfillment` service and the `carrier-gateway` adapter service. Confirmed orders are released to the warehouse by rule, picked (basic wave/batch picking; advanced WMS with location directives arrives in wave 31), packed into containers with SSCC labels, rated and labelled through a carrier abstraction, and shipped with a posted packing slip that deducts inventory and posts cost to the universal journal. Drop-ship (direct delivery from vendor) and partial shipments are supported, and tracking is published for the customer portal. Out of scope: location directives, license plates and mobile RF flows (wave 31), transportation planning and freight reconciliation (wave 34), hazmat documentation, and export compliance.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Delivered-not-invoiced never ties: AX "Cost of units delivered" postings via InventPosting drift from CustPackingSlipJour when invoices are cancelled; SAP has no DNI account at all (PGI posts COGS immediately, so revenue and COGS land in different periods) | Physical and financial postings are decoupled across SalesFormLetter classes; reconciliation is a month-end report | Packing slip and invoice share one cost_snapshot_id; DNI projection reconciled to GL 1405 daily; both AX-style and PGI-style matrices supported by policy | Month-end DNI reconciliation effort: 4–8 h → 0 (automated daily, diff 0.00) |
| Posting a packing slip blocks the user for 5–20 s and fails opaquely (AX SalesFormLetter batch, "Update has been cancelled"; VL02N PGI errors "Posting period closed" after picking is done) | Synchronous multi-table update in one session with late validation | Pre-validation at PACKED (period, holds, dims, negative stock) and a Temporal saga with compensation; p95 post < 2 s | Posting failures discovered after pick: ~3% → < 0.1%; post latency 5–20 s → < 2 s |
| Carrier integration is a per-carrier project (AX ShipCarrier via a third-party desktop app, SAP XSI/TM) | No provider abstraction; labels produced outside the ERP | carrier-gateway with rate/label/track contract and reference adapters; `tracking-updated` events to portal and any subscriber | Time to add a carrier: 4–8 weeks → < 2 days (adapter contract tests); tracking visibility 0 → 100% of shipments |
| Short picks and partial shipments require manual SalesLine splitting (AX delivery schedule) or VBEP edits | Schedule lines are not first-class in the fulfillment flow | Schedule lines drive shipments; short pick auto-creates the remainder and releases the reservation | Manual line splits per 1,000 orders: ~40 → 0 |
| SSCC/label errors at customer receiving (wrong check digit, duplicate SSCC on re-print) | SSCC generated by label software, not validated by ERP | Gap-free SSCC per GS1 prefix with mod-10 check, uniqueness and 12-month no-reuse enforced in the container entity | Label rejections at customer DC: ~1% of pallets → 0 |

### 5.28.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Release to warehouse | Release to warehouse (WHSWorkTable), SalesLine.ReleaseStatus; AX2012 output orders (InventPickingList) | VL01N/VL10 delivery creation, LIKP/LIPS | shipment (release rules) | full |
| Picking list / registration | WMSPickingRoute, WMSOrderTrans, picking list registration | LT03 transfer order, LTAP; S/4 EWM warehouse task | pick_work, pick_line | partial (advanced w31) |
| Packing / containers | WHSContainerTable, packing station | HU packing (VEKP/VEPO, HUMO) | container, container_line | full |
| SSCC / labels | WHSContainerTable.LicensePlate (SSCC) | HU external id SSCC, GS1-128 | container.sscc, label PDF/ZPL | full |
| Packing slip posting | CustPackingSlipJour/CustPackingSlipTrans, SalesFormLetter_PackingSlip, InventTrans StatusIssue=Deducted | Post goods issue VL02N, MKPF/MSEG movement 601, LIKP WBSTK | packing_slip (posts inventory + journal) | full |
| COGS posting | Inventory posting profiles (InventPosting: Cost of units delivered / Cost of goods delivered) | OBYC GBB-VAX, BSX | posting matrix BR-28.1 | full |
| Carrier interface | Shipping carrier interface (ShipCarrierTable, ShipCarrierShippingRequest) | SAP TM / Express Ship Interface (XSI) | carrier-gateway (rate/label/track) | improved |
| Drop-ship | Direct delivery (PurchTable.DirectDelivery via SalesLine) | Third-party order TAS, PO from requirement, statistical GR | drop_ship flow | full |
| Delivery schedules | SalesLine delivery schedule (SalesDeliverySchedule) | VBEP schedule lines | order_schedule_line → shipments | full |
| ASN | AIF ASN outbound (DESADV) | DESADV IDoc from delivery | ASN JSON event + EDI mapping hook (w35) | partial |
| Shipment tracking | Shipment (WMSShipment), tracking no. on packing slip | VTTK shipment, tracking via TM | carrier_shipment.tracking events | improved |

### 5.28.3 Functional requirements
- **FR-28.1 Release rules** — Must. The system shall release confirmed, unheld order lines to the warehouse by rule (confirmed ship date ≤ today + release horizon, default 2 d; full ATP available; customer priority), creating one shipment per (order, warehouse, ship date) unless consolidation across orders is enabled.
- **FR-28.2 Picking** — Must. The system shall create pick work grouped by wave (max 200 lines or 50 orders) or batch, reserve physical stock (wave 16), and accept pick confirmation of quantity, dims (batch/serial) and location.
  - Short picks create a backorder remainder and release the unpicked reservation.
- **FR-28.3 Packing** — Must. The system shall pack picked lines into containers (type, tare, dims), nest containers (pallet → carton), and allocate an SSCC per container from the GS1 company prefix with a correct mod-10 check digit.
- **FR-28.4 Packing slip posting** — Must. The system shall post a packing slip that records delivered quantities on lines, creates inventory issue transactions, and posts the journal entries of the matrix in BR-28.1 with the cost snapshot current at posting.
  - Posting is atomic across fulfillment_db, inventory and ledger via a Temporal saga with compensation; packing slip number from sequence `PS-`.
- **FR-28.5 Packing slip cancellation** — Must. The system shall cancel a packing slip that is not yet invoiced, reversing inventory and journal postings with mirror entries referencing the original.
- **FR-28.6 Carrier abstraction** — Must. The system shall expose rate, label, manifest and track operations through a provider-agnostic adapter with reference adapters (EasyPost-style, Shippo-style) and a `file` adapter for manual carriers.
  - Labels returned as PDF and ZPL; tracking polled every 30 min or received by webhook.
- **FR-28.7 Drop-ship** — Must. The system shall create a linked purchase order (wave 17) for drop-ship lines, receive the vendor's shipment confirmation as the packing slip trigger, and post COGS at vendor cost without inventory.
- **FR-28.8 Partial shipments and schedules** — Must. The system shall ship each schedule line separately when partial delivery is allowed and keep the order PARTIALLY_SHIPPED until remaining_qty = 0.
- **FR-28.9 ASN** — Should. The system shall emit an ASN with containers, SSCCs and line quantities on packing-slip post for the wave 35 EDI mapper.
- **FR-28.10 Tracking visibility** — Should. The system shall publish tracking changes and expose them to the portal and order detail.
- **FR-28.11 Mobile pick UI (basic)** — Should. The system shall provide a PWA screen for pick confirmation with barcode entry (GS1-128 parsing of AI 00, 01, 10, 17, 21, 30, 37) and offline tolerance of 5 minutes.
- **FR-28.12 Delivered-not-invoiced reconciliation** — Must. The system shall maintain a delivered_not_invoiced projection equal to GL 1405 and run the daily check.

### 5.28.4 Data model

**shipment**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, shipment_number | uuid, text | Y | SH- sequence |
| legal_entity_id, warehouse_id | uuid | Y | RLS, one warehouse per shipment |
| customer_id, delivery_address_id | uuid | Y | |
| status | enum | Y | OPEN, RELEASED, PICKING, PICKED, PACKED, SHIPPED, DELIVERED, CANCELLED |
| planned_ship_date, actual_ship_date | date, timestamptz | Y/N | |
| carrier_code, service_level, mode_of_delivery | text | N | |
| lines[] | shipment_line | Y | order_line_id, schedule_line_id, qty_planned, qty_picked, qty_packed, qty_shipped, dims |
| consolidation_key | text | N | (customer, address, date) when consolidating |
| packing_slip_id, carrier_shipment_id | uuid | N | |
| version | int | Y | |

**container**: id, shipment_id, parent_container_id, container_type, sscc char(18), tare_weight, gross_weight, length/width/height, uom, lines[] (shipment_line_id, qty, batch, serials[]), label_ref, status (OPEN | CLOSED | SHIPPED).

**packing_slip** (CustPackingSlipJour/Trans): id, packing_slip_number, shipment_id, order_id, posted_at, posting_date, status (POSTED | CANCELLED | INVOICED), lines[] (order_line_id, qty, unit, cost_amount, cost_snapshot_id, inventory_transaction_ids[], journal_posting_id), total_cost, reversal_of_id, document_ref (PDF).

**carrier_shipment**: id, shipment_id, provider, provider_ref, rate_quote jsonb, tracking_number, label_ref (pdf/zpl), status (QUOTED | LABELLED | MANIFESTED | IN_TRANSIT | DELIVERED | EXCEPTION), events[] (timestamp, code, description, location), cost_amount, currency.

Invariants: Σ container line qty per shipment line = qty_packed ≤ qty_picked ≤ qty_planned; packing slip qty = qty_shipped; a packing slip has exactly one journal posting (plus one reversal if cancelled); SSCC check digit valid and unique per tenant; delivered_not_invoiced(order_line) = Σ packing slip qty − Σ invoiced qty, and Σ cost of uninvoiced packing slips = GL 1405. Indexing: shipments by (warehouse, status, planned_ship_date); packing slips by (order_id), (posting_date); containers by sscc unique; partition packing_slip by posting month.

### 5.28.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/fulfillment/releases:run | Run release rules for a warehouse (or specific orders) | Idempotency-Key | WarehouseRelease |
| POST | /api/v1/fulfillment/waves, …/{id}:confirm-pick | Create wave, confirm picks (qty, dims, location) | Idempotency-Key | WarehousePick |
| POST | /api/v1/fulfillment/shipments/{id}/containers, …/containers/{cid}:close | Pack lines, close container (allocate SSCC) | Idempotency-Key | WarehousePack |
| POST | /api/v1/fulfillment/shipments/{id}:rate, :label, :manifest | Carrier operations | Idempotency-Key | WarehouseShip |
| POST | /api/v1/fulfillment/shipments/{id}:post-packing-slip | Post packing slip (saga) | Idempotency-Key | PackingSlipPost |
| POST | /api/v1/fulfillment/packing-slips/{id}:cancel | Reverse packing slip | Idempotency-Key | PackingSlipPost |
| GET | /api/v1/fulfillment/shipments/{id}/tracking | Tracking events | — | ShipmentView / Portal |
| POST | /api/v1/fulfillment/drop-ship/{orderLineId}:vendor-confirm | Vendor ship confirmation triggers packing slip | Idempotency-Key | PurchasingConfirm |
| POST | /webhooks/carriers/{provider} | Carrier tracking webhook (HMAC) | provider event id | system |

Validation: post-packing-slip requires status PACKED, no ACTIVE hold on the order, posting date in an open inventory period (wave 14), and no negative on-hand unless the product's policy allows (wave 16).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.fulfillment.shipment.released.v1 | release run | shipment_id, order_id, warehouse, lines | sales (line status), analytics |
| passage.fulfillment.pick.confirmed.v1 | pick confirm | wave_id, lines[{order_line_id, qty, dims, location}] | inventory (reservation → picked), sales |
| passage.fulfillment.container.closed.v1 | close container | container_id, sscc, lines, weights | ASN, label print |
| passage.fulfillment.packing-slip.posted.v1 | saga complete | packing_slip_id, order_id, lines[{order_line_id, qty, cost_amount, cost_snapshot_id}], journal_posting_id, posting_date | billing, sales, inventory, ledger, portal, credit |
| passage.fulfillment.packing-slip.cancelled.v1 | cancel | packing_slip_id, reversal_journal_posting_id | billing, sales, inventory |
| passage.fulfillment.asn.issued.v1 | packing-slip posted | shipment_id, containers[{sscc, lines}], carrier, tracking | EDI (w35), customer |
| passage.fulfillment.shipment.tracking-updated.v1 | carrier poll/webhook | shipment_id, tracking_number, status, event | portal, sales |

### 5.28.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Fulfillment workspace | tiles: to release, picking, packed not shipped, carrier exceptions, DNI balance | counts | WarehouseView |
| Release board | run rules, release selected, hold | order, customer, ship date, ATP status, priority, hold | WarehouseRelease |
| Wave / pick list | print, confirm pick, short pick, reassign | wave, line, product, location, qty, dims, picker | WarehousePick |
| Packing station | scan line, add container, close (SSCC), print label | container, type, weights, lines, SSCC | WarehousePack |
| Shipment detail | rate, label, manifest, post packing slip, cancel | carrier, service, cost, tracking, documents | WarehouseShip / PackingSlipPost |
| Mobile pick (PWA) | scan, confirm, skip | location, product, qty, batch/serial | WarehousePick |
| Carrier exceptions | retry, switch provider, manual tracking | shipment, provider error, age | WarehouseShip |

Interaction rules: scanning focus is always in the scan field; Enter confirms a line; the packing station parses GS1-128 and auto-fills batch/serial; bulk release on multi-select; posting shows the journal preview (accounts, amounts) before confirm; layouts per warehouse are personalizable.

### 5.28.7 Business rules and edge cases
- **BR-28.1 Packing-slip COGS posting matrix** (accounts from the wave 16 inventory posting profile; dims from the order line):

| Scenario | At packing slip post | At invoice post (wave 29) |
|---|---|---|
| Stocked item, physical posting enabled (default, AX-style) | Dr 1405 Delivered-not-invoiced / Cr 1400 Inventory (physical) at cost snapshot | Reverse 1405/1400 physical; Dr 5000 COGS / Cr 1400 Inventory (financial) at settled cost |
| Stocked item, policy `cogs_at_shipment` (SAP PGI-style) | Dr 5000 COGS / Cr 1400 Inventory | Revenue and AR only |
| Standard-cost item | Same as above at standard; no variance at shipment | Same |
| Drop-ship line | No posting (no inventory) | Dr 5000 COGS / Cr 2110 GR/IR at vendor PO cost when vendor confirmation posts; revenue at invoice |
| Non-stocked / service line | No posting | Revenue only |
| Catch-weight item | Cost on inventory quantity, not catch-weight quantity | Same |
| Packing slip cancellation | Mirror entries with negative amounts, same dims, reversal_of set | — |

- **BR-28.2 Cost snapshot.** Cost at packing slip = running average or FIFO estimate per wave 20 at posting time, stored as cost_snapshot_id; inventory close (wave 20) adjusts 5000/1400 but never 1405 after invoicing.
- **BR-28.3 Posting date** defaults to actual ship timestamp converted to the warehouse time zone; if the inventory period is closed, posting is rejected with `PERIOD_CLOSED` and the shipment stays PACKED.
- **BR-28.4 Saga ordering and compensation.** Steps: (1) write packing slip PENDING; (2) inventory issue transactions; (3) journal posting with Idempotency-Key = packing_slip_id; (4) mark POSTED and emit event. Failure at 3 compensates 2; a retry of 3 with the same key is a no-op at the ledger. Pending slips older than 10 min alert.
- **BR-28.5 SSCC** = extension digit (0–9) + GS1 company prefix + serial reference padded to 17 digits + mod-10 check digit (weights 3,1 from the right); serial reference from a gap-free sequence per prefix; reuse forbidden for 12 months.
- **BR-28.6 Shipment state machine.** OPEN → RELEASED → PICKING → PICKED → PACKED → SHIPPED → DELIVERED; CANCELLED allowed before SHIPPED (releases reservations); SHIPPED → (packing slip cancelled) → PACKED.
- **BR-28.7 Over-pick** beyond planned qty is rejected; over-ship beyond ordered qty is rejected unless the customer allows overdelivery_pct.
- **BR-28.8 Consolidation** merges shipments only when customer, address, warehouse, planned date and carrier match and no order has `ship_separately`.
- **BR-28.9 Carrier failures.** Rate or label errors retry 3× with exponential backoff (2 s, 8 s, 32 s), then land in the exceptions queue; the packing slip can still post with a manual tracking number.
- **BR-28.10 Serial/batch items** must have their dims captured at pick; the packing slip rejects lines with missing tracking dims (wave 8 tracking dimension group).

### 5.28.8 Acceptance criteria
- **AC-28.1** (FR-28.4) Given a packed shipment of 10 units at cost 12.50 USD with physical posting enabled, when the packing slip posts, then inventory on-hand decreases by 10, a journal posting of Dr 1405 125.00 / Cr 1400 125.00 exists, and `packing-slip.posted` carries cost_amount 125.00.
- **AC-28.2** (FR-28.5) Given that packing slip, when cancelled before invoicing, then on-hand increases by 10, a mirror posting exists, and GL 1405 is back to its prior balance.
- **AC-28.3** (FR-28.3) Given company prefix 0614141 and serial 0123456789, then SSCC = 006141410123456789 + check digit 1 (expected 0061414101234567891) and the GS1-128 label scans back to the same value.
- **AC-28.4** (FR-28.2) Given a wave with a 100-unit line and 90 units picked (short), then qty_picked = 90, 10 units remain on backorder, and the reservation for 10 is released.
- **AC-28.5** (FR-28.7) Given a drop-ship line, when the vendor confirms shipment, then a packing slip posts with no inventory transaction and COGS 5000 / GR-IR 2110 at PO cost.
- **AC-28.6** (FR-28.6) Given recorded fixtures for both reference adapters, when rate/label/track are called, then responses match the contract and labels are valid PDF and ZPL.
- **AC-28.7** (FR-28.1) Given 1,000 confirmed orders in the simulated warehouse, when release → pick → pack → post runs end-to-end, then every order is SHIPPED and Σ inventory issues = Σ packing slip quantities with zero mismatch.
- **AC-28.8** (FR-28.12) Given 50 posted packing slips of which 20 invoiced, then the DNI projection equals the cost of the 30 uninvoiced slips and equals GL 1405.
- **AC-28.9** (FR-28.8) Given a line with schedule lines 40 and 5, when the first ships, then the order is PARTIALLY_SHIPPED and remaining_qty = 5.
- **AC-28.10** (FR-28.11) Given scanning `(01)00614141000012(10)LOT7(37)5` on the mobile pick screen, then product GTIN, batch LOT7 and qty 5 are filled.

### 5.28.9 Test plan and fixtures
Fixtures: `warehouse-sim-1k` (1,000 orders across 120 products, 3 warehouses, batch/serial mix); `sscc-200` (valid/invalid check digits from GS1 examples); `carrier-recorded` (EasyPost/Shippo-style HTTP recordings); `dropship-30`; `cogs-matrix-14` (one per matrix cell × costing method, accountant-reviewed).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Posting matrix | contract (ledger) | post/cancel | 14/14 balanced postings; cancel mirrors exactly |
| Saga compensation | integration (Temporal) | post with injected ledger failure | no inventory movement remains; slip not POSTED |
| SSCC | unit | check digit | 200/200 |
| GS1-128 parse | unit/property | parser | all AI combinations round-trip |
| Pick-pack-ship 1k | E2E | simulated warehouse | 0 inventory mismatch, all SHIPPED in < 60 min |
| Carrier adapters | contract | recordings | green |
| DNI = GL 1405 | reconciliation | daily job | difference 0.00 |

Reconciliation: Σ inventory issue cost (wave 16) for packing slips = Σ journal Cr 1400 from fulfillment; DNI projection = GL 1405; order line delivered_qty = Σ packing slip qty.

### 5.28.10 Deployment and operations
Helm `passage-fulfillment` (Go), `passage-carrier-gateway` (TS, egress via Envoy with per-provider secrets from Vault), `fulfillment-db`; PWA served from the shell. Config: `RELEASE_HORIZON_DAYS=2`, `WAVE_MAX_LINES=200`, `GS1_COMPANY_PREFIX` per legal entity, `COGS_AT_SHIPMENT=false`. Flags: `fulfillment.consolidation`, `fulfillment.asn`, `fulfillment.mobile_pick`. Jobs: release run every 15 min per warehouse; tracking poll every 30 min; DNI reconciliation 02:00. SLOs: packing slip post p95 < 2 s, saga completion 99.9% within 60 s; alerts: pending slips > 10 min, carrier error rate > 5%, DNI diff ≠ 0. Runbook: resume stuck saga, re-print label, manual tracking. Rollback: previous digest; posting saga is versioned (Temporal workflow versioning) so in-flight sagas complete on the old code.

### 5.28.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustPackingSlipJour/CustPackingSlipTrans (not invoiced) | packing_slip, DNI opening balance | only slips with uninvoiced remainder; cost from InventTrans CostAmountPhysical |
| WMSShipment, WMSPickingRoute (open) | shipment, pick_work | open picks reset to RELEASED (re-pick) |
| WHSContainerTable (open shipments) | container | SSCC retained |
| LIKP/LIPS with WBSTK ≠ C (not goods-issued) | shipment | status A/B → RELEASED/PICKING |
| LIKP/LIPS goods-issued, not billed (FKSTK ≠ C) | packing_slip | cost from MSEG 601 DMBTR |
| VEKP/VEPO handling units | container | EXIDV SSCC retained |
| ShipCarrierTable | carrier mapping config | manual |

Validation: DNI opening balance = legacy GL delivered-not-invoiced balance to the cent; open shipment line quantities = legacy open delivery quantities. Not migrated: completed shipments (lakehouse), tracking history, picking performance stats.

### 5.28.12 Dependencies, risks, open questions
- Depends on: wave 27 confirmed orders and holds; wave 16 inventory transactions and reservations; wave 20 cost snapshots; wave 17 POs (drop-ship); wave 11 journal; wave 14 period control; wave 8 tracking dimensions and GS1 identifiers; wave 5 PWA shell and document rendering.
- Risks: physical-posting semantics unfamiliar to SAP users (mitigation: policy flag with documented matrix); carrier sandbox instability (mitigation: recorded fixtures are the CI gate, live sandbox nightly only).
- Open questions: (1) Default `cogs_at_shipment` for migrated SAP entities — proposed true; decide by wave 29. (2) Who owns ASN EDI mapping, wave 28 or 35? Proposed 35; decide by wave 28 close.

## 5.29 Wave 29: Billing, Invoicing & Revenue Recognition

### 5.29.1 Scope and business context
Wave 29 ships the `billing` and `revrec` services. Posted packing slips and eligible order lines become invoices (one-off, consolidated per customer/period, subscription, milestone); free-text invoices and credit notes post directly; invoice layouts render to PDF with an e-invoice hook for wave 35. Every posted invoice creates AR open items, tax postings (wave 13) and journal entries, and, where the contract has multiple performance obligations or deferred terms, a revenue contract with standalone selling price allocation and recognition schedules. A billing clerk invoices the day's shipments and a controller sees deferred revenue roll forward. Out of scope: cash application and settlement (wave 30), intercompany invoicing (hook only; wave 40), project billing (wave 41), Peppol transmission (wave 35), usage metering sources beyond API-posted usage.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Revenue allocation for bundles lives in spreadsheets: D365 RevRecSchedule requires revenue prices maintained per item and still cannot explain allocation; SAP RAR (FARR_D_*) is a separate engine with its own posting run and reconciliation (FARR_RECON) | Rev rec is bolted on after billing; allocation inputs are not on the order | Revenue contract created at invoice post from order lines with SSP determination order BR-29.2; allocation and schedules visible on the contract and in the invoice trace | Close-time rev rec effort: 2–3 days per month → < 2 h review; 40/40 bundle fixtures reproducible |
| Invoice totals differ from the order by cents (AX SalesLine rounding per form letter; SAP tax rounding at MWST condition vs. BSEG) | Each document recomputes and rounds independently | Engine net reused; per-line tax; totals property-tested (FR-29.3) | Cent discrepancies: ~0.5% of invoices → 0 |
| Billing is batch: SAP VF04 due list and AX summary update run nightly, so an invoice for today's shipment reaches the customer tomorrow | Billing driven by scheduled jobs over document flow tables | `packing-slip.posted` event creates the proposal immediately; post p95 < 3 s; consolidation is a policy, not a batch | Shipment-to-invoice lead time: 12–24 h → < 5 min for non-consolidated accounts |
| Deferred revenue roll-forward is rebuilt by hand from GL 2400 and schedule exports | No system-produced roll-forward that ties to the GL | `GET /revrec/roll-forward` reconciled nightly to GL 2400 with drill-down to schedules | Roll-forward preparation: 1 day → 0; diff 0.00 every night |
| Credit note against a partially recognized invoice needs manual journal corrections (D365: manual schedule adjustment; RAR: contract change processing) | Credit notes do not know the recognition state of their original | BR-29.6 reverses recognized and deferred proportions automatically | Manual rev rec journals per month: 10–30 → 0 |

### 5.29.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Sales invoice from shipment | SalesFormLetter_Invoice, CustInvoiceJour/CustInvoiceTrans, summary update (SalesSummaryParameters) | VF01/VF04 billing due list, VBRK/VBRP, copy control VTFL | invoice (source PACKING_SLIP), consolidated invoice | full |
| Free-text invoice | CustInvoiceTable/CustInvoiceLine | FI invoice FB70, SD order-related billing without material (DR) | free_text_invoice | full |
| Credit note | Negative invoice / credit note from return (CustInvoiceJour), free-text credit | VF01 from credit memo request (G2), VBRK VBTYP O | credit_note | full |
| Billing plans / milestones | D365 project milestone billing; subscription billing | Billing plan (FPLA/FPLT), milestone/periodic | subscription, milestone_schedule | full |
| Subscription billing | D365 Subscription billing (recurring contract billing) | SAP BRIM / S/4 solution order | subscription with proration | partial (usage basic) |
| AR open item creation | CustTrans / CustTransOpen | BSID (open) / BSAD (cleared), BKPF/BSEG | receivables.open_item via invoice.posted | full |
| Tax on invoice | TaxTrans, TaxGroup/TaxItemGroup | FTXP, MWST condition, BSET | tax engine (w13) call at post | full |
| Revenue recognition | D365 Revenue recognition (RevRecSchedule, RevRecContract, revenue schedule), revenue prices | RAR (Revenue Accounting and Reporting): contracts, POBs, FARR_D_* | revenue_contract, performance_obligation, revenue_schedule | full |
| Deferred revenue postings | Deferred revenue account on item posting; RevRecSchedule processing | RAR posting run, deferred revenue/contract liability | schedule recognition job to 2400/4000 | full |
| Invoice layout / e-invoice | SSRS SalesInvoice report; Electronic reporting (ER) | Smart Forms/Adobe Forms, DRC e-document | docgen template + e-invoice hook | partial (transmission w35) |
| Billing audit trace | Invoice → packing slip → order (CustInvoiceTrans.InventTransId) | Document flow VBFA | trace API | improved |

### 5.29.3 Functional requirements
- **FR-29.1 Invoice from shipment** — Must. The system shall create invoice proposals from posted packing slips (and from order lines for non-stocked items), one invoice per order by default, with lines carrying quantity, price, discounts, charges and tax codes from the order.
  - Invoice number from gap-free sequence `INV-` per legal entity and fiscal year; invoice date defaults to today, may not precede the packing slip date.
- **FR-29.2 Consolidated invoices** — Must. The system shall combine packing slips for the same invoice account, currency, payment terms and billing cycle (daily/weekly/monthly) into one invoice.
- **FR-29.3 Posting** — Must. The system shall post an invoice atomically: journal entries (Dr 1200 AR gross; Cr 4000 revenue per line account; Cr 2300 tax; Cr 2400 deferred revenue for deferred obligations; COGS/DNI reversal per BR-28.1), AR open item creation, and emit `invoice.posted`.
  - Totals = Σ line nets + Σ charges + tax, rounded per currency; a property test enforces this.
- **FR-29.4 Free-text invoices** — Must. The system shall post invoices without products (account, dimensions, amount, tax code) and support recurring free-text templates.
- **FR-29.5 Credit notes** — Must. The system shall post credit notes referencing an original invoice (whole or lines) or standalone, creating negative open items and reversing revenue, tax and recognition.
- **FR-29.6 Tax** — Must. The system shall call the wave 13 tax engine at proposal and again at post, and reject posting if the tax result changed by more than 0.01 since proposal without re-review.
- **FR-29.7 Revenue contracts** — Must. The system shall identify performance obligations per order (one per line, or bundle templates per product), assign standalone selling prices (SSP from price list, percentage of list, or residual), allocate the transaction price proportionally, and create schedules.
- **FR-29.8 Recognition schedules** — Must. The system shall support POINT_IN_TIME (at packing slip or invoice), RATABLE (daily or monthly proration over a term), MILESTONE (manual or event-based) and USAGE (per posted usage record) methods, and post recognition from 2400 to 4000 by a period-end job or on event.
- **FR-29.9 Deferred revenue roll-forward** — Must. The system shall produce a roll-forward (opening, additions, recognized, adjustments, closing) per period and contract that ties to GL 2400.
- **FR-29.10 Subscriptions** — Should. The system shall bill recurring schedules (monthly/quarterly/annual, in advance or arrears) with proration on mid-period start, cancel and plan change, usage-based lines, and generate invoices by a daily job.
- **FR-29.11 Documents** — Must. The system shall render invoice and credit note PDFs from templates (per legal entity, language) and emit the e-invoice document (UBL 2.1 JSON) for wave 35.
- **FR-29.12 Audit trace** — Should. The system shall expose invoice → packing slip → order line → price explain → revenue schedule as one trace.
- **FR-29.13 Intercompany hook** — Could. Invoices to a customer flagged as an intercompany party emit `invoice.posted` with `intercompany=true` for wave 40.

### 5.29.4 Data model

**invoice** (CustInvoiceJour / VBRK)

| Field | Type | Required | Notes |
|---|---|---|---|
| id, invoice_number | uuid, text | Y | gap-free per entity+fiscal year |
| legal_entity_id, invoice_account_id, customer_id | uuid | Y | |
| invoice_type | enum | Y | SHIPMENT, CONSOLIDATED, FREE_TEXT, SUBSCRIPTION, MILESTONE, CREDIT_NOTE |
| status | enum | Y | PROPOSAL, POSTED, PARTIALLY_SETTLED, SETTLED, REVERSED |
| invoice_date, posting_date, due_date | date | Y | due from payment terms |
| currency, exchange_rate_id | char(3), uuid | Y | 3-currency amounts per wave 12 |
| net_amount, charge_amount, tax_amount, gross_amount | numeric(19,4) | Y | gross = net + charges + tax |
| cash_discount_date, cash_discount_amount | date, numeric | N | |
| journal_posting_id, tax_document_id | uuid | Y (POSTED) | |
| original_invoice_id | uuid | N | credit notes |
| document_ref, einvoice_ref | text | N | PDF, UBL |
| lines[] | invoice_line | Y | see below |

**invoice_line** (CustInvoiceTrans / VBRP): id, invoice_id, line_no, order_line_id, packing_slip_line_id, product_id, description, qty, unit, unit_price, disc_pct_1/2, line_net, tax_code, tax_amount, revenue_account, dimensions jsonb, cost_amount, performance_obligation_id, price_explain_ref.

**revenue_contract / performance_obligation / revenue_schedule**: contract (id, order_id, customer_id, currency, transaction_price, allocation_method, status OPEN | COMPLETE | CANCELLED); obligation (id, contract_id, product_id or bundle_component, ssp_amount, ssp_source (LIST | PCT_OF_LIST | RESIDUAL), allocated_amount, method (POINT_IN_TIME | RATABLE | MILESTONE | USAGE), term_start, term_end, satisfied_at, deferred_account 2400, revenue_account 4000); schedule (id, obligation_id, period_id, scheduled_date, amount, status SCHEDULED | RECOGNIZED | REVERSED, journal_posting_id).

**subscription**: id, customer_id, order_id, plan, billing_cycle, bill_in_advance bool, start_date, end_date, next_bill_date, proration_method (DAILY), status (ACTIVE | PAUSED | CANCELLED), lines[] (product, qty, unit_price, usage_based bool, usage_unit).

Invariants: gross_amount = Σ line_net + Σ charges + Σ tax exactly after per-line rounding; Σ allocated_amount over obligations = transaction_price (residual cents to the largest obligation); Σ schedule amounts per obligation = allocated_amount; a POSTED invoice is immutable (corrections only via credit note); for each period, opening + additions − recognized ± adjustments = closing deferred revenue = GL 2400. Indexing: invoices by (invoice_account_id, status, due_date), unique invoice_number per (entity, fiscal_year); schedules by (period_id, status); partition invoices by posting month.

### 5.29.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/billing/proposals | Build proposal from packing slips / orders / subscriptions (filters, consolidation) | Idempotency-Key | InvoiceMaintain |
| POST | /api/v1/billing/invoices/{id}:post | Post proposal (tax, journal, AR) | Idempotency-Key = invoice id | InvoicePost |
| POST | /api/v1/billing/free-text-invoices, …/{id}:post | Free-text invoices | Idempotency-Key | InvoiceMaintain / InvoicePost |
| POST | /api/v1/billing/credit-notes, …/{id}:post | Credit notes (ref original) | Idempotency-Key | CreditNotePost |
| GET | /api/v1/billing/invoices/{id}/document?format=pdf\|ubl | Rendered documents | — | InvoiceView / Portal |
| GET | /api/v1/billing/invoices/{id}/trace | Audit trace | — | InvoiceView |
| POST | /api/v1/revrec/contracts/{id}:allocate, GET …/contracts/{id} | Allocation and contract view | Idempotency-Key | RevRecMaintain |
| POST | /api/v1/revrec/recognition-runs | Period recognition run | Idempotency-Key (period) | RevRecPost |
| POST | /api/v1/revrec/milestones/{obligationId}:satisfy | Satisfy milestone | Idempotency-Key | RevRecMaintain |
| GET | /api/v1/revrec/roll-forward?period= | Deferred revenue roll-forward | — | RevRecView |
| POST | /api/v1/billing/subscriptions, …/{id}:change, :cancel; POST /usage | Subscriptions and usage records | Idempotency-Key | SubscriptionMaintain |

Validation: post rejects if posting_date is in a closed AR or GL period, if any line lacks a revenue account, if tax engine unavailable (no silent zero tax), and if a credit note exceeds the original's remaining creditable amount.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.billing.invoice.posted.v1 | post | invoice_id, number, type, customer, invoice_account, currency, dates, lines[{order_line_id, product, qty, net, tax, revenue_account, obligation_id}], totals, journal_posting_id, intercompany | receivables, revrec, ledger, pricing (rebate/commission), portal, sales |
| passage.billing.credit-note.posted.v1 | post | credit_note_id, original_invoice_id, lines, totals, journal_posting_id | receivables, revrec, ledger, portal |
| passage.billing.invoice.reversed.v1 | reversal (same day, unsettled) | invoice_id, reversal_posting_id | receivables, revrec |
| passage.revrec.contract.allocated.v1 | allocate | contract_id, obligations[{id, ssp, allocated, method}] | analytics, audit |
| passage.revrec.schedule.recognized.v1 | run / event | schedule_id, obligation_id, period_id, amount, journal_posting_id | ledger, analytics |
| passage.billing.subscription.billed.v1 | daily job | subscription_id, invoice_id, period, proration | portal, analytics |

### 5.29.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Billing workspace | tiles: ready to invoice, proposals, failed postings, deferred balance, subscriptions due | counts, amounts | InvoiceView |
| Billing due list | filter, select, build proposals, consolidate | customer, packing slips, amount, cycle | InvoiceMaintain |
| Invoice proposal / detail | edit charges, review tax, post, print, credit | lines, tax breakdown, totals, obligations | InvoicePost |
| Free-text invoice | add lines, recurring template, post | account, dims, amount, tax code | InvoiceMaintain |
| Revenue contract | view allocation, adjust SSP (duty), satisfy milestone | obligation, SSP, allocated, recognized, deferred | RevRecMaintain |
| Deferred revenue roll-forward | run period, drill to schedules | opening, additions, recognized, adjustments, closing, GL 2400 | RevRecView |
| Subscriptions | create, change plan, pause, cancel, view upcoming | plan, cycle, next bill, proration preview | SubscriptionMaintain |

Interaction rules: proposals show tax and journal preview before post; posting is a single confirm with a 5-second undo window only before the journal call starts; grid supports multi-select post (≤ 500 invoices per batch) with per-row result; roll-forward drill-down opens schedules filtered by period; column sets personalizable.

### 5.29.7 Business rules and edge cases
- **BR-29.1 ASC 606 allocation worked example.** Order bundle: hardware unit (SSP 8,000.00 USD, POINT_IN_TIME at packing slip), installation service (SSP 1,500.00, MILESTONE on completion), 12-month support (SSP 2,500.00, RATABLE monthly). Σ SSP = 12,000.00; contract price 10,800.00 (bundle discount 10%). Allocation factor = 10,800 / 12,000 = 0.9 → hardware 7,200.00, installation 1,350.00, support 2,250.00 (Σ = 10,800.00). Invoice at shipment on 2027-03-10 for 10,800.00 posts Dr 1200 10,800.00 (plus tax), Cr 4000 7,200.00 (hardware satisfied), Cr 2400 3,600.00 (installation + support deferred). Installation satisfied 2027-03-25: Dr 2400 / Cr 4000 1,350.00. Support term 2027-03-10 to 2028-03-09, monthly ratable 2,250.00 / 12 = 187.50 per month: first partial month by days (22/31 × 187.50 = 133.06), last month gets the residual so Σ = 2,250.00. If the contract price had been 10,799.99 the allocation 7,199.99/1,349.99/2,249.99 sums to 10,799.97; the 0.02 residual goes to the largest obligation (hardware → 7,200.01).
- **BR-29.2 SSP determination order.** Explicit SSP on product × customer group → product list price (PRICE condition at access 7) → percentage of list per product group → residual (allowed for at most one obligation per contract).
- **BR-29.3 Rounding.** Line net rounded half-even to minor units; tax per line per wave 13 then summed (not tax on total) unless the tax engine returns document-level rounding; allocation at 2 decimals with residual rule; ratable schedules at 2 decimals with the last period absorbing the residual.
- **BR-29.4 Invoice date rules.** invoice_date ≥ packing slip posting date; posting_date must be in an open AR period; due_date = invoice_date + terms (net days, end-of-month variants from wave 7); cash discount date per terms.
- **BR-29.5 Consolidation** groups by (invoice_account, currency, payment_terms, billing_cycle, delivery address if `split_by_address`); max 2,000 lines per invoice; a packing slip belongs to at most one invoice.
- **BR-29.6 Credit note on partially recognized revenue** reverses recognized revenue proportionally (Dr 4000) and deferred revenue for unrecognized schedules (Dr 2400) in the credit note's period; schedules after the credit are marked REVERSED.
- **BR-29.7 Invoice state machine.** PROPOSAL → POSTED → PARTIALLY_SETTLED → SETTLED (wave 30); POSTED → REVERSED only on the same posting date with no settlements, else use a credit note. Schedule: SCHEDULED → RECOGNIZED → REVERSED (by credit note only).
- **BR-29.8 Subscription proration** is daily: amount × days in service / days in billing period, including 29 February when present; a plan change mid-period credits the unused days of the old plan and bills the new plan's remaining days on one invoice; cancellation effective date ≥ today.
- **BR-29.9 Concurrency.** Two posts of the same proposal share the Idempotency-Key and return the same invoice; gap-free number allocation uses a serialized sequence per (entity, fiscal_year) inside the posting transaction; a failed post releases nothing (number not consumed) because the number is drawn after tax and journal succeed in the saga's final step.
- **BR-29.10 Usage records** are accepted up to 45 days after the service period end; later records go to the next period with `late=true`.

### 5.29.8 Acceptance criteria
- **AC-29.1** (FR-29.3) Given 1,000 random invoices in USD, EUR and JPY, then gross = Σ line_net + charges + tax for every invoice after rounding (property).
- **AC-29.2** (FR-29.7) Given the BR-29.1 bundle, when allocated, then obligations are 7,200.00 / 1,350.00 / 2,250.00 and the first recognized postings match the example to the cent.
- **AC-29.3** (FR-29.7) Given the 40 ASC 606 fixtures, then allocation and recognition per period equal the accountant-reviewed workbook for 40/40.
- **AC-29.4** (FR-29.9) Given period 2027-03 with additions 3,600.00 and recognized 1,483.06, then the roll-forward closing 2,116.94 equals GL 2400.
- **AC-29.5** (FR-29.2) Given 3 packing slips for the same account on a weekly cycle, then one invoice with all lines is proposed and each slip's status becomes INVOICED on post.
- **AC-29.6** (FR-29.5) Given a posted invoice of 10,800.00 with 1,350.00 recognized installation, when a full credit note posts, then AR −10,800.00, revenue reversed 8,550.00, deferred reversed 2,250.00 less recognized support.
- **AC-29.7** (FR-29.10) Given a monthly 300.00 EUR subscription starting 2028-02-10 billed in advance, then the first invoice is 300 × 20/29 = 206.90 EUR.
- **AC-29.8** (FR-29.6) Given the tax engine is unavailable, when posting, then the post fails with 503 `TAX_UNAVAILABLE` and no journal or open item exists.
- **AC-29.9** (FR-29.4) Given a free-text invoice line to account 4900 with 20% VAT in DMF-DE, then the posting is Dr 1200 120.00 / Cr 4900 100.00 / Cr 2300 20.00 EUR.
- **AC-29.10** (FR-29.12) Given invoice INV-000123, when the trace is requested, then it returns packing slip, order line, price explain and schedules in one response.

### 5.29.9 Test plan and fixtures
Fixtures: `asc606-40` (bundles with SSPs, discounts, milestones, modifications; accountant-reviewed workbook `fixtures/revrec/asc606-40.xlsx`); `invoices-random-1k` (property seed); `subscription-edges-25` (mid-period, leap day, plan change, cancel); `consolidation-12`; `credit-notes-15`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Totals property | property | post | gross = Σ components for 1,000 random invoices in 3 currencies |
| ASC 606 fixtures | unit | revrec | 40/40 match workbook |
| Roll-forward = GL 2400 | reconciliation | nightly | difference 0.00 |
| Proration edges | unit | subscription | 25/25 expected amounts |
| Posting saga | integration | billing ↔ tax ↔ ledger ↔ receivables | no partial state on injected failures |
| Documents | snapshot | docgen templates | PDF text and UBL schema valid |
| Billing ↔ fulfillment, receivables | contract | Pact | green |

Reconciliation: Σ POSTED invoice gross − credit notes = AR open item additions (wave 30 receivables) = GL 1200 debits from billing; Σ revenue lines recognized = GL 4000 credits from billing + revrec; DNI reversals = packing slip costs invoiced.

### 5.29.10 Deployment and operations
Helm `passage-billing`, `passage-revrec` (Go), databases `billing-db`, `revrec-db`; docgen templates mounted from a ConfigMap per legal entity. Config: `BILLING_MAX_LINES=2000`, `BILLING_BATCH_MAX=500`, `REVREC_RESIDUAL_RULE=largest`, `USAGE_LATE_DAYS=45`. Flags: `billing.consolidation`, `billing.subscriptions`, `revrec.usage`, `billing.einvoice`. Jobs: subscription billing 03:00; recognition run on period close trigger (wave 14) and nightly preview; roll-forward reconciliation 02:30. SLOs: post p95 < 3 s, availability 99.95%; alerts: failed posts > 1% in 15 min, roll-forward diff ≠ 0, recognition run > 30 min. Runbook: re-run failed proposal, reverse same-day invoice, regenerate document. Rollback: previous digest; migrations additive; recognition job idempotent per period.

### 5.29.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustInvoiceJour/CustInvoiceTrans (open per CustTransOpen) | invoice (POSTED), lines | header amounts and due dates; lines for trace only |
| CustInvoiceTable/CustInvoiceLine (posted free-text, open) | invoice type FREE_TEXT | |
| RevRecSchedule / RevRecContract (D365) | revenue_contract, obligations, schedules (unrecognized) | recognized history as opening 4000; unrecognized = opening 2400 |
| VBRK/VBRP with open BSID | invoice, lines | VBTYP O → CREDIT_NOTE |
| BSID open items | receivables.open_item (loaded in wave 30, schema from wave 27) | |
| FARR_D_CONTRACT / FARR_D_POBS / FARR_D_DEFITEM (RAR) | revenue_contract, obligations, schedules | deferred items per POB → schedules |
| FPLA/FPLT billing plans (open) | subscription / milestone_schedule | |

Validation: Σ migrated open invoice balances = legacy AR trial balance per customer; Σ unrecognized schedules = legacy deferred revenue GL balance. Not migrated: settled invoices older than the current fiscal year (lakehouse), SSRS/Smart Forms layouts (re-templated), RAR posting history.

### 5.29.12 Dependencies, risks, open questions
- Depends on: wave 28 packing slips; wave 27 order lines and terms; wave 13 tax engine; wave 11/12 journal and currencies; wave 14 period control; wave 26 explain refs and rebate basis; wave 5 document rendering and gap-free sequences.
- Risks: gap-free numbering under saga failure (mitigation BR-29.9); SSP data missing for migrated products (mitigation: residual method with warning report).
- Open questions: (1) Tax rounding at document vs. line level per country — proposed line level unless the country pack says otherwise; decide by wave 30. (2) Should subscription invoices be gap-free per subscription or per entity? Proposed per entity; decide by wave 29 mid-point.

## 5.30 Wave 30: Accounts Receivable, Collections, Returns & Customer Portal

### 5.30.1 Scope and business context
Wave 30 closes the loop: the `receivables` service applies cash (manual, BAI2 lockbox, remittance advice, camt.053 bank statement with ML-assisted suggestions) and settles open items; `collections-workflows` (TS on Temporal) runs aging, dunning letters, interest notes, promises to pay, disputes and customer risk scoring; return orders flow through RMA with disposition codes to inventory and credit notes; wave 26 rebate accruals settle; customer statements are produced; and the customer portal at `customer.<env>.passage.dev` exposes orders, invoices, payments (Stripe/Adyen adapter in sandbox), returns and documents. The wave ends with the Phase 5 exit: the demo manufacturer's one-month simulation with reconciled subledgers and stakeholder sign-off. Out of scope: bank statement reconciliation of non-customer items and cash forecasting (wave 36), legal-collections agency integration, portal self-registration beyond invited contacts.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Cash application is manual: AX settle open transactions form (CustOpenTrans) requires the clerk to find invoices per check; SAP F-28 "process open items" plus FLB2 lockbox post-processing lands 30–50% of payments on account | Matching is exact-reference only; no scoring, no subset-sum, no remittance parsing | BR-30.1 scored rules (R1–R7) with auto-apply at ≥ 85 and ML-ranked suggestions, across lockbox, camt.053, remittance and portal sources | Auto-match rate: 40–60% → ≥ 90%; clerk time per payment 3 min → < 20 s for suggestions; false matches 0 |
| Dunning runs produce wrong letters: F150 MHNK proposals ignore disputes unless FSCM is configured; AX CustCollectionLetterJour cannot skip promised items | Exclusion state (dispute, promise) is not part of the dunning selection | Exclusions evaluated at issue time from open_item.dispute_id/promise_id; grace, interval and minimum rules in BR-30.2 | Letters later retracted: ~5% → 0; run preparation 2 h → < 10 min review |
| Returns need three documents keyed by hand (AX ReturnTable → arrival journal → credit note; SAP RE order → LR delivery → G2 credit memo request → VF01) | Each step is a separate form with manual reference entry | One RMA with dispositions (BR-30.3) drives receipt, scrap/repair/replace and credit note in a saga | Clicks per return: ~45 across 3 forms → ≤ 8; RMA cycle time 3–5 days → same day after receipt |
| Customers phone for statements, invoice copies and order status; AR clerks re-send PDFs | No customer-facing surface tied to live open items | Customer portal on the same `open-items`, `invoices` and `shipments` APIs, with card/ACH payment and return requests | Inbound "copy of invoice/status" requests: baseline → −80%; invoice-to-cash days −5 via portal payments |
| AR-to-GL reconciliation is a month-end exercise (CustTrans vs. LedgerTrans; BSID vs. GL recon account via F.03) | Subledger and GL are updated by different code paths | Every settlement posts through the journal API; Σ open_amount = GL 1200 checked daily, surfaced in the reconciliation pack | AR reconciliation effort: 1–2 days per month → 0; diff 0.00 for 31/31 days in the simulation |

### 5.30.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Customer payments and settlement | LedgerJournalTrans (CustPaymJournal), CustTrans/CustSettlement, CustPaymModeTable, settle open transactions | F-28 incoming payment, F-32 clear, BSID → BSAD, OB41 posting keys | payment, settlement, open_item | full |
| Lockbox | CustLockbox / lockbox import (BAI2) | FLB2 lockbox (BAI2), FLB1 | lockbox_batch (BAI2) | full |
| Bank statement auto-match | Advanced bank reconciliation (BankStmtISO camt.053) | FF_5 / EBS with camt.053, auto-clearing | camt.053 import + matcher | improved (ML suggest) |
| Aging and collections workspace | CustAgingLegalEntity, Collections workspace, CustCollectionsPool, cases | FBL5N, FSCM Collections (UDM_SPECIALIST worklist, strategies) | aging snapshot, collections worklist | full |
| Collection letters / dunning | CustCollectionLetterJour/Line, collection letter sequence, grace days | F150 dunning (MHNK/MHND), dunning procedure FBMP, levels | dunning_run, dunning_letter | full |
| Interest notes | CustInterestJour/Trans, interest codes | F.24 / F.2B interest calculation | interest_note | full |
| Promise to pay, disputes | Collections promise to pay; cases | FSCM dispute management (UDM_DISPUTE, FDM) | promise_to_pay, dispute | full |
| Return orders / RMA | ReturnTable (SalesTable SalesType=ReturnItem), ReturnDispositionCode, ReturnReasonCode, return reason, RMA number | VA01 order type RE, return delivery LR, credit memo request G2, S/4 advanced returns (MSR) | return_order (sales), return_receipt (fulfillment), credit_note (billing) | full |
| Rebate settlement | Rebate settlement (TAM / RebateManagement to credit note or AP) | VBO1 settlement, credit memo B1/B2 | rebate_settlement → credit_note or payment | full |
| Customer statements | CustAccountStatementExt | F.27 periodic statements (correspondence) | statement job + portal | full |
| Customer portal | D365 Customer portal (Power Pages) | SAP Commerce / Fiori self-service | customer-portal (React, BFF) | improved |
| Card payments | Payment services (Adyen connector in D365) | SAP digital payments add-on | payments-gateway (Stripe/Adyen) | full |

### 5.30.3 Functional requirements
- **FR-30.1 Open items and settlement** — Must. The system shall maintain AR open items from posted invoices, credit notes, payments and adjustments, and settle them fully or partially with cash discount, write-off (tolerance), and FX gain/loss (wave 12) posted at settlement.
- **FR-30.2 Manual payments** — Must. The system shall record customer payments in a payment journal (payment mode: check, ACH, wire, card, cash) and post Dr 1000 bank (or 1010 undeposited) / Cr 1200; unapplied amounts post to 1205.
- **FR-30.3 Lockbox BAI2** — Must. The system shall import BAI2 lockbox files (record types 1–9), create one payment per check with remittance detail, and run matching.
- **FR-30.4 Remittance advice** — Should. The system shall ingest remittance advice (CSV, JSON, EDI 820 hook) and attach it to payments for matching.
- **FR-30.5 camt.053 auto-match** — Must. The system shall import camt.053 statements, identify customer credits (Ntry/CdtDbtInd=CRDT) with RmtInf and debtor data, and match them with the rules in BR-30.1 plus an ML suggestion model (Python, wave 46 provider layer not required) that ranks candidates but never auto-applies on its own.
- **FR-30.6 Aging and worklist** — Must. The system shall compute aging snapshots (buckets current, 1–30, 31–60, 61–90, 91–180, > 180 by due date or invoice date) nightly and a collections worklist ranked by risk score.
- **FR-30.7 Dunning** — Must. The system shall run dunning per legal entity with levels, grace days, minimum amounts, blocks and intervals (BR-30.2), generate letters via docgen, post fees, and raise the customer's dunning level.
- **FR-30.8 Interest notes** — Should. The system shall calculate interest on overdue items (annual rate, day-count ACT/365, minimum 5.00 USD / 5.00 EUR) and post interest notes as open items.
- **FR-30.9 Promises and disputes** — Must. The system shall record promises to pay (amount, date) and disputes (reason, amount, owner, SLA) that exclude items from dunning and interest while open, with a Temporal workflow for review and resolution (credit, paid, write-off, rejected).
- **FR-30.10 Risk scoring** — Should. The system shall compute a 0–100 customer risk score from overdue ratio, average days to pay, dunning level, disputes and broken promises, published to the credit module.
- **FR-30.11 Return orders (RMA)** — Must. The system shall create return orders referencing original invoice lines (or blind returns), issue an RMA number with a 30-day validity, receive goods to a quarantine/inspection location, apply disposition codes (BR-30.3), post inventory receipts at return cost, and create the credit note with restocking charges.
- **FR-30.12 Rebate settlement** — Must. The system shall settle wave 26 accruals per agreement period into a credit note (default) or an AP payment (wave 19 hook), reversing 2150 and emitting `rebate.settled`.
- **FR-30.13 Statements** — Should. The system shall generate customer statements (open items, activity, aging) monthly and on demand, as PDF and in the portal.
- **FR-30.14 Customer portal** — Must. The system shall provide invited customer contacts with order entry and status, invoices and documents, payments by card/ACH via the payments gateway, return requests and shipment tracking, authorized by Keycloak (portal realm) and OPA scoped to the contact's accounts.
- **FR-30.15 Phase-exit simulation** — Must. The system shall support the one-month simulation harness (`sim/phase5-month`) that drives Phases 3–5 end-to-end and produces the reconciliation pack in §5.0.

### 5.30.4 Data model

**open_item** (CustTrans/CustTransOpen, BSID)

| Field | Type | Required | Notes |
|---|---|---|---|
| id | uuid | Y | |
| legal_entity_id, customer_id, invoice_account_id | uuid | Y | RLS |
| source_type, source_id | enum, uuid | Y | INVOICE, CREDIT_NOTE, PAYMENT, INTEREST, FEE, ADJUSTMENT, REBATE |
| document_number, document_date, due_date | text, date, date | Y | |
| currency, amount, amount_entity_currency | char(3), numeric(19,4), numeric(19,4) | Y | sign: debit +, credit − |
| open_amount | numeric(19,4) | Y | = amount − Σ settlements |
| cash_discount_date, cash_discount_amount | date, numeric | N | |
| dunning_level, last_dunned_at, dunning_block | smallint, date, bool | Y | default 0/null/false |
| dispute_id, promise_id | uuid | N | exclusion flags |
| status | enum | Y | OPEN, PARTIALLY_SETTLED, SETTLED, REVERSED |

**payment**: id, payment_number, customer_id (nullable until identified), payment_mode (CHECK | ACH | WIRE | CARD | CASH), amount, currency, received_date, value_date, bank_account_id, source (MANUAL | LOCKBOX | CAMT053 | PORTAL | REMITTANCE), source_ref (batch id, Ntry id, Stripe charge id), payer_name, payer_account_hash, remittance[] (reference, amount), status (RECEIVED | SUGGESTED | APPLIED | ON_ACCOUNT | REVERSED), match_score, journal_posting_id. **settlement** (CustSettlement): id, payment_id or credit_item_id, open_item_id, settled_amount, cash_discount_taken, write_off_amount, fx_gain_loss, settled_at, journal_posting_id, reversal_of_id.

**dunning_run / dunning_letter**: run (id, legal_entity_id, run_date, parameters, status DRAFT | REVIEWED | ISSUED | CANCELLED); letter (id, run_id, customer_id, level, items[] (open_item_id, open_amount, days_overdue), fee_amount, document_ref, issued_at). **dispute**: id, customer_id, open_item_id, reason_code, disputed_amount, owner, status (OPEN | UNDER_REVIEW | RESOLVED_CREDIT | RESOLVED_PAID | RESOLVED_WRITE_OFF | REJECTED | CLOSED), sla_due, workflow_run_id.

**return_order** (ReturnTable): id, rma_number, customer_id, original_invoice_id, return_reason_code, status (REQUESTED | AUTHORIZED | RECEIVED | INSPECTED | DISPOSITIONED | CREDITED | CLOSED | REJECTED | EXPIRED), expires_at, lines[] (original_invoice_line_id, product, qty_expected, qty_received, disposition_code, return_cost_method (ORIGINAL_COST | CURRENT_COST | ZERO), restocking_pct, restocking_amount, inventory_receipt_ids[], credit_note_line_id).

Invariants: Σ settlement.settled_amount per open item ≤ |amount|; open_amount = 0 ⇔ status SETTLED; Σ open_amount over OPEN items per entity = GL 1200 balance; Σ ON_ACCOUNT payments = GL 1205; a payment is APPLIED only when Σ settlements + residual (on account) = payment amount; dunning_level never decreases while the item is open; an item under dispute or promise is excluded from dunning and interest; RMA lines credited ≤ qty received × unit credit. Indexing: open items by (customer_id, status, due_date), (document_number); payments by (status, received_date), (source, source_ref) unique; partition settlements by month.

### 5.30.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /api/v1/receivables/open-items?customer=&status=&asOf= | Open items (FBL5N) | — | ARView / Portal (own) |
| POST | /api/v1/receivables/payments, …/{id}:apply, :on-account, :reverse | Manual payment lifecycle | Idempotency-Key | ARPaymentPost |
| POST | /api/v1/receivables/lockbox/imports (BAI2), /bank-statements/imports (camt.053) | File imports | file hash | ARImport |
| POST | /api/v1/receivables/payments/{id}:match | Run matching, return scored candidates | stateless | ARPaymentPost |
| POST | /api/v1/receivables/settlements:batch | Settle items with credits/payments; write-off/discount | Idempotency-Key | ARSettle |
| GET | /api/v1/receivables/aging?asOf=&bucketBy= | Aging snapshot | — | ARView |
| POST | /api/v1/receivables/dunning-runs, …/{id}:review, :issue, :cancel | Dunning lifecycle | Idempotency-Key | DunningRun |
| POST | /api/v1/receivables/interest-notes:calculate, :post | Interest | Idempotency-Key | InterestPost |
| POST | /api/v1/receivables/promises, /disputes, /disputes/{id}:resolve | Promises and disputes | Idempotency-Key | CollectionsMaintain |
| POST | /api/v1/sales/return-orders, …/{id}:authorize, /lines/{l}:receive, :disposition | RMA lifecycle (sales + fulfillment + billing saga) | Idempotency-Key | ReturnMaintain / WarehouseReceive |
| POST | /api/v1/pricing/rebates/settlements | Settle accruals for period | Idempotency-Key (agreement, period) | RebateSettle |
| GET/POST | /portal/api/v1/{orders,invoices,payments,returns,shipments,documents} | Portal BFF (scoped) | Idempotency-Key | PortalCustomer |
| POST | /api/v1/payments-gateway/intents, /webhooks/{provider} | Card/ACH intents and provider webhooks | provider id | system |

Validation: apply rejects when Σ settlements ≠ payment amount − on-account residual; settlement rejects cross-currency unless FX settlement enabled; dunning issue requires status REVIEWED; disposition requires qty_received > 0; portal payments require an open item set with Σ open ≥ amount.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.receivables.payment.received.v1 | import/manual/portal | payment_id, source, amount, currency, payer, remittance[] | matcher, analytics |
| passage.receivables.payment.applied.v1 | apply | payment_id, settlements[{open_item_id, amount, discount, write_off, fx}], residual, journal_posting_id | ledger, sales (credit), portal, collections |
| passage.receivables.customer-balance.changed.v1 | any open item change | customer_id, balance, overdue, oldest_item_days, dunning_level, risk_score | sales credit, portal |
| passage.receivables.dunning-letter.issued.v1 | issue | run_id, customer_id, level, items[], fee, document_ref | docgen, portal, sales (hold at level 4) |
| passage.receivables.dispute.opened.v1 / resolved.v1 | dispute lifecycle | dispute_id, open_item_id, amount, reason, resolution | collections, billing (credit note), analytics |
| passage.receivables.return-order.authorized.v1 / dispositioned.v1 | RMA | rma_id, line, disposition, qty, restocking, credit_note_id | fulfillment, inventory, billing, portal |
| passage.pricing.rebate.settled.v1 | settlement | agreement_id, period_id, amount, credit_note_id or payment_ref | ledger, receivables, analytics |

### 5.30.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| AR workspace | tiles: unapplied cash, overdue by bucket, disputes open, dunning due, RMAs awaiting inspection | amounts, counts | ARView |
| Cash application | import, view suggestions, accept/adjust, apply, on-account, split | payment, payer, amount, candidates with score and reason, residual | ARPaymentPost |
| Open items / settlement | select items, settle, discount, write-off, reverse | document, due, open amount, discount available, dunning level | ARSettle |
| Collections worklist | call/email log, promise, dispute, hold order, next action date | customer, risk score, overdue, oldest item, promises, last contact | CollectionsMaintain |
| Dunning run | create, preview letters, exclude, issue | customer, level, items, fee, blocks | DunningRun |
| Dispute detail | assign, add evidence, resolve (credit/paid/write-off/reject) | amount, reason, SLA, timeline | CollectionsMaintain |
| Return order (RMA) | authorize, receive, inspect, set disposition, credit | RMA, lines, disposition, restocking, credit preview | ReturnMaintain |
| Customer portal | place order, view invoices/documents, pay, request return, track | orders, invoices, balance, payments, RMAs, tracking | PortalCustomer |

Interaction rules: cash application keyboard flow (J/K move candidates, Enter accept, A apply) for clerks; suggestions show the matched rule ids; bulk accept of all ≥ 85 suggestions; worklist personalizable with saved filters; dunning preview opens the rendered letter; portal is WCAG 2.1 AA and mobile-first; portal payments show the provider's hosted fields (no card data touches Passage).

### 5.30.7 Business rules and edge cases
- **BR-30.1 Cash application matching rules and scores.** Candidates are open items of the identified customer (or all customers when unidentified). Score per candidate set: R1 exact document number in remittance/RmtInf/check memo +60; R2 payment amount = open amount of one item +30, or = Σ open of a subset of ≤ 5 items (subset-sum with 0.00 tolerance) +25; R3 payment amount = open − available cash discount (discount date ≥ value_date) +28; R4 payer bank account hash (MICR/IBAN) matches customer bank account on file +15; R5 payer name Jaro-Winkler ≥ 0.92 to customer or invoice account name +10; R6 value_date within 7 days of due date +5; R7 amount short by < 1% or < 50.00 USD / 45.00 EUR +20 with short-pay reason code required. Decision: auto-apply when exactly one candidate set scores ≥ 85 and the runner-up is ≤ winner − 20; 60–84 → SUGGESTED for one-click; < 60 → ON_ACCOUNT (1205) with the top-3 suggestions kept. The ML ranker may reorder suggestions below 85 but cannot raise a set above 85. Every auto-application is reversible within the open period.
- **BR-30.2 Dunning levels and grace.** Level 1 "Reminder": due date + grace days (default 5 per customer group) passed, no fee; Level 2: ≥ 15 days overdue, no fee; Level 3: ≥ 30 days, fee 25.00 USD / 20.00 EUR posted as open item type FEE; Level 4: ≥ 45 days, fee 50.00 USD / 40.00 EUR, legal notice text, emits hold recommendation (sales applies CREDIT hold via CR-3). Rules: minimum letter amount 50.00 USD / 45.00 EUR per customer; interval ≥ 10 days since last letter at any level; an item's level rises by at most one per run; letter level = max item level; customers or items with dunning_block, open disputes, or promises not yet broken (promise date + 2 days) are excluded; items < 1.00 are ignored; a run is a DRAFT until reviewed and issued, and only issue posts fees and levels; full payment resets the item (not the customer's historical level).
- **BR-30.3 RMA dispositions** (ReturnDispositionCode equivalents): CREDIT_ONLY — no physical return, credit note at original price less restocking (default 0%); RETURN_TO_STOCK — receipt to the return warehouse at return cost (ORIGINAL_COST default), then credit less restocking (default 15%, minimum 25.00 USD / 20.00 EUR); SCRAP — receipt then immediate scrap issue to 5900 Scrap, credit less restocking; REPAIR — receipt to quarantine, repair work order hook (Phase 8 service), return to customer, no credit; REPLACE — receipt (as RETURN_TO_STOCK or SCRAP) and a replacement sales order line at zero price referencing the RMA, credit note offset by replacement; REJECT_RETURN_TO_CUSTOMER — receipt to quarantine, ship back, no credit. Disposition is per line and immutable after credit; qty credited = qty received × unit credit price (original invoice unit price net of discounts) less restocking.
- **BR-30.4 Settlement accounting.** Cash discount taken within terms posts Dr 4100 Discounts; write-off within tolerance (default ≤ 5.00 or 0.5% of the item) posts Dr 6100 Write-off; FX difference on settlement of foreign-currency items posts to 7100/7200 realized gain/loss at the settlement date rate; partial settlement keeps the item PARTIALLY_SETTLED with reduced open_amount.
- **BR-30.5 Interest** = open_amount × annual_rate × days_overdue / 365, computed from due date + grace to the calculation date, once per item per period, excluded below the minimum and for disputed/promised items.
- **BR-30.6 Payment state machine.** RECEIVED → SUGGESTED → APPLIED | ON_ACCOUNT → (REVERSED). ON_ACCOUNT can later be applied. Dispute: OPEN → UNDER_REVIEW → RESOLVED_* → CLOSED; REJECTED from OPEN/UNDER_REVIEW. RMA: REQUESTED → AUTHORIZED → RECEIVED → INSPECTED → DISPOSITIONED → CREDITED → CLOSED; REJECTED from REQUESTED; EXPIRED from AUTHORIZED after 30 days without receipt.
- **BR-30.7 Concurrency.** Settlement of an open item takes a row lock; two clerks settling the same item → second gets 409; file imports are deduplicated by SHA-256 of content; camt.053 entries by (statement id, entry reference) unique.
- **BR-30.8 Rebate settlement** runs after period close of the invoice period; settlement amount = Σ accruals − Σ prior settlements for the period; a credit note of type REBATE posts Dr 2150 / Cr 1200; negative settlements (over-accrual) post a free-text invoice.
- **BR-30.9 Portal payments** create a payment intent for selected open items; the provider webhook (signed) creates the payment with source PORTAL and auto-applies by R1 (intent carries document numbers, score 100); refunds flow only through credit notes and the provider refund API with duty ARRefund.
- **BR-30.10 Statements** list items as of the statement date; a statement never changes balances; the portal shows the same open items as `GET /open-items` (single source).

### 5.30.8 Acceptance criteria
- **AC-30.1** (FR-30.5, FR-30.3) Given the 500-payment fixture (lockbox + camt.053 + remittance), when matching runs, then ≥ 450 payments are auto-applied and zero are applied to the wrong items (compared to the fixture key).
- **AC-30.2** (FR-30.1) Given invoice 1,000.00 USD with 2% 10 net 30 and a 980.00 payment on day 8, then the item settles fully with 20.00 discount posted to 4100.
- **AC-30.3** (FR-30.7) Given the dunning fixture of 60 customers (overdue mix, 5 blocked, 4 disputed, 3 with promises), when the run issues, then letters exist for exactly the 31 expected customers at the expected levels, fees 25.00 for level 3 items, and the 4 disputed items appear on no letter.
- **AC-30.4** (FR-30.7) Given a customer dunned at level 2 on 2027-03-05, when a run executes on 2027-03-12, then no letter is issued (interval 10 days).
- **AC-30.5** (FR-30.11) Given an RMA of 4 units at invoice price 100.00 USD with RETURN_TO_STOCK and 15% restocking, when 4 units are received and dispositioned, then an inventory receipt of 4 at original cost exists and a credit note of 340.00 plus tax posts Dr 4000 400.00 / Cr 4200 Restocking 60.00 / Cr 1200 340.00 (plus tax lines).
- **AC-30.6** (FR-30.11) Given disposition SCRAP, then the receipt is followed by a scrap issue posting Dr 5900 / Cr 1400 at return cost.
- **AC-30.7** (FR-30.12) Given a 2% monthly rebate with 10,000.00 accrued for 2027-03, when settled, then a REBATE credit note of 10,000.00 posts and GL 2150 for the agreement is 0.00.
- **AC-30.8** (FR-30.14) Given an invited portal contact, when they pay two invoices totalling 1,250.00 EUR by card in the Stripe sandbox, then a PORTAL payment is created from the webhook and both items are SETTLED within 60 s.
- **AC-30.9** (FR-30.9) Given a dispute on a 500.00 item, when a dunning run executes, then the item is excluded; when the dispute resolves as credit, then a credit note of the disputed amount posts and the item settles.
- **AC-30.10** (FR-30.15) Given the simulation harness runs the 2027-03 month on stage, then every check in §5.0 passes and the reconciliation pack is produced and signed.
- **AC-30.11** (FR-30.6) Given the aging snapshot, then Σ buckets per customer = open balance and Σ over customers = GL 1200.

### 5.30.9 Test plan and fixtures
Fixtures: `cash-app-500` (500 payments with answer key: 300 lockbox BAI2 checks, 150 camt.053 entries, 50 remittances; includes short pays, discounts, multi-invoice, unidentified payers); `dunning-60`; `rma-30` (one per disposition × costing method); `disputes-20`; `rebate-settle-12`; `portal-e2e` (Stripe and Adyen sandbox recordings); `sim/phase5-month` (generated month: 200 POs, 150 production orders, 1,000 orders, 600 payments, 30 RMAs for DMF-US and DMF-DE).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Matching | unit | BR-30.1 | ≥ 90% auto-match, 0 false matches on cash-app-500 |
| Subset-sum bound | property | matcher | ≤ 5 items, completes < 50 ms per payment |
| Dunning | unit/E2E | run | exact expected letter set and levels |
| RMA E2E | E2E | sales + fulfillment + billing + inventory + ledger | 30/30 balanced postings |
| Settlement accounting | contract | ledger | discount, write-off, FX postings balanced |
| BAI2 / camt.053 parsers | unit/property | importers | spec samples parse; malformed files rejected with line numbers |
| Portal | E2E (Playwright + axe) | customer portal | order, pay, return flows pass; 0 serious a11y violations |
| Month simulation | E2E + reconciliation | sim/phase5-month | all §5.0 checks pass, 31/31 daily reconciliations clean |

Reconciliation checks: Σ open_item.open_amount = GL 1200; Σ ON_ACCOUNT = GL 1205; Σ settlements by day = GL 1000/1010 debits from receivables; dunning fees = GL 4300 credits; interest notes = GL 4310; RMA receipts = inventory receipts (wave 16) = GL 1400 debits from returns; rebate settlements = GL 2150 debits; deferred revenue, DNI, inventory, WIP, AP and tax checks from earlier waves re-run on simulation data; trial balance balanced.

### 5.30.10 Deployment and operations
Helm `passage-receivables` (Go), `passage-collections-workflows` (TS, Temporal namespace `collections`), `passage-customer-portal` (React + TS BFF, ingress `customer.<env>.passage.dev`, Keycloak realm `portal`), `passage-payments-gateway` (TS; provider keys in Vault; PCI scope limited to tokens), matcher ML ranker (Python, CPU only). Databases `receivables-db`, `portal-db`. Config: `MATCH_AUTO_THRESHOLD=85`, `MATCH_MARGIN=20`, `DUNNING_INTERVAL_DAYS=10`, `DUNNING_MIN_AMOUNT`, `RMA_EXPIRY_DAYS=30`, `WRITE_OFF_TOLERANCE=5.00/0.5%`. Flags: `ar.ml_ranker`, `ar.camt053`, `collections.interest`, `portal.payments`, `portal.returns`. Jobs: aging and risk score 01:30; lockbox/camt.053 poll every 15 min (SFTP); dunning proposal weekly (issue is manual); statements on the 1st; rebate settlement after period close; interest monthly. SLOs: open-items read p95 < 100 ms, apply p95 < 1 s, portal availability 99.9%, webhook processing < 60 s. Alerts: unapplied cash > 2% of monthly receipts for 3 days, webhook failures, reconciliation diff ≠ 0, dunning run stuck in DRAFT > 7 days. Runbook: reverse a mis-application, re-import a file, re-issue a letter, rotate provider keys. Rollback: previous digest; portal is a separate deployable with its own rollback; Temporal workflows versioned.

### 5.30.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustTrans + CustTransOpen (open) | open_item | AmountCur/AmountMST → amount/entity currency; CollectionLetterCode → dunning_level |
| CustSettlement (current year) | settlement | history for statements |
| CustCollectionLetterJour (last letter per customer) | dunning history | level and date only |
| CustInterestJour (open) | open_item type INTEREST | |
| ReturnTable / SalesLine (ReturnStatus not Closed) | return_order | disposition code mapping to BR-30.3 |
| CustPaymModeTable, CustLockbox setup | payment modes, lockbox config | manual review |
| BSID (open items), BSAD (current year) | open_item, settlement history | MANSP dunning block → dunning_block; MSCHL/MANST → level |
| MHNK/MHND (last run) | dunning history | |
| UDM_DISPUTE cases (open) | dispute | |
| VBAK RE/LR orders open | return_order | |
| KONA rebate accruals (VBOX) open | rebate_accrual opening | settled in Passage |

Validation: Σ open items per customer = legacy AR aging to the cent; count of open disputes and RMAs equal; dunning levels per item equal. Not migrated: cleared items older than the current fiscal year (lakehouse), collection notes/activities (exported to the lakehouse), legacy statements.

### 5.30.12 Dependencies, risks, open questions
- Depends on: wave 29 invoices and credit notes; wave 28 receipts into inventory for RMAs; wave 27 credit profile and holds; wave 26 rebate accruals; wave 12 FX; wave 11 journal; wave 19 AP payments (rebate payout hook); wave 16 inventory; wave 5 documents, Keycloak realms; Phases 3–4 for the simulation.
- Risks: PCI scope creep in the portal (mitigation: hosted fields only, tokens stored, annual SAQ-A); ML ranker drift (mitigation: ranker cannot exceed threshold, weekly precision report); simulation reveals cross-phase defects (mitigation: dry run at wave 28 exit).

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| False auto-match in production | Low | High | threshold + margin rule, reversible, weekly audit sample of 50 |
| Dunning sends to disputed customers | Low | High | exclusion evaluated at issue time, not proposal time |
| RMA credit without receipt | Medium | Medium | CREDIT_ONLY requires duty ReturnCreditOnly and reason code |
| Sign-off slips because accountant review is late | Medium | High | accountant engaged from wave 29 fixtures onward |

- Open questions: (1) Which payment provider is primary for the demo (Stripe or Adyen)? Proposed Stripe sandbox; decide by wave 30 start. (2) Should dunning fees be taxable in DMF-DE? Proposed: follow country pack (wave 48), untaxed in v1; decide by wave 30 mid-point. (3) Does the go-live simulation include DMF-DE intercompany sales to DMF-US? Proposed: no (wave 40); decide by wave 29 close.
