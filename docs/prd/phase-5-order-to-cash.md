# Part 5 — Phase 5: Order-to-Cash (waves 26–30)

## 5.0 Phase overview

**Business outcome at phase exit.** At the end of wave 30 a manufacturer runs the complete buy → make → sell → collect loop on Passage. A sales rep quotes and takes an order at a deterministically resolved price; the order is promised against cumulative ATP and checked against credit; the warehouse picks, packs and ships it with a posted packing slip that moves inventory cost to the universal journal; billing posts the invoice with tax and an ASC 606 / IFRS 15 recognition schedule; cash is applied from lockbox, remittance or bank statement; collections runs dunning and disputes; returns flow through RMA to credit note; and every subledger reconciles to the journal by automated check. Wave 30 is the first go-live candidate.

**Phase-exit sign-off criteria** (each produces an artifact in `reconciliation/phase-5-exit/`):
- One simulated 31-day month (fixture period 2027-03) runs end-to-end on `stage` for demo entities DMF-US (USD) and DMF-DE (EUR): ≥ 200 purchase orders received (Phase 3), ≥ 150 production orders reported as finished (Phase 4), 1,000 sales orders, ≥ 900 shipments, ≥ 950 invoices including ≥ 40 bundles with revenue schedules, 600 customer payments across manual / BAI2 lockbox / camt.053, one dunning run, 30 RMAs, one rebate settlement, inventory close and period close for 2027-03.
- Daily reconciliation passes for all 31 days with zero unexplained differences: AR open items = GL 1200; deferred revenue roll-forward = GL 2400; delivered-not-invoiced = GL 1405; inventory subledger = GL 1400; WIP = GL 1350; rebate accrual = GL 2150; tax payable = tax engine settlement = GL 2300; unapplied cash = GL 1205.
- Trial balance for 2027-03 balances; GL 4000 = Σ posted invoice revenue lines − credit notes − deferrals; GL 5000 = Σ packing-slip cost − return receipts.
- Pricing: 500 scenario fixtures and 10k-request determinism property test green; p95 < 20 ms for a 10-line order.
- Order intake 500 orders/min at p99 < 500 ms; ATP never over-promises (property test over 100 fixtures).
- Cash application ≥ 90% auto-match on the 500-payment fixture with zero false matches; dunning run produces letters for exactly the expected customer set.
- Pick-pack-ship E2E for 1,000 orders with zero inventory mismatch; packing-slip reversal contract tests green.
- Recorded walkthrough (≤ 45 min) and a reconciliation pack signed by a practicing accountant, both linked from the wave 30 issue.

**Cross-wave data model summary**

| Entity | Owning service (Postgres db) | Key relationships |
|---|---|---|
| condition_record, pricing_procedure, rebate_agreement, price_journal | pricing (pricing_db, Go) | → customer/group (w7), product (w8); explain refs consumed by sales/billing |
| sales_quotation, sales_order(_line), sales_agreement, order_hold, credit_profile, atp_bucket | sales (sales_db, Go) | → condition_record; supply/demand from w16/w17/w21/w22; production order link (w21) |
| shipment, container, packing_slip, carrier_shipment | fulfillment (fulfillment_db, Go) + carrier-gateway (TS) | → sales_order_line; inventory_transaction (w16); journal posting (w11) |
| invoice(_line), free_text_invoice, credit_note, subscription | billing (billing_db, Go) | → packing_slip; tax (w13); journal (w11) |
| revenue_contract, performance_obligation, revenue_schedule | revrec (revrec_db, Go) | → invoice_line; journal (w11) |
| open_item, payment, settlement, lockbox_batch, dunning_run, dispute, interest_note, return_order | receivables (receivables_db, Go) + collections-workflows (TS, Temporal) | → invoice, customer, camt.053 statement, journal |
| portal_user, payment_intent | customer-portal (portal_db, TS BFF) + payments-gateway (TS) | → party contact (w7), open_item, Stripe/Adyen adapter |

**Cross-wave event catalog** (CloudEvents on Kafka topics `passage.<domain>`, Avro in schema registry, Debezium outbox)

| Event | Producer | Consumers | Payload summary |
|---|---|---|---|
| passage.pricing.condition-record.activated.v1 | pricing | sales cache, analytics | id, type, keys, validity, version |
| passage.pricing.rebate-accrual.posted.v1 | pricing | ledger, receivables | agreement, period, amount, journal ref |
| dev.passage.sales.order.confirmed.v1 | sales | planning, fulfillment, credit, analytics | order, customer, lines with confirmed dates, totals |
| passage.sales.order-hold.applied.v1 / released.v1 | sales | fulfillment, collections, portal | order, hold type, reason, actor |
| passage.fulfillment.packing-slip.posted.v1 | fulfillment | billing, inventory, ledger, sales, portal | slip, lines, qty, cost snapshot, journal ref |
| dev.passage.billing.invoice.posted.v1 | billing | receivables, revrec, ledger, pricing, portal | invoice, lines, tax, totals, due date |
| dev.passage.revrec.schedule.recognized.v1 | revrec | ledger, analytics | schedule line, obligation, amount, period |
| dev.passage.receivables.payment.applied.v1 | receivables | ledger, sales (credit), portal | payment, settlements[], residual |
| passage.receivables.customer-balance.changed.v1 | receivables | sales credit, portal | customer, balance, overdue, oldest item days, dunning level |
| passage.receivables.return-order.dispositioned.v1 | receivables | fulfillment, billing, inventory | RMA line, disposition, qty, restocking |

**Phase-level risks**

| Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|
| AX "find next" vs. SAP access-sequence semantics confuse migrated users | High | Medium | Two reference procedures shipped; both in the 500-scenario fixtures | Product |
| ATP over-promises because a supply source is missed | Medium | High | Projection rebuilt nightly from events and diffed; property test gates release | Engineering |
| Delivered-not-invoiced drifts from GL 1405 | Medium | High | Packing slip and invoice share one cost_snapshot_id; daily check from wave 28 day 1 | Engineering |
| ASC 606 allocation disputed by auditors | Medium | High | 40 fixtures reviewed by a practicing accountant; residual-cent rule documented | Product + accountant |
| False auto-match in cash application | Low | High | Single candidate ≥ 85 with 20-point margin; every auto-match reversible | Engineering |
| Go-live simulation exposes Phase 3/4 defects late | High | Medium | Dry-run at wave 28 exit with sell/collect stubs | Both owners |

## 5.26 Wave 26: Pricing, Discounts & Trade Agreements

### 5.26.1 Scope and business context
Every order, quotation, invoice and rebate in Phase 5 depends on a price that is explainable and repeatable. Wave 26 ships the `pricing` service: condition records with validity and quantity breaks, access sequences resolving from the most specific key to the most general, line/multiline/total discounts, surcharges, exclusion groups, rounding rules, campaigns, rebate agreements with accrual, atomic price journals, a simulation/explain API and commission hooks. At wave end a pricing administrator maintains agreements in the Pricing workspace and any caller receives, for a customer/product/date/quantity/currency tuple, a net price plus the ordered list of conditions that produced it. Out of scope: rebate settlement (wave 30), vendor pricing (wave 17), configurator price components beyond attribute-keyed records, storefront promotions. Added from the coverage registers (§5.26.21): vendor rebate, broker and royalty agreements with accrual (claims paid through wave 19 AP), commission transactions and posting, trade promotion funds and promotion planning, commodity pricing, free goods and retroactive accrual.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Nobody can say why a line priced at 93.40: AX "Find next" results are invisible on SalesLine; SAP needs the VA01 condition "Analysis" screen per line | Resolution is a side effect inside the order form (PriceDisc class / RV61A formulas), not a queryable function | `POST /pricing/resolve?explain=true` returns the access-by-access trace, stored with the line as `price_explain_ref` | "Why this price": 20–40 min consultant trace → < 10 s self-service; 100% of lines traced |
| Mass price change is a multi-hour, non-atomic job: PriceDiscAdmTrans posts row by row and leaves half-applied prices on failure; VK12 mass change has no undo | Row loop in X++/ABAP without a transaction boundary across the set | price_journal validates fully, posts in one transaction and reverses with one action | 100k prices: 3–6 h plus cleanup → < 120 s; rollback 1 action |
| Overlapping agreements silently win by insertion order (first PriceDiscTable row found; A-table collisions blocked only per access) | No overlap validation at save | Activation rejects overlapping validity for the same key with 409 naming the conflict | Overlap-caused price disputes: ~10 per 1,000 agreements/month → 0 |
| Rounding differs between order, packing slip and invoice (SalesLine.LineAmount re-rounded per form letter; KPEIN pricing-unit rounding) | Each consuming document re-rounds | Engine rounds once (BR-26.3); consumers reuse the stored net; invoice totals property-tested | Cent-level order/invoice differences: ~0.5% → 0 |
| CPQ tools and storefronts re-implement price lists in middleware because pricing is unreachable outside the form call stack | No stateless pricing API | Resolve API p95 < 20 ms, condition-record events, MCP exposure via wave 46 | External lookups: nightly export → live; zero duplicated price lists |

### 5.26.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Sales price agreements | PriceDiscTable (Relation=PriceSales), PriceDiscAdmTable/Trans journal | VK11/VK12/VK13, KONH/KONP, A004/A005/A305, PR00 | condition_record type PRICE | full |
| Line discount | PriceDiscTable (LineDiscSales), PriceDiscGroup | K004/K005/K007, KONP-KBETR | condition_record LINE_DISC | full |
| Multiline discount | PriceDiscTable (MultiLineDiscSales) | Group condition T685A-KZBZG, K020 | MULTILINE_DISC with group key | full |
| Total discount | PriceDiscTable (EndDiscSales) | Header conditions HI01/RA01 | TOTAL_DISC | full |
| Price/discount groups | PriceDiscGroup; CustTable.PriceGroup/LineDisc/MultiLineDisc/EndDisc; InventTableModule.PriceGroup | KNVV-KONDA/KDGRP, MVKE-KONDM | customer_pricing_group, product_pricing_group | full |
| Access order / exclusion | PriceDiscParameters, "Find next", "Search again" | V/07 access sequences (T682), V/08 procedure (T683S), exclusion T684 | pricing_procedure, access_sequence, exclusion_group | improved (explain) |
| Smart rounding | PriceDiscRounding, journal rounding | DIFF condition, T001R | rounding_rule | full |
| Campaigns / promotions | Trade allowance agreements (TAMAgreement) | Sales deals VB21/VB22, promotions VB31 (KONA) | campaign | full |
| Rebates | PriceDiscRebate / TAMRebateAgreement, D365 Rebate management | VBO1/VBO2 (KONA, BO01–BO06, VBOX); S/4 condition contract WCOCO | rebate_agreement, rebate_accrual | partial (settlement w30) |
| Commissions | CommissionCalc, CommissionSalesRep, CommissionTrans | VBPA partner function; ICM | commission_rule + event hook | partial |
| Mass update | Price/discount agreement journals | VK12 mass change, MASS | price_journal (atomic, reversible) | improved |
| Vendor rebates | PdsRebate* (PI vendor rebate agreements, rebate groups, program types), D365 Rebate management vendor deals, claim journals | Supplier condition contracts (WCOCO purchasing side), settlement documents | rebate_agreement with party_role VENDOR, vendor_rebate_claim → w19 AP | full (claim payment w19) |
| Broker contracts and royalties | PdsBrokerContractTable, PdsRoyaltyAgreement, broker/royalty claim journals | Settlement management royalty and broker condition contracts | broker_contract, royalty_agreement, claim journal → w19 AP | full |
| Commission posting | CommissionCalc, CommissionSalesGroup, CommissionCustomerGroup, CommissionItemGroup, CommissionPosting, CommissionTrans | VBPA partner functions VE/PE; settlement management commission contracts; ICM | commission_rule, sales_group, commission_transaction (posted) | full (payout w19/w43) |
| Trade promotion funds and planning | TAMFund, TAMFundUsage, TAMMerchandisingEvent, TAMPromotionPlanning | CRM TPM; condition contracts with budget | fund, promotion_plan (baseline, lift, actuals) | partial (lift analysis basic) |
| Free goods | Supplementary items (PriceDiscSuppItem, order-side w27) | VBN1/VBN2/VBN3 NA00, OVKF, KOTN | condition type FREE_GOODS | full |
| Commodity pricing (PI) | PdsCommodityCostBasis, PdsPricingCalc, PdsPriceMarginTemplate, price calculation run | Condition contracts with index-based conditions | commodity_index, price_calculation → price_journal | full |
| Retroactive agreements | Rebate management recalculation | VBOF, delta accrual / delta settlement | retroactive recompute with delta posting | full |

### 5.26.3 Functional requirements
- **FR-26.1 Condition types and procedure** — Must. The system shall define condition types (PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC, SURCHARGE, FREIGHT, REBATE_ACCRUAL, ROUNDING) ordered in a versioned pricing procedure per legal entity and sales channel. Notes: reference procedures `AX_STANDARD` and `SAP_RVAA01`; steps 10–990 with optional CEL requirement (≤ 512 chars); activated versions are immutable.
- **FR-26.2 Condition records** — Must. The system shall store records keyed by customer | customer group | all × variant | product | product group | all, with optional site/warehouse/channel/campaign, validity, currency, unit, ≤ 20 strictly increasing quantity breaks, amount and/or two percentages. Notes: overlapping ACTIVE records for the same key and currency are rejected with 409.
- **FR-26.3 Access sequence** — Must. The system shall resolve each condition type by trying accesses in configured order (BR-26.1) and stop at the first access yielding a candidate unless `search_next` is set, in which case it continues and keeps the lower net.
- **FR-26.4 Deterministic resolution with explain** — Must. The system shall return identical outputs for identical (procedure version, records as of pricing_date, request) and an explain trace (≤ 64 KB, content-addressed) listing every access tried, candidates, winner and reason. No wall-clock reads.
- **FR-26.5 Line, multiline and total discounts** — Must. The system shall apply line discounts per line, multiline discounts over summed quantity of lines sharing a group, and total discounts on order net after line discounts; two percentages apply sequentially (1−p1)(1−p2); amount before percentage.
- **FR-26.6 Surcharges** — Should. Fixed or percentage surcharges as separate conditions with their own revenue account; small-order threshold default 250 USD / 200 EUR.
- **FR-26.7 Rounding** — Must. Unit prices at NUMERIC(19,6); line amounts half-even to ISO 4217 minor units (wave 12); smart rounding bands for journals (0–10 → .99, 10–100 → .95, ≥ 100 → whole).
- **FR-26.8 Exclusion groups** — Must. BEST (lowest net) or FIRST (lowest step) among members; an `exclusive` winner suppresses later discount steps.
- **FR-26.9 Price journals** — Must. Mass create/update/delete through a journal that validates fully, posts atomically (100k lines < 120 s) and reverses by one action into a new journal referencing the original.
- **FR-26.10 Campaigns** — Should. Records grouped under a campaign with validity, target groups, budget and kill switch.
- **FR-26.11 Rebate agreements and accrual** — Must. Customer rebate agreements (tiered % or amount per unit; monthly/quarterly/annual; basis net invoiced or quantity) accrue on each posted invoice as Dr 4150 / Cr 2150 via the journal API; retroactive tier crossing recomputes the period and posts the delta.
- **FR-26.12 Commission hooks** — Could. Rules by sales rep × customer group × product group publish `commission.calculated` for Phase 8.
- **FR-26.13 Simulation** — Should. Simulate any tuple without a document and compare two procedure versions.
- **FR-26.14 Performance** — Must. p95 < 20 ms, p99 < 60 ms for a 10-line resolve; in-memory record cache per entity invalidated by `condition-record.activated`.
- **FR-26.15 Vendor rebate agreements** — Should. The system shall support `rebate_agreement.party_role = VENDOR`: tiered percentage or amount per unit on vendor-invoiced net value or received quantity per period, accruing Dr 2160 Vendor rebate receivable / Cr 5100 Purchase rebate income on each `vendor-invoice.posted` (w19). Notes: rebate groups (vendor) and program types BILL_BACK, FREIGHT, MARKETING; lifecycle Cumulate → Approve → Process claim as in AX PdsRebate; a claim per (agreement, period) settles by a w19 AP credit note or deduction from the next vendor payment, journal sequence `RC-`; Σ accruals − Σ claims = GL 2160 daily.
- **FR-26.16 Broker contracts** — Could. The system shall hold broker contracts (broker vendor, customer scope, product scope, rate as % of net or amount per unit, validity) and on `invoice.posted` accrue a broker claim Dr 6210 Broker commission / Cr 2175 Broker payable; claims are processed per period to w19 AP. Notes: credit notes reverse proportionally; claim statement document `BC-`.
- **FR-26.17 Royalty agreements** — Could. The system shall hold royalty agreements (licensor vendor, product or product group, basis net sales or quantity, tiered rates, minimum royalty per period) accruing Dr 6220 Royalty expense / Cr 2176 Royalty payable on `invoice.posted`, with a period claim to w19 AP. Notes: minimum royalty shortfall posts at period close; D365 price-based deals map to the same entity.
- **FR-26.18 Commission transactions and posting** — Should. The system shall define sales groups (reps with share percentages), commission customer groups, commission item groups and commission rules (basis REVENUE or MARGIN, rate) resolved specific → general, and on `invoice.posted` create `commission_transaction` rows posted Dr 6200 Commission expense / Cr 2170 Commission payable per rep. Notes: sales group and rep assignment on the order header (AX SalesTable.CommissionGroup; SAP VBPA VE/PE); credit notes create negative transactions referencing the original; a settlement export per rep and period feeds payroll (w43) or AP (w19); AX Commission report and SAP commission contract settlement list are reproduced by the Commission inquiry (§5.26.14).
- **FR-26.19 Trade promotion funds** — Could. The system shall define funds (type, budget per period, currency, customer scope, owner) whose usage is the sum of campaign accruals, rebate accruals, claims and deductions (w30) attributed to the fund; usage is visible per period and a fund close freezes it. Notes: overspend blocked unless `allow_overspend`; fund budget recalculation nightly.
- **FR-26.20 Promotion planning** — Could. The system shall plan a promotion (merchandising event category and type, campaign, fund, customers, products, dates) with a baseline (average weekly units over the prior 13 weeks), forecast lift % and planned spend; actuals are updated nightly from posted invoices and effectiveness = (actual − baseline) / baseline per product.
- **FR-26.21 Commodity pricing** — Could. The system shall store commodity cost-basis types with dated index values, pricing calculations (index × conversion factor + margin from a price margin template) and run a price calculation that produces a `price_journal` updating the affected trade agreements. Notes: run is previewable; potency pricing (w24) reuses the calculation; index values are versioned.
- **FR-26.22 Free goods** — Should. The system shall support condition type FREE_GOODS with inclusive (1 free within every 10 ordered) and exclusive (1 extra per 10 ordered) variants, calculation rule PROPORTIONAL, UNIT_REFERENCE or WHOLE_UNITS and a minimum quantity; the resolve response returns a zero-priced free-goods line proposal that w27 adds as a supplementary line (FR-27.14).
- **FR-26.23 Retroactive rebate accrual and delta settlement** — Must. The system shall, when an agreement is activated or changed with `valid_from` earlier than today, recompute accruals for invoices already posted in the scope (SAP VBOF) and post the delta per period with `retroactive=true`; settlement after a retroactive change posts only the delta against prior settlements.

### 5.26.4 Data model

**condition_record**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, tenant_id, legal_entity_id | uuid | Y | PK (v7); RLS |
| condition_type, key_combination | enum, smallint | Y | FK access_sequence_step |
| customer_id / customer_group_id | uuid | N | exactly one or null (all) |
| product_id / product_group_id / variant_dims | uuid / jsonb | N | same rule; dims {color,size,config} |
| site_id, warehouse_id, sales_channel, campaign_id | uuid/text | N | optional dims |
| currency, unit | char(3), text | Y | ISO 4217; FK uom (w6) |
| from_quantity, amount | numeric(19,6) | Y/N | default 0 |
| disc_pct_1, disc_pct_2 | numeric(7,4) | N | 0–100 |
| valid_from, valid_to | date | Y | inclusive |
| search_next, exclusive | bool | Y | default false |
| status, version, journal_id | enum, int, uuid | Y | DRAFT/ACTIVE/EXPIRED/WITHDRAWN |

**pricing_procedure**: id, legal_entity_id, name, version, status, steps[] (step_no, condition_type, requirement_cel, exclusion_group_id, statistical, account_key). **access_sequence_step**: condition_type, seq_no, key_combination, required_fields[].

**rebate_agreement**: id, customer_id | customer_group_id, product scope, basis (NET_AMOUNT | QUANTITY), period (MONTH | QUARTER | YEAR), tiers[] (from_value, rate_pct | amount_per_unit), currency, accrual_account 2150, expense_account 4150, valid_from/to, status, settlement_method (CREDIT_NOTE | PAYMENT). **rebate_accrual**: id, agreement_id, period_id, invoice_line_id, basis_value, accrued_amount, journal_posting_id, status (ACCRUED | ADJUSTED | SETTLED | REVERSED).

**price_journal**: id, name, lines[] (op, before/after snapshot), status (OPEN | VALIDATED | POSTED | REVERSED), posted_at, reversed_by_journal_id.

Invariants: no two ACTIVE records share (type, keys, currency, unit, from_quantity) with overlapping validity; every ACTIVE record has a journal_id; resolve is reproducible from (procedure version, pricing_date, request); Σ rebate_accrual per period = GL 2150 movement. Indexing: composite btree (tenant, entity, type, key_combination, customer_id, product_id, currency, valid_from) plus partial indexes per key combination; partition by legal_entity; cache snapshot per (entity, pricing_date), 60 s TTL plus event invalidation.

### 5.26.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/pricing/resolve | Resolve header + ≤ 500 lines; `?explain=true` | stateless | PricingResolve |
| POST | /api/v1/pricing/condition-records, …/{id}:activate, :withdraw | Record lifecycle with overlap check | Idempotency-Key | PricingMaintain |
| GET | /api/v1/pricing/condition-records?customer=&product=&asOf= | Query | — | PricingView |
| POST | /api/v1/pricing/journals, …/{id}:post, :reverse | Mass update | Idempotency-Key | PricingJournalPost |
| POST | /api/v1/pricing/procedures, …/versions/{v}:activate | Procedure maintenance | Idempotency-Key | PricingAdmin |
| POST/GET | /api/v1/pricing/rebates/agreements; /rebates/accruals?period= | Rebates | Idempotency-Key | RebateMaintain |
| POST | /api/v1/pricing/simulate | Simulation, version comparison | stateless | PricingView |

Validation: resolve requires legal_entity_id, customer_id, currency, pricing_date, lines[].product_id/quantity/unit; quantity ≤ 0 or unknown unit → 422. Journal post requires status VALIDATED with zero errors.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| passage.pricing.condition-record.activated.v1 | activate / journal post | id, type, keys, currency, validity, version, journal_id, retroactive | sales cache, analytics |
| passage.pricing.condition-record.withdrawn.v1 | withdraw / reverse | id, version, reason | sales cache |
| dev.passage.pricing.journal.posted.v1 | post / reverse | journal_id, line_count, reversed_journal_id | audit |
| passage.pricing.rebate-accrual.posted.v1 | invoice.posted consumed | agreement_id, period_id, invoice_line_id, amount, currency, journal_posting_id | ledger, receivables |
| dev.passage.pricing.commission.calculated.v1 | invoice.posted consumed | invoice_line_id, sales_rep_id, basis, rate, amount | Phase 8 |

### 5.26.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Pricing workspace | tiles: expiring in 30 d, draft journals, campaigns, accrual this period | counts | PricingView |
| Trade agreements grid | create, copy, activate, withdraw, export | type, customer/group, product/group, currency, from qty, amount, %, validity, status | PricingMaintain |
| Price journal | import CSV (≤ 100k rows), validate, post, reverse | op, before/after, errors | PricingJournalPost |
| Procedure designer | add step, requirement, exclusion, activate version | step, type, requirement, exclusive | PricingAdmin |
| Simulation / explain | enter tuple, run, compare versions | per step: access, candidate, winner, amount | PricingView |
| Rebate agreements | tiers, accruals, close period | customer, basis, tiers, accrued | RebateMaintain |

Interaction rules: keyboard row navigation and Ctrl+D duplicate; bulk edit validity and percentage on multi-select; validation on blur and at activate; explain tree navigable by arrow keys; column layouts personalizable (wave 5 shell).

### 5.26.7 Business rules and edge cases
- **BR-26.1 Resolution algorithm.** For each procedure step in order: (1) evaluate requirement; (2) for the step's condition type iterate accesses by seq_no — PRICE default: 1 customer × variant, 2 customer × product, 3 customer × product group, 4 customer group × product, 5 customer group × product group, 6 customer × all products, 7 all customers × product, 8 all customers × product group, 9 all × all; (3) at each access select ACTIVE records matching keys, currency = document currency (no cross-currency fallback), unit = line unit or convertible via wave 6, valid_from ≤ pricing_date ≤ valid_to, from_quantity ≤ quantity; (4) pick greatest from_quantity, then most dims matched, then lowest net unit price; (5) stop unless winner has search_next, then continue and keep the minimum; (6) no candidate at any access → nothing for discounts, `PRICE_NOT_FOUND` for PRICE unless a manual price exists; (7) exclusion groups evaluated after member steps (BEST lowest net, FIRST lowest step); (8) `exclusive` winner suppresses later discount steps; (9) round per BR-26.3.
- **BR-26.2 Pricing date** = requested ship date by default (AX), configurable to order or delivery date (SAP KOMK-PRSDT).
- **BR-26.3 Rounding.** Net unit = amount × (1−p1/100) × (1−p2/100) at 6 decimals; line net = round_half_even(qty × net unit, minor units); totals are sums of rounded lines; a percentage total discount rounds once on the total.
- **BR-26.4 Multiline grouping** sums quantities of lines with the same group and unit on one document; adding a line re-resolves the group.
- **BR-26.5 Manual override** requires duty PricingOverride, keeps the trace with a reason code, and is excluded from rebate basis when the agreement says `exclude_manual`.
- **BR-26.6 Concurrency.** Activation locks the key hash (`SELECT … FOR UPDATE`); overlap → 409 with the conflicting id.
- **BR-26.7 Retroactive journals** never re-price posted invoices; the event carries `retroactive=true`.
- **BR-26.8 Rebate tiers** evaluate on cumulative period basis; crossing a tier recomputes the whole period and posts the delta on the next invoice or at period end.
- **BR-26.9 State machines.** Record: DRAFT → ACTIVE → EXPIRED (nightly) | WITHDRAWN. Journal: OPEN → VALIDATED → POSTED → REVERSED. No return to DRAFT.
- **BR-26.10 Negative prices** rejected; 100% discount allowed and yields 0.00 with trace.
- **BR-26.11 Vendor rebate basis** = vendor invoice net (default) or received quantity; tier crossing recomputes the period as in BR-26.8; claim amount = Σ accruals − Σ prior claims for the period; a negative claim (over-accrual) creates a w19 vendor invoice instead of a credit note.
- **BR-26.12 Commission resolution** tries (sales group, customer commission group, item commission group) → (sales group, customer group, all) → (sales group, all, item group) → (sales group, all, all); basis REVENUE = line net, MARGIN = line net − cost_snapshot (w20); amount split by rep share %, rounded half-even per rep, residual cent to the first rep; a credit note creates negative transactions referencing the original.
- **BR-26.13 Free goods** inclusive reduces the priced quantity (10 ordered → 9 priced + 1 free); exclusive adds a free line; WHOLE_UNITS truncates; free goods never combine with a 100% discount on the same line and are excluded from rebate and commission basis unless the agreement sets `include_free_goods`.
- **BR-26.14 Retroactive recompute** replays `invoice.posted` for the agreement scope from `valid_from`; delta = recomputed − posted per period, posted as one journal per period; periods closed in w14 post to the first open period with the original period in the reference.
- **BR-26.15 Fund usage** = Σ attributed accruals + claims + deductions; it never exceeds budget unless `allow_overspend`; fund close freezes usage and blocks new attribution with `FUND_CLOSED`.

### 5.26.8 Acceptance criteria
- **AC-26.1** (FR-26.3) Given C1 in group PG-A with access-2 record 95.00 USD and access-4 record 90.00, when resolved without search_next, then 95.00 and access 4 is shown as not evaluated.
- **AC-26.2** (FR-26.3) Given search_next on the access-2 record, then 90.00 and both accesses appear in the trace.
- **AC-26.3** (FR-26.2) Given breaks 0→10.00, 100→9.50, 500→9.00 EUR, when qty = 500 then 9.00; when 499 then 9.50.
- **AC-26.4** (FR-26.4) Given 10,000 random requests resolved twice on two replicas, then outputs are byte-identical.
- **AC-26.5** (FR-26.5) Given discounts 10%/5% on 100.00 USD × 3, then line net = 256.50.
- **AC-26.6** (FR-26.8) Given exclusion group BEST with LINE_DISC 8% and campaign 12%, then only 12% applies.
- **AC-26.7** (FR-26.9) Given a 100k-line journal with one invalid line, then nothing is written and the error names the line; after correction and post, reversal restores all prior versions.
- **AC-26.8** (FR-26.11) Given a 2% monthly rebate and a 10,000.00 USD invoice, then 200.00 posts Dr 4150 / Cr 2150.
- **AC-26.9** (FR-26.14) Given 50 concurrent callers over the 500 scenarios, then p95 < 20 ms.
- **AC-26.10** (FR-26.7) Given smart rounding ".99 below 10" and a journal raising 7.41 by 5%, then 7.99 is stored.
- **AC-26.11** (FR-26.15) Given a 3% vendor rebate agreement and a 20,000.00 USD vendor invoice posted, then 600.00 posts Dr 2160 / Cr 5100 and the period claim preview shows 600.00.
- **AC-26.12** (FR-26.18) Given reps R1 60% / R2 40% in sales group SG-E and a 5% revenue rule, when a 10,000.00 USD invoice posts, then commission transactions 300.00 and 200.00 post Dr 6200 / Cr 2170 and the credit note of that invoice posts −300.00 / −200.00.
- **AC-26.13** (FR-26.22) Given inclusive free goods 1 per 10 with WHOLE_UNITS and an ordered quantity of 25, then 23 units are priced and 2 are free.
- **AC-26.14** (FR-26.23) Given a 2% agreement activated 2027-03-20 with valid_from 2027-03-01 and 50,000.00 USD invoiced in between, then a 1,000.00 delta posts Dr 4150 / Cr 2150 with retroactive=true.

### 5.26.9 Test plan and fixtures
Fixtures: `pricing-500` (hand-built from AX trade-agreement and SAP RVAA01 examples, with expected price and trace); `pricing-random-10k` (seeded); `price-journal-100k`; `rebate-tiers-12` (accountant-reviewed).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Resolver scenarios | unit | resolve | 500/500 price and trace |
| Determinism | property | resolve | identical across 10k runs and replicas |
| Overlap rejection | unit | activate | 100% rejected with 409 |
| Journal atomicity | integration | journals | failure leaves zero rows changed |
| Rebate accrual = GL | reconciliation | accrual vs ledger | Σ per period = GL 2150 to the cent |
| Latency | performance | 10-line resolve, 50 callers | p95 < 20 ms, p99 < 60 ms |
| Sales ↔ pricing | contract (Pact) | resolve | green |

Reconciliation: Σ rebate_accrual per (agreement, period) = journal postings tagged `source=rebate_accrual`; journal snapshots replay to current record versions.

### 5.26.10 Deployment and operations
Helm `passage-pricing` (Go, 3 replicas, HPA CPU 70%); CloudNativePG `pricing-db` (3 instances); Atlas migrations from metadata, additive-only. Config: `PRICING_CACHE_TTL=60s`, `PRICING_MAX_LINES=500`, `PRICING_EXPLAIN_MAX_BYTES=65536`. Flags: `pricing.search_next`, `pricing.sap_procedure`, `pricing.commission_hooks`. Temporal jobs: nightly expiry 00:10 entity-local; rebate recompute at period end. SLOs: 99.95% availability, p95 20 ms. Alerts: invalidation lag > 5 s, error rate > 0.5%, journal post > 300 s. Runbook: cache rebuild, journal reversal, procedure version rollback. Rollback: previous image digest via Argo CD.

### 5.26.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| PriceDiscTable (Relation, AccountCode/Relation, ItemCode/Relation, QuantityAmount, Amount, Percent1/2, Currency, FromDate/ToDate, SearchAgain) | condition_record | Table/GroupId/All → customer/group/all; Relation → type; SearchAgain → search_next |
| PriceDiscGroup; CustTable price/discount groups | customer_pricing_group, product_pricing_group | one group per AX type |
| KONH/KONP + A004/A005/A305 (KSCHL, DATAB/DATBI, KBETR, KONWA, KPEIN, KMEIN), KONM scales | condition_record | A-table → key_combination; KONM → breaks; KPEIN normalized to per-unit at 6 decimals |
| T683S / T682 | pricing_procedure, access_sequence_step | map to nearest reference procedure; manual review |
| KONA (BO01–BO06), VBOX; TAMRebateAgreement | rebate_agreement, opening accrual to 2150 | |

Validation: 1,000 sampled customer/product pairs resolve to the legacy price (AX PriceDisc::findPrice export, SAP VK13 export) to the cent. Not migrated: records expired > 24 months (archived), commission history, pricing logs, posted PriceDiscAdmTrans (lakehouse).

### 5.26.12 Dependencies, risks, open questions
- Depends on: w6 units, w7 customers/groups, w8 products/variants/attributes, w11 journal, w12 currencies, w25 (phase order).
- Risks: KPEIN/per-unit mismatch on import → normalize at 6 decimals; cache stampede after a 100k journal → warm cache inside the journal transaction.
- Open questions: (1) Cross-currency fallback? Default no; decide by wave 27. (2) Margin-based commissions before Phase 8? Default no; decide by wave 29.

### 5.26.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| price_search_mode | legal entity | enum | FIRST_MATCH | FIRST_MATCH, SEARCH_NEXT | Default for `search_next` on new records (BR-26.1 step 5) | AR parameters → Prices → "Find next" / PriceDiscParameters | V/07 access sequence "Exclusive" flag |
| pricing_date_basis | legal entity | enum | SHIP_DATE | SHIP_DATE, ORDER_DATE, DELIVERY_DATE | Pricing date per BR-26.2 | Fixed to requested ship date | VOV8 pricing date default (KOMK-PRSDT) |
| total_discount_basis | legal entity | enum | AFTER_LINE_DISC | AFTER_LINE_DISC, GROSS | Base for TOTAL_DISC | AR parameters → Prices → "Total discount calculation" | RVAA01 header condition HI01/RA01 step base |
| smart_rounding_bands | legal entity | table | 0–10 → .99; 10–100 → .95; ≥ 100 → whole | ordered bands | FR-26.7 journal rounding | PriceDiscRounding / AR parameters → Prices → Smart rounding | DIFF condition, T001R rounding rules |
| reference_procedure | legal entity × sales channel | enum | AX_STANDARD | AX_STANDARD, SAP_RVAA01, custom | Procedure determination (FR-26.1) | n/a (PriceDisc class order fixed) | OVKK procedure determination (T683V) |
| cross_currency_fallback | tenant | bool | false | true/false | Allow records in another currency converted at w12 rate (open question 1) | Trade agreement "Currency" strict | KONP-KONWA conversion in pricing |
| cache_ttl_seconds | tenant | int | 60 | 10–600 | Record cache snapshot lifetime | n/a | n/a (buffering) |
| max_lines_per_resolve | tenant | int | 500 | 1–2,000 | Request size limit | n/a | n/a |
| explain_max_bytes | tenant | int | 65,536 | 8,192–262,144 | Trace size (FR-26.4) | n/a | Condition analysis screen size |
| small_order_threshold | legal entity | money | 250.00 USD / 200.00 EUR | ≥ 0 | SURCHARGE below threshold (FR-26.6) | Charges codes (MarkupAutoTable) | AMIW / AMIZ minimum value surcharge |
| rebate_basis_default | legal entity | enum | NET_AMOUNT | NET_AMOUNT, QUANTITY | Default on new rebate agreements | Rebate management "Calculation basis" | KONA rebate basis (subtotal 7) |
| rebate_accrual_accounts | legal entity | account pair | 4150 / 2150 | chart (w11) | FR-26.11 postings | Customer rebate posting (TAM/rebate posting profile) | VKOA ERU / ERB account keys |
| rebate_exclude_manual_prices | legal entity | bool | false | true/false | BR-26.5 | n/a | n/a |
| retroactive_mode | legal entity | enum | DELTA | DELTA, FULL_REVERSAL | FR-26.23 posting shape | Rebate recalculation | VBOF update billing documents |
| commission_basis | legal entity | enum | REVENUE | REVENUE, MARGIN | FR-26.18 | Commission calculation → Basis (Revenue/Margin) | Condition contract basis |
| commission_accounts | legal entity | account pair | 6200 / 2170 | chart (w11) | FR-26.18 | Commission posting form | Condition contract account determination |
| commission_settlement_target | legal entity | enum | AP | AP, PAYROLL, NONE | Settlement export destination | Commission posting (vendor/employee) | Settlement to HR/AP |
| vendor_rebate_accounts | legal entity | account pair | 2160 / 5100 | chart (w11) | FR-26.15 | Rebate program types → posting | Supplier condition contract account keys |
| vendor_rebate_claim_mode | legal entity | enum | CREDIT_NOTE | CREDIT_NOTE, PAYMENT_DEDUCTION | FR-26.15 settlement | Rebate claim journal names | Settlement document type |
| free_goods_calc_rule | legal entity | enum | WHOLE_UNITS | PROPORTIONAL, UNIT_REFERENCE, WHOLE_UNITS | BR-26.13 | n/a | OVKF calculation rule 1/2/3 (NA00) |
| fund_allow_overspend | tenant | bool | false | true/false | BR-26.15 | TAM fund "Allow overspend" | n/a |
| commodity_index_rounding | legal entity | int | 6 | 2–6 | Decimals for index × factor | PdsPricingCalc rounding | n/a |
| campaign_kill_switch_effect | tenant | enum | NEXT_RESOLVE | NEXT_RESOLVE, IMMEDIATE_CACHE_FLUSH | FR-26.10 | n/a | VB22 deactivate |

### 5.26.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Trade agreements | list inquiry | type, customer/group, product/group, currency, validity as-of, status | key fields, from qty, amount, %, validity, version, journal | condition_record | Trade agreements (PriceDiscTable list) | VK13 / VK33 condition display |
| Price list by customer | operational report | customer, product group, as-of date, currency | product, unit, net price per break | resolve (batch) | SSRS Price list (customer) / by item | V/LD price list report |
| Price explain | drill-down | document line or tuple | step, access, candidates, winner, reason | explain trace store | n/a (PriceDisc::findPrice has none) | VA01 item condition "Analysis" |
| Price journal log | list inquiry | journal, date, user, status | op, before, after, reversed_by | price_journal lines | PriceDiscAdmTrans posted journals | VK12 change documents (CDHDR) |
| Rebate accruals | drill-down | agreement, customer, period, status | basis, accrued, adjusted, settled, journal ref | rebate_accrual | Rebate management → Transactions | VBO3 + VBOX rebate index |
| Commission transactions | list inquiry | rep, sales group, period, customer, invoice | basis, rate, amount, status, journal ref | commission_transaction | Commission transactions (CommissionTrans) / SSRS Commission report | Settlement management document list |
| Vendor rebate claims | list inquiry | vendor, agreement, period, status | accrued, claimed, approved, settled | vendor_rebate_claim | Vendor rebate inquiries / SSRS Vendor rebate claim | Supplier condition contract settlement list |
| Broker and royalty claims | list inquiry | contract, vendor, period | basis, rate, accrued, claimed | broker/royalty claims | Broker/royalty claim inquiries | Settlement documents (royalty) |
| Fund usage | drill-down | fund, period, customer | budget, usage by source, remaining | fund_usage | Fund usage inquiry / SSRS Fund statement | TPM fund report |
| Promotion effectiveness | KPI | promotion, product, period | baseline, actual, lift %, spend, ROI | promotion_plan actuals | Promotion effectiveness analysis | n/a |
| Commodity prices | list inquiry | cost basis type, date range | index value, source, version, affected agreements | commodity_index | Commodity price inquiries | n/a |
| Expiring agreements | KPI | days ahead (30) | count, value of affected open lines | condition_record | Pricing workspace tile | VK13 validity filter |

### 5.26.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Customer price list | on demand / price list job | header: customer, currency, as-of date; lines: product, unit, break qty, net price; footer: validity note | PDF, CSV, email | none (report) | none; keep 24 months for dispute support |
| Rebate agreement statement | period close, on demand | header: agreement, customer, period; lines: invoice, basis, tier, accrued; footer: Σ accrued, Σ settled, open | PDF, email, portal (w30) | none | retain 7 years with accounting records |
| Commission statement | period close per rep | header: rep, sales group, period; lines: invoice, customer, basis, rate, amount; footer: total, settlement reference | PDF, email, CSV export to payroll/AP | none | 7 years (payroll support) |
| Vendor rebate claim | claim generation | header: vendor, agreement, period, claim number; lines: accruals; footer: claim amount, settlement mode | PDF, email, EDI hook (w35) | `RC-` per legal entity | 7 years; forms the basis of the AP credit note |
| Broker / royalty claim statement | claim generation | header: contract, vendor, period; lines: invoice, basis, rate, amount | PDF, email | `BC-` / `RY-` | 7 years |
| Price journal audit extract | post / reverse | journal header, lines before/after | CSV, JSON | none | audit log retention 10 years (w48) |

### 5.26.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Record expiry | 00:10 entity-local daily | legal_entity | status transition by (id, version) | < 60 s per 1M records | retry 3×; skipped entity alerted | expiry lag > 1 day |
| Rebate period recompute | period end + 1 day 02:00; on tier crossing | agreement | journal Idempotency-Key = (agreement, period, run_seq) | < 10 min per 10k agreements | compensate partial journals; rerun | run > 30 min; Σ accrual ≠ GL 2150 |
| Retroactive accrual recompute | event-driven on activation; nightly catch-up 02:30 | agreement | checkpoint per invoice id processed | < 5 min per 100k invoices | resume from checkpoint | pending retro > 24 h |
| Commission calculation | on `invoice.posted`; nightly reconcile 03:00 | legal_entity | key = invoice_line_id | < 50 ms per invoice | DLQ with replay | missing commission for posted invoice > 1 h |
| Vendor rebate cumulate / claim generation | nightly 03:30; claims at period end | agreement | key = (agreement, period) | < 5 min | rerun idempotent | claims not generated 2 days after period end |
| Promotion actuals update | nightly 04:00 | promotion | watermark on invoice posting time | < 10 min | resume from watermark | lag > 1 day |
| Commodity price calculation run | per calculation schedule (weekly default) | cost basis type | run id; journal post idempotent | < 2 min per 10k agreements | journal stays VALIDATED for review | index older than schedule |
| Fund budget recalculation | nightly 04:30; fund close manual | fund | key = (fund, period) | < 1 min | rerun | usage > budget when overspend disabled |
| Cache warm after journal post | inside journal transaction | legal_entity | n/a | < 30 s | journal still POSTED; cache rebuilt lazily | invalidation lag > 5 s |

### 5.26.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| PricingView | GET condition-records, simulate, explain; Pricing workspace, Simulation screen | none | Sales rep, Sales manager, Pricing analyst, Auditor |
| PricingMaintain | POST condition-records, :activate, :withdraw; Trade agreements grid; campaigns | with PricingJournalPost on the same journal (maker/checker when `journal_four_eyes`) | Pricing analyst |
| PricingJournalPost | POST journals, :post, :reverse; Price journal screen | with PricingMaintain under four-eyes; with CommodityPriceRun | Pricing manager |
| PricingAdmin | POST procedures, versions:activate; Procedure designer | with PricingJournalPost | Pricing manager, Application admin |
| PricingOverride | manual price on order lines (w27) with reason | with CreditRelease (w27) | Sales manager |
| RebateMaintain | POST rebates/agreements; Rebate agreements screen; accrual inquiry | with RebateSettle (w30) | Pricing analyst, AR manager |
| RebateView | GET rebates/accruals, claims | none | Controller, Auditor |
| VendorRebateClaimApprove | approve vendor rebate claims; claim job | with APPaymentPost (w19), with RebateMaintain | Purchasing manager |
| CommissionMaintain | commission rules, sales groups, rep assignment | with payroll settlement (w43) and APInvoicePost (w19) | Sales operations |
| CommissionView | commission inquiry, statements | none | Sales rep (own), Sales manager |
| FundMaintain | funds, promotion plans, fund close | with RebateSettle (w30) | Trade marketing manager |
| CommodityPriceRun | commodity indexes, calculation run | with PricingJournalPost | Pricing manager |

### 5.26.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| PRC-0001 | error | Price not found for product {product} on {date} | No PRICE candidate at any access and no manual price | Create an agreement or enter a manual price (PricingOverride) | BR-26.1 step 6 |
| PRC-0002 | error | Overlapping active record {id} for the same key, currency and unit | Validity overlap on activation | Adjust validity or withdraw the conflicting record | FR-26.2, BR-26.6 |
| PRC-0003 | error | Negative net price is not allowed | Amount or discounts produce < 0 | Correct the amount or percentages | BR-26.10 |
| PRC-0004 | error | Quantity breaks must be strictly increasing | Duplicate or descending from_quantity | Reorder breaks | FR-26.2 |
| PRC-0005 | error | Journal has {n} validation errors; nothing was posted | Validation failed on post | Fix listed lines and re-validate | FR-26.9 |
| PRC-0006 | warning | Retroactive activation: {n} posted invoices will be re-accrued | valid_from before today on activation | Confirm or move valid_from | FR-26.23 |
| PRC-0007 | error | Unit {unit} is not convertible to the record unit {unit2} | No w6 conversion factor | Add conversion or record in line unit | BR-26.1 step 3 |
| PRC-0008 | error | Procedure version {v} is active and immutable | Edit on an activated version | Create a new version | FR-26.1 |
| PRC-0009 | error | Request exceeds {max} lines | max_lines_per_resolve | Split the request | FR-26.14 |
| PRC-0010 | info | search_next continued to access {n}; lower net kept | search_next in effect | none | FR-26.3 |
| PRC-0011 | error | Fund {fund} is closed | Attribution after close | Reopen (FundMaintain) or choose another fund | BR-26.15 |
| PRC-0012 | error | Fund {fund} budget exceeded by {amount} | Usage > budget and overspend disabled | Increase budget or enable overspend | BR-26.15 |
| PRC-0013 | error | Commission rule not found for sales group {sg} | No rule at any level | Add a rule or set a default | BR-26.12 |
| PRC-0014 | warning | Free goods cannot combine with a 100% discount on line {n} | Both apply | Remove one | BR-26.13 |
| PRC-0015 | error | Vendor rebate claim for {agreement} {period} already processed | Duplicate claim | View the existing claim | FR-26.15 |
| PRC-0016 | warning | Commodity index for {basis} is older than {days} days | Stale index | Load the index before the run | FR-26.21 |
| PRC-0017 | error | Minimum royalty shortfall {amount} posted at period close | Royalty below minimum | none (informational posting) | FR-26.17 |

### 5.26.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| resolve (10 lines) | 2M per tenant per day; peak 5,000/min | 6 / 20 / 60 ms | 500 req/s per replica | none (stateless; traces 64 KB × sampled 10%) | in-memory snapshot per (entity, date) | HPA to 9 replicas; explain sampling drops to 1%; p95 ≤ 40 ms |
| Condition record activate | 20k per day | 20 / 80 / 200 ms | 100/s | 1M records × 1 KB = 1 GB | composite btree + partial per key combination; partition by entity | queue activations; overlap check still serialized |
| Price journal post (100k lines) | 5 per day | — / 120 s / 180 s | 1k lines/s | 50 GB journals (snapshots) | partition journal lines by journal id | single journal at a time per entity; others wait VALIDATED |
| Rebate accrual on invoice | 50k invoices per day | 10 / 40 / 100 ms | 200/s | 20M accrual rows × 300 B = 6 GB | (agreement, period) btree; partition by period | accrual lag alert at 5 min; invoices unaffected |
| Commission calculation | 50k invoices per day | 10 / 40 / 100 ms | 200/s | 5 GB | (rep, period) | same as accrual |
| Retroactive recompute | 1 per day, 100k invoices | — / 5 min / 10 min | 500 invoices/s | none | checkpointed | throttled to 50% of a replica |
| Commodity calculation run | weekly, 10k agreements | — / 2 min / 5 min | — | 100 MB | n/a | run queued |

### 5.26.20 Localization and compliance hooks

- Currency rounding to ISO 4217 minor units per currency (w12); JPY and HUF zero-decimal handling in BR-26.3; EUR/USD examples in fixtures.
- Country packs (w48) may inject statutory surcharge conditions (for example packaging or environmental fees surfaced from w28 FR-28.14) as SURCHARGE steps through the procedure extension point `procedure.steps[].country_pack_id`; packs never alter resolution order.
- Rebate, commission, broker and royalty accruals are posted through the w11 journal with tax treatment deferred to settlement (w30 / w19); withholding on royalties (for example DE §50a, IN TDS) is applied by the w19 AP claim, not here.
- Price lists and statements render per customer language (w5 docgen locale) with date and number formats from the entity locale; explain traces are English-only engineering artifacts.
- Privacy: commission transactions reference worker ids (w42) and are subject to the w48 personal-data retention policy (7 years then pseudonymized); rep names never appear in events.
- Retention: condition records and journals 10 years (audit), promotion plans 3 years.
- Extension point: condition type registry accepts country-pack types with a CEL requirement; access sequences per pack are additive and versioned with the procedure.

### 5.26.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.16.1 #54 — Vendor rebates (PI vendor rebate agreements, R3) | GAP | ADDED as FR-26.15 | party_role VENDOR on rebate_agreement; accrual 2160/5100 |
| AX §12.16.1 #55 — Broker contracts (PI brokers, R3) | GAP | ADDED as FR-26.16 | broker_contract, claim to w19 AP |
| AX §12.16.1 #56 — Royalty agreements (PI, R3) | GAP | ADDED as FR-26.17 | royalty_agreement with minimum royalty |
| AX §12.16.2 #58 — Vendor rebate inquiries | GAP | ADDED as FR-26.15 | §5.26.14 Vendor rebate claims |
| AX §12.16.2 #59 — Broker/royalty claim inquiries | GAP | ADDED as FR-26.16 / FR-26.17 | §5.26.14 Broker and royalty claims |
| AX §12.16.2 #60 — SSRS Vendor rebate reports | GAP | ADDED as FR-26.15 | §5.26.15 Vendor rebate claim document |
| AX §12.16.3 #61 — Vendor rebates → Cumulate / Approve / Process claims | GAP | ADDED as FR-26.15 | §5.26.16 cumulate and claim jobs; duty VendorRebateClaimApprove |
| AX §12.16.3 #62 — Process broker claims / Process royalty claims | GAP | ADDED as FR-26.16 / FR-26.17 | claim jobs at period end |
| AX §12.16.4 #63 — Vendor rebate setup (agreements, program types, rebate groups, claim journal names) | GAP | ADDED as FR-26.15 | vendor_rebate_accounts, vendor_rebate_claim_mode (§5.26.13) |
| AX §12.16.4 #64 — Broker contract setup / Royalty agreement setup / Claim journal names | GAP | ADDED as FR-26.16 / FR-26.17 | `BC-` / `RY-` sequences |
| AX §12.23.1 #65 — Vendor rebates / Brokers / Royalties (PI links) | GAP | ADDED as FR-26.15–26.17 | same entities |
| AX §12.23.3 #69 — Vendor rebates → Process (PI) | GAP | ADDED as FR-26.15 | claim job |
| AX §12.23.4 #71 — Vendor rebates / Brokers / Royalties setup (PI) | GAP | ADDED as FR-26.15–26.17 | §5.26.13 |
| AX §12.36.1 #102 — Deal → Vendor deals (purchase rebates) | GAP | ADDED as FR-26.15 | D365 vendor deal = VENDOR agreement |
| AX §12.36.1 #103 — Deal → Price-based deals (royalty-type) | GAP | ADDED as FR-26.17 | |
| AX §12.36.2 #104 — Vendor deal inquiries | GAP | ADDED as FR-26.15 | §5.26.14 |
| AX §12.36.2 #105 — SSRS Vendor rebate claim | GAP | ADDED as FR-26.15 | §5.26.15 |
| AX §12.36.3 #106 — Vendor rebate claims processing | GAP | ADDED as FR-26.15 | §5.26.16 |
| AX §12.36.4 #107 — Vendor deal setup (claim via AP) | GAP | ADDED as FR-26.15 | settlement through w19 |
| AX §12.3.1 #20 — Commission transactions (CommissionTrans) | PARTIAL-26 | ADDED as FR-26.18 | commission_transaction posted 6200/2170 |
| AX §12.3.2 #21 — Commission inquiry | PARTIAL-26 | ADDED as FR-26.18 | §5.26.14 Commission transactions |
| AX §12.3.2 #23 — SSRS Commission report / Commission transactions | PARTIAL-26 | ADDED as FR-26.18 | §5.26.15 Commission statement |
| AX §12.3.3 #25 — Commission → Calculate/post commissions (via invoice) | PARTIAL-26 | ADDED as FR-26.18 | event-driven on invoice.posted |
| AX §12.3.4 #29 — Commission calculation / sales groups / customer groups / item groups / posting | PARTIAL-26 | ADDED as FR-26.18 | commission_basis, commission_accounts (§5.26.13) |
| AX §12.3.4 #33 — Sales districts / Sales groups (sales reps) | PARTIAL-26 | ADDED as FR-26.18 | sales_group; sales district stays a w7 customer attribute |
| AX §12.15.1 #142 — Released product → Commodity pricing (PdsCommodity*) | PARTIAL-26 | ADDED as FR-26.21 | |
| AX §12.15.4 #150 — Commodity pricing setup (cost basis types, pricing calculations, price margin templates, commodity codes) | PARTIAL-26 | ADDED as FR-26.21 | commodity codes reuse w8 product attributes |
| AX §12.17.1 #169 — Sales → Commissions (link) | PARTIAL-26 | ADDED as FR-26.18 | |
| AX §12.17.2 #170 — SSRS Commission report (link) | PARTIAL-26 | ADDED as FR-26.18 | |
| AX §12.17.4 #172 — Commissions setup (calculation, sales groups, customer groups, item groups, posting) | PARTIAL-26 | ADDED as FR-26.18 | |
| AX §12.18.1 #174 — Merchandising events (promotion planning) | PARTIAL-26 | ADDED as FR-26.20 | baseline 13 weeks, lift % |
| AX §12.18.1 #175 — Funds (TAMFund) | PARTIAL-26 | ADDED as FR-26.19 | |
| AX §12.18.2 #177 — Fund usage inquiry | PARTIAL-26 | ADDED as FR-26.19 | §5.26.14 Fund usage |
| AX §12.18.2 #179 — Promotion effectiveness analysis (lift) | PARTIAL-26 | ADDED as FR-26.20 | §5.26.14 Promotion effectiveness |
| AX §12.18.2 #180 — SSRS Fund statement / Fund usage | PARTIAL-26 | ADDED as FR-26.19 | Fund usage inquiry export (PDF/CSV) |
| AX §12.18.3 #183 — Funds → Fund budget recalculation / Fund close | PARTIAL-26 | ADDED as FR-26.19 | §5.26.16 job |
| AX §12.18.3 #184 — Promotion → Update promotion planning actuals | PARTIAL-26 | ADDED as FR-26.20 | nightly actuals job |
| AX §12.18.4 #185 — Funds setup (fund types, budget periods) | PARTIAL-26 | ADDED as FR-26.19 | fund_allow_overspend |
| AX §12.18.4 #186 — Merchandising event categories / Event types | PARTIAL-26 | ADDED as FR-26.20 | |
| AX §12.23.1 #199 — Commodity pricing (PI link) | PARTIAL-26 | ADDED as FR-26.21 | |
| AX §12.23.2 #204 — Commodity price inquiries | PARTIAL-26 | ADDED as FR-26.21 | §5.26.14 Commodity prices |
| AX §12.23.3 #207 — Commodity pricing → Price calculation run | PARTIAL-26 | ADDED as FR-26.21 | §5.26.16 job → price_journal |
| AX §12.23.4 #209 — Commodity pricing setup (cost basis types, pricing calculations, price margin templates) | PARTIAL-26 | ADDED as FR-26.21 | |
| SAP §13.19 #217 — VBN1/VBN2/VBN3 free goods condition records | PARTIAL-26 | ADDED as FR-26.22 | inclusive/exclusive |
| SAP §13.19 #227 — OVKF free goods (NA00, NA0001, KOTN) | PARTIAL-26 | ADDED as FR-26.22 | free_goods_calc_rule |
| SAP §13.20 #228 — VBOF update billing documents (retroactive accrual) | PARTIAL-26 | ADDED as FR-26.23 | |
| SAP §13.20 #230 — Commission contracts (settlement management) | PARTIAL-26 | ADDED as FR-26.18 | |
| SAP §13.20 #232 — Royalty / licensing payments (settlement management) | GAP (propose 26) | ADDED as FR-26.17 | |
| SAP §13.20 #233 — VBOF batch retroactive accrual update | PARTIAL-26 | ADDED as FR-26.23 | §5.26.16 retroactive recompute job |
| SAP §13.20 #234 — Delta accruals / delta settlement | PARTIAL-26 | ADDED as FR-26.23 | delta against prior settlements (w30 BR-30.8 reads the delta) |
| SAP §13.20 #236 — Commission settlement integration (HR/AP) | PARTIAL-26 | ADDED as FR-26.18 | commission_settlement_target; payout executes in w19 / w43 |
| SAP §13.20 #229 — Supplier condition contracts (purchasing rebates, chargebacks) | PARTIAL-19 (note proposes w26 engine) | ADDED as FR-26.15 (accrual); chargeback claims DEFERRED to wave 30 (FR-30.22) | AP settlement remains wave 19 |

## 5.27 Wave 27: Sales Orders, Quotations, ATP & Credit

### 5.27.1 Scope and business context
Wave 27 ships the `sales` service: quotations with conversion, sales agreements, order types (journal, order, subscription, returned — completed in wave 30), delivery date control against cumulative ATP/CTP, credit management with limits, aging rules, holds and a Temporal release workflow, versioned change management, line-level status and a multi-channel intake API. A sales rep can quote, convert, promise a date and see holds; a credit manager releases orders. Out of scope: picking and shipping (wave 28), invoicing (wave 29), portal UI (wave 30), EDI intake (wave 35). Added from the coverage registers (§5.27.21): supplementary items, listing/exclusion, product allocation quotas, backorder processing strategies, customer consignment, sales-order special stock, scheduling-agreement releases with cumulative quantities, product proposals, one-time customers and a cost-estimate hook.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Promise dates go stale after a planning run: AX ATP is recomputed only when the "Available ship and receipt dates" dialog opens; SAP VBEP dates change only on VA02 re-check or the nightly V_V2 rescheduling batch | ATP computed on demand inside the form; supply changes do not re-promise open lines | atp_bucket projection from inventory/planning events (lag < 30 s); re-promise on `order.changed` and supply cancellation; backward propagation (BR-27.1) | Promise accuracy ~70% → ≥ 95%; re-promise latency nightly → < 30 s |
| Credit holds are opaque: SalesOrderHold shows a code; OVA8 writes only VBAK-CMGST and the VKM1 list | Rules evaluated inside posting classes; only the final status persists | `credit-check.evaluated` stores every rule, value and threshold; release queue shows the failing rule | Analyst lookup per release 5–8 min (FD32/CustTable) → < 30 s; 2-business-day SLA by timer |
| Order entry takes 12+ clicks across SalesTable → SalesLine → InventDim → ATP dialog (VA01 overview → item → schedule lines) | Price, ATP and dims live on separate tabs | One grid with inline price and ATP within 300 ms of product + quantity | Clicks per priced, promised line 12 → 2 |
| Channel orders are re-keyed or loaded by nightly AIF/IDoc batches with no idempotency (duplicate ORDERS05 → duplicate VBAK) | Batch integration without a business-key dedup | Intake API with (channel, external_ref) idempotency at 500 orders/min, p99 < 500 ms; `order.confirmed` events for any consumer | Duplicates per 10k channel orders ~5 → 0; latency hours → < 500 ms |
| Changing a confirmed order loses history (SalesLine overwritten; CDHDR/CDPOS unreadable) | No versioned document | Versions with field-level diff and selective re-pricing/ATP/credit | "What changed and who": 15 min → < 5 s |

### 5.27.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Quotations | SalesQuotationTable/Line, SalesQuotationStatus | VA21/VA22, VBAK VBTYP B; VA11 inquiries | sales_quotation, …:convert | full |
| Sales orders | SalesTable/SalesLine, SalesStatus, SalesLineStatus, SalesType | VA01/VA02, VBAK/VBAP/VBEP, item categories TAN/TAS | sales_order, sales_order_line, order_schedule_line | full |
| Sales agreements | SalesAgreementHeader/AgreementLine, release orders | VA31 scheduling agreements, VA41 contracts | sales_agreement with release tracking | full |
| ATP / delivery date control | InventDim ATP (DlvDateControlType ATP/CTP), ATP time fence, SalesLine.ConfirmedDlv | OVZ9 checking rule, VBEP confirmed lines, CO09 | promising module, atp_bucket | full (CTP partial) |
| Credit limit | CustTable.CreditMax/MandatoryCreditLimit, CustParameters.CreditLimitType, D365 credit management | FD32/UKM_BP (KNKK), OVA8, VKM1 release | credit_profile, CREDIT hold, Temporal release | full |
| Order holds | SalesOrderHold / hold codes | VBAK-LIFSK delivery block, FAKSK billing block | order_hold (CREDIT, FRAUD, MANUAL, PRICE_REVIEW) | full |
| Change management | D365 order change approval | VA02 + CDHDR/CDPOS | sales_order.version + change_request | improved |
| Configured products | PCConfiguration on SalesLine | Variant configuration, KMAT | configuration_id (w8) + production link (w21) | partial |
| Backorders / alternative items | SalesBackorder; InventTable.AltItemId | V_RA; VB11 substitution | backorder workspace; alternative in ATP fallback | full |
| Multi-channel intake | AIF SalesSalesOrderService | IDoc ORDERS05, BAPI_SALESORDER_CREATEFROMDAT2 | POST /sales/orders with idempotency | improved |
| Supplementary items | PriceDiscSuppItem, supplementary items prompt on SalesLine | Free goods NA00 (w26), item proposals | supplementary_item_rule + free-goods lines | full |
| Listing / exclusion | — (customer-specific item blocking via InventTable stopped flags) | VB01/VB02/VB03, OV04 listing/exclusion procedures (T684) | product_listing (ALLOW / DENY per customer, group) | full |
| Product allocation | — | OVZJ/OVZI product allocation (T190, KOTA), aATP product allocation | allocation_quota per (product, customer/region, period) | full |
| Backorder processing strategies | SalesBackorder, backorder lines by item | V_V2 rescheduling; aATP BOP strategies win/gain/redistribute/fill/lose, release for delivery | backorder_run with strategy and priority | full |
| Customer consignment | — (consignment is vendor-side in D365) | VA01 KB/KE/KA/KR, MB58, special stock W | consignment_fill_up / issue / pick_up / return orders with owner dimension (w16) | partial (ownership dimension w16) |
| Sales-order special stock | Reservation against order (InventTrans marking) | Special stock E (sales order stock), MIE1, OVZG requirements class | sales_order_stock reservation segment; valuation in w21/w39 | partial |
| Scheduling agreement releases | SalesAgreement + release orders; AIF DELFOR (w35) | VA31/VA32 with JIT/forecast delivery schedules, cumulative quantities, DELFOR/DELJIT | sales_agreement.schedule_release with cumulative reconciliation | full (EDI mapping w35) |
| Cross-selling / product proposal | — | VB41 cross-selling, dynamic product proposal | product_proposal rule | full |
| One-time customer | One-time customer (CustTable.OneTimeCustomer) | CPD account group, document-level address (OBD2) | one_time party role (w7) + order-level name/address | full |

### 5.27.3 Functional requirements
- **FR-27.1 Quotations** — Must. Priced quotations with expiry (default +30 d) and probability, convertible whole or by line to one order preserving explain refs.
- **FR-27.2 Sales orders** — Must. Header (customer, invoice account, currency, delivery address, terms, procedure) and lines (product, variant dims, qty, unit, site/warehouse, requested dates, price fields). Notes: types JOURNAL, ORDER, SUBSCRIPTION, RETURNED; ≤ 2,000 lines; sequence `SO-`.
- **FR-27.3 Delivery date control** — Must. Confirmed ship/receipt dates per line by NONE, SALES_LEAD_TIME, ATP or CTP, applying transport days and the customer receiving calendar.
- **FR-27.4 Cumulative ATP** — Must. Per (product, dims, site, warehouse): on-hand available + scheduled receipts − scheduled issues within the time fence, with backward propagation of negative periods (BR-27.1). Notes: supply = confirmed POs (w17), released/started production orders (w21), transfers, planned orders optional (default excluded); demand = confirmed sales lines, production BOM lines, transfer issues, optional safety stock.
- **FR-27.5 CTP** — Should. When ATP fails, request a capable-to-promise date from scheduling (w23) using BOM/route lead time and finite capacity, creating a planned order link.
- **FR-27.6 Backorders and partials** — Must. Split lines into schedule lines when partial delivery is allowed; backorder list by promised date.
- **FR-27.7 Credit profile and checks** — Must. Per customer per entity: limit, currency, limit type, tolerance, review date, max dunning level; rules in BR-27.3 evaluated at entry, confirmation, release and invoicing per policy.
- **FR-27.8 Holds and release** — Must. Holds block confirmation/release; Temporal release workflow with approvers by duty and a 2-business-day SLA timer.
- **FR-27.9 Change management** — Must. Version on every post-confirmation change with field-level diff; re-run pricing/ATP/credit for affected lines; optional approval per customer.
- **FR-27.10 Sales agreements** — Should. Blanket commitment by quantity or amount; released orders inherit price and decrement commitment atomically.
- **FR-27.11 Configured products** — Should. Accept a configuration id; on confirmation create the production/planned order linkage for make-to-order items.
- **FR-27.12 Intake API** — Must. External orders with idempotency keys at 500 orders/min, p99 < 500 ms, returning id and holds.
- **FR-27.13 Alternative items** — Could. Offer the configured alternative with its ATP when the ordered product fails inside the requested date.
- **FR-27.14 Supplementary items** — Should. The system shall, at line commit, evaluate supplementary item rules (product or product group × customer or group; MANDATORY or OPTIONAL; quantity per base quantity; free or priced) and w26 FREE_GOODS proposals, add mandatory lines automatically and prompt for optional ones; supplementary lines reference the parent line and are canceled with it. Notes: AX PriceDiscSuppItem semantics; free lines carry unit_price 0.00 and `supplementary_of`.
- **FR-27.15 Listing and exclusion** — Should. The system shall validate every order line against product listings (ALLOW lists) and exclusions (DENY lists) per customer or customer group with validity, rejecting a denied line with 422 `PRODUCT_NOT_LISTED` and an override only with duty SalesOrderApprove. Notes: SAP VB01 listing/exclusion and OV04 procedures map to one `product_listing` entity.
- **FR-27.16 Product allocation** — Could. The system shall hold allocation quotas per (product or group, customer or region, period) consumed by ATP confirmation; when the quota is exhausted the line is confirmed at the next period with source `ALLOCATION`. Notes: SAP OVZJ/OVZI; quota consumption is reversible on cancellation; quota inquiry per period.
- **FR-27.17 Backorder processing strategies** — Should. The system shall run a backorder processing run per (product, site) that re-confirms open lines by strategy WIN (protect confirmed), GAIN (improve), REDISTRIBUTE (by priority), FILL (first-come) or LOSE (deconfirm low priority) with a priority from customer priority class, requested date and order value, and a preview before commit. Notes: replaces SAP V_V2 and aATP BOP; AX backorder workspace is the UI; run results emit `atp.promised` per changed line.
- **FR-27.18 Customer consignment** — Could. The system shall support consignment fill-up (ship to customer-owned location, inventory stays owned by the entity with owner dimension CUSTOMER_LOCATION from w16), consignment issue (customer consumes; invoice created), pick-up (return to own warehouse) and consignment return orders, with a stock-at-customer inquiry (SAP MB58). Notes: depends on the w16 ownership/location dimension; packing slips for fill-up post no COGS (BR-28.1 extension).
- **FR-27.19 Sales-order special stock** — Should. The system shall, for make-to-order lines, segregate supply and demand per order (`sales_order_stock = true` on the w16 reservation segment and on w21 production orders) so that ATP for that line consults only its own supply and the stock cannot be consumed by other orders; valuation of the segregated stock (valuated/non-valuated) remains with w21/w39. Notes: SAP special stock E, requirements class OVZG; AX "marking" of InventTrans.
- **FR-27.20 Scheduling agreement releases** — Should. The system shall accept forecast and JIT delivery schedules against a sales agreement (release number, cumulative received quantity, schedule lines by date, horizon), reconcile cumulative quantities against shipped quantities (BR-27.12), replace the previous release atomically and generate or adjust order schedule lines. Notes: SAP VA31/VA32 with DELFOR/DELJIT; inbound EDI 830/862 mapping arrives in w35 through this API; cumulative-quantity reset at fiscal year change per customer.
- **FR-27.21 Cross-selling and product proposals** — Could. The system shall hold product proposal rules (ordered product → proposed products with reason CROSS_SELL, UP_SELL, ACCESSORY, from w8 related products) and show proposals at line commit without creating lines unless accepted. Notes: SAP VB41, dynamic product proposal; acceptance rate recorded for analytics.
- **FR-27.22 One-time customers** — Should. The system shall allow an order and its invoice for a one-time party (w7 role ONE_TIME, one shared account per entity) with document-level name, address and tax id captured on the order header and carried to the invoice and open item. Notes: SAP CPD account group; AX one-time customer flag; credit check uses `one_time_order_limit` (default 5,000 USD / 4,500 EUR).
- **FR-27.23 Sales order cost estimate hook** — Could. The system shall expose `POST /sales/orders/{id}/lines/{l}:estimate-cost` that calls the w20 cost snapshot (stocked) or the w39 simulation API (configured/MTO) and stores `estimated_cost` and margin on the line for quotation margin display. Notes: SAP CK51N; full sales-order costing is delivered by wave 39.

### 5.27.4 Data model

**sales_order** (SalesTable / VBAK)

| Field | Type | Required | Notes |
|---|---|---|---|
| id, order_number | uuid, text | Y | SO- sequence, unique per entity |
| tenant_id, legal_entity_id | uuid | Y | RLS |
| customer_id, invoice_account_id | uuid | Y | FK party (w7) |
| order_type, status | enum | Y | see BR-27.5 |
| currency, pricing_procedure_version_id | char(3), uuid | Y | |
| delivery_address_id, delivery_terms, mode_of_delivery | uuid, text, text | Y | |
| payment_terms_id, cash_discount_id | uuid | Y/N | w7 terms |
| requested_ship_date, requested_receipt_date | date | Y | |
| credit_check_result | jsonb | N | last evaluation |
| version, etag | int, text | Y | optimistic concurrency |
| net/discount/tax/gross totals | numeric(19,4) | Y | derived, stored |
| channel, external_ref, idempotency_key | text | N | unique (tenant, channel, external_ref) |

**sales_order_line** (SalesLine / VBAP): id, order_id, line_no, product_id, variant_dims, quantity, unit, site_id, warehouse_id, unit_price, disc_pct_1/2, line_net, tax_group, delivery_date_control, requested/confirmed ship and receipt dates, line_status (OPEN | RESERVED | PICKED | DELIVERED | INVOICED | CANCELLED), delivered_qty, invoiced_qty, remaining_qty, configuration_id, production_order_id, agreement_line_id, price_explain_ref, multiline_group, reservation_id. **order_schedule_line** (VBEP): line_id, seq, qty, confirmed_ship_date, source (atp_bucket id | planned order id).

**credit_profile**: customer_id, legal_entity_id, credit_limit numeric(19,4), currency, limit_type (NONE | BALANCE | BALANCE_PACKING_SLIP | BALANCE_ALL), tolerance_pct (5), tolerance_amount (1,000.00), oldest_item_days_max (30), max_dunning_level (3), review_date, risk_group, mandatory_check, exposure jsonb (open_orders, delivered_not_invoiced, open_ar, unapplied_cash, updated_at).

**order_hold**: id, order_id, hold_type, reason_code, status (ACTIVE | RELEASED | CLEARED), applied_by, released_by, workflow_run_id, applied_at, released_at. **atp_bucket** (projection): product_id, dims hash, site_id, warehouse_id, bucket_date, supply_qty, demand_qty, period_atp, cumulative_atp, source_refs, computed_at.

Invariants: Σ schedule_line.qty = line.quantity; delivered_qty ≤ quantity; remaining_qty = quantity − delivered_qty (0 after remainder cancellation); order status derives from line statuses; an order with an ACTIVE hold is never released; cumulative_atp[n] = min over k ≥ n of Σ period_atp[0..k]; every ATP-confirmed line references an atp_bucket as of confirmation. Indexing: lines (order_id, line_no); partial index on open line statuses for demand; atp_bucket PK (product, dims, site, warehouse, bucket_date); partition orders by legal_entity.

### 5.27.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/sales/quotations, …/{id}:send, :convert | Quotation lifecycle | Idempotency-Key | SalesQuotationMaintain |
| POST | /api/v1/sales/orders | Create (UI or channel) | Idempotency-Key + external_ref | SalesOrderMaintain / ChannelIntake |
| PATCH | /api/v1/sales/orders/{id} | Change, creates version | If-Match etag | SalesOrderMaintain |
| POST | /api/v1/sales/orders/{id}:confirm, :cancel; /lines/{l}:cancel-remainder | Price, ATP, credit, confirm; cancellation | Idempotency-Key | SalesOrderConfirm |
| POST/GET | /api/v1/sales/atp:check; /atp/buckets?product=&site= | Simulate ATP; bucket view (CO09) | stateless | SalesOrderView |
| POST | /api/v1/sales/orders/{id}/holds, …/holds/{h}:release | Apply / release hold | Idempotency-Key | SalesOrderHold / CreditRelease |
| GET/PUT | /api/v1/sales/credit-profiles/{customerId} | Credit profile | etag | CreditMaintain |
| POST | /api/v1/sales/agreements, …/{id}:release-order | Agreements | Idempotency-Key | SalesAgreementMaintain |

Validation: confirm rejects unpriced lines (unless manual), quantity ≤ 0, customer status BLOCKED (w7); closed period on order date is a warning only (period control applies at posting).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.sales.order.confirmed.v1 | confirm | order_id, customer_id, currency, lines[{line_id, product, dims, qty, site, warehouse, confirmed_ship_date}], totals, version | fulfillment, planning (w22), credit, analytics |
| dev.passage.sales.order.changed.v1 / cancelled.v1 | PATCH / cancel | order_id, version, diff[] or line_ids, reason | fulfillment, planning, portal |
| dev.passage.sales.atp.promised.v1 | confirm with ATP/CTP | line_id, requested, confirmed, bucket_date, source | planning, portal |
| passage.sales.order-hold.applied.v1 / released.v1 | hold lifecycle | order_id, hold_type, reason, actor, workflow_run_id | fulfillment, collections, portal |
| passage.sales.credit-check.evaluated.v1 | any check | customer_id, order_id, rules[{id, result, value, threshold}], outcome | audit, collections |
| dev.passage.sales.quotation.converted.v1 | convert | quotation_id, order_id, lines | analytics, CRM |

### 5.27.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Sales workspace | tiles: on hold, backorders, quotes expiring 7 d, intake errors | counts | SalesOrderView |
| Quotation detail | add line, price, send PDF, convert, mark lost | customer, lines, expiry, probability, totals | SalesQuotationMaintain |
| Order entry (header + grid) | add line with ATP badge, price, confirm, hold, cancel, copy | product, qty, unit, site/wh, requested/confirmed dates, price, disc, net, status | SalesOrderMaintain |
| ATP explorer | pick product/site, view buckets, simulate | bucket date, supply, demand, period ATP, cumulative ATP, sources | SalesOrderView |
| Credit release queue | view exposure, release/reject with comment | customer, order, amount, exposure vs limit, rule failed, age | CreditRelease |
| Backorders / change requests | re-promise, split, cancel remainder; approve diff | line, promised date, qty; field, old, new | SalesOrderMaintain / SalesOrderApprove |

Interaction rules: Tab/Enter across the grid; product + quantity triggers inline price and ATP within 300 ms; validation at line commit and confirm; bulk re-promise on multi-select; default site/warehouse and column sets personalizable; changes to a confirmed order show the diff before save.

### 5.27.7 Business rules and edge cases
- **BR-27.1 Cumulative ATP worked example.** FG-100, site S1, warehouse W11, time fence 30 d, on-hand 100 on 2027-03-01. Supply: PO-2001 50 on 03-04; PRD-3001 80 on 03-08. Existing demand: SO-1001 60 on 03-02, SO-1002 40 on 03-05, SO-1003 70 on 03-09. Buckets open at each receipt: bucket 0 (03-01) supply 100, demand 60, period ATP 40; bucket 1 (03-04) 50 − 40 = 10; bucket 2 (03-08) 80 − 70 = 10. Cumulative ATP: 40, 50, 60. A new line of 45 requested 03-01: bucket 0 cumulative 40 < 45; first bucket with cumulative ≥ 45 is bucket 1 → confirmed ship date 2027-03-04 for complete delivery, or schedule lines 40 on 03-01 and 5 on 03-04 if partial delivery is allowed. After confirming 45 on 03-04, bucket 1 period ATP = −35; negative periods propagate backward, so cumulative becomes 5, 5, 15. Had SO-1002 been 60, bucket 1 period ATP −10 would already have reduced bucket 0 cumulative to 30.
- **BR-27.2 Dates.** confirmed_receipt = confirmed_ship + transport days (mode of delivery) rolled to the customer's next receiving day; past requested dates become today with a warning; dates beyond the fence are promised at fence end with source `TIME_FENCE`.
- **BR-27.3 Credit check rules** (any FAIL → CREDIT hold; WARN displayed only): CR-1 exposure + order amount > limit × (1 + tolerance_pct/100) and > limit + tolerance_amount → FAIL, where exposure by limit_type is BALANCE = open AR + unapplied; BALANCE_PACKING_SLIP adds delivered-not-invoiced; BALANCE_ALL adds confirmed open orders; CR-2 oldest open item overdue > oldest_item_days_max → FAIL; CR-3 dunning level ≥ max_dunning_level → FAIL; CR-4 review_date passed or profile missing while mandatory_check → FAIL; CR-5 overdue > 25% of open AR → WARN; CR-6 order > 50,000 USD / 45,000 EUR for a customer younger than 90 days → FAIL. Amounts convert to the profile currency at the wave 12 spot rate on the check date.
- **BR-27.4 Hold precedence.** CREDIT and FRAUD block confirmation and release; MANUAL and PRICE_REVIEW block release only; a CREDIT release is valid 7 days; an amount increase > 5% re-triggers CR-1.
- **BR-27.5 State machines.** Order: DRAFT → OPEN → CONFIRMED → PARTIALLY_SHIPPED → SHIPPED → INVOICED → CLOSED; CANCELLED from DRAFT/OPEN/CONFIRMED. Line: OPEN → RESERVED → PICKED → DELIVERED → INVOICED; CANCELLED from OPEN/RESERVED. Quotation: DRAFT → SENT → CONVERTED | LOST | EXPIRED | CANCELLED.
- **BR-27.6 Concurrency.** PATCH requires If-Match; concurrent confirm and change → later call 409; ATP consumption serialized per (product, dims, site, warehouse) by Postgres advisory lock.
- **BR-27.7 Reservation.** ATP confirmation creates a wave 16 reservation when `reserve_on_confirm` (default true for ORDER); JOURNAL orders never reserve or consume ATP.
- **BR-27.8 Quantities** at 6 decimals; `whole_units` products reject fractions; unit conversion via wave 6 factors, half-even.
- **BR-27.9 Agreements** decrement commitment atomically and may not exceed it beyond overdelivery_pct (default 0); subscription/agreement lines re-price only when the agreement price changes.
- **BR-27.10 Intake duplicates.** Same (channel, external_ref) within 30 days returns the original id with 200; same key with a different payload → 409.
- **BR-27.11 Backorder run.** Lines are ordered by (priority class asc, requested date asc, order value desc); WIN never reduces a confirmed quantity; LOSE may deconfirm only lines with priority class ≥ the run's `lose_threshold`; a run is a preview until committed and commits serialize per (product, dims, site, warehouse) like confirmation.
- **BR-27.12 Cumulative quantities.** A release's cumulative received quantity (customer view) is compared with Σ shipped since the cumulative reset date; a difference beyond `cum_tolerance` (default 0) raises `CUM_QTY_MISMATCH` and the release is held for review; open schedule lines = release cumulative required − Σ shipped; a new release replaces all open lines of the same agreement item in one transaction.
- **BR-27.13 Listing precedence.** DENY at customer level beats ALLOW at group level; an empty ALLOW list means no restriction; validity evaluated on the order date.
- **BR-27.14 Supplementary lines** inherit site, warehouse, dates and delivery address from the parent, cannot be re-promised independently, and are removed when the parent quantity drops below the rule's base quantity.
- **BR-27.15 Allocation consumption** happens at confirmation in requested-period order; cancellation returns quota to the period it consumed; a quota change below consumed quantity is rejected with 409.

### 5.27.8 Acceptance criteria
- **AC-27.1** (FR-27.4) Given the BR-27.1 fixture, when a 45-unit line requests 2027-03-01 with complete delivery, then confirmed ship date = 2027-03-04 and the promised event references bucket 1.
- **AC-27.2** (FR-27.4) Given any `atp-100` fixture, when all orders confirm, then no cumulative ATP is negative after propagation and total promised ≤ on-hand + included supply (property).
- **AC-27.3** (FR-27.7, FR-27.8) Given limit 10,000 USD, open AR 8,500, tolerance 5% / 1,000, when a 2,000 USD order confirms, then no hold (10,500 within both tolerances) and a WARN; when a 3,000 USD order confirms, then a CREDIT hold applies (11,500 > 10,500 and > 11,000), and release by a CreditRelease user confirms the order and emits `order-hold.released`.
- **AC-27.4** (FR-27.11) Given a make-to-order product with configuration CFG-77, when confirmed, then a wave 21 production order references the line and the confirmed date = scheduled end + transport days.
- **AC-27.5** (FR-27.12) Given 500 orders/min for 10 min through intake, then p99 < 500 ms and all 5,000 orders exist exactly once.
- **AC-27.6** (FR-27.1) Given a sent 3-line quotation, when lines 1 and 3 convert, then one 2-line order exists with matching prices and explain refs and the quotation is CONVERTED.
- **AC-27.7** (FR-27.9) Given a confirmed order, when line 10 changes from 5 to 8, then version increments, the diff is recorded, only line 10 is re-promised and `order.changed` is emitted.
- **AC-27.8** (FR-27.6) Given partial delivery and ATP 40 of 45, then two schedule lines exist and the backorder list shows 5 units on 2027-03-04.
- **AC-27.9** (FR-27.10) Given an agreement of 1,000 units fully released, then the next release is rejected with 422 `AGREEMENT_EXHAUSTED`.
- **AC-27.10** (FR-27.14) Given a mandatory supplementary rule "1 × MANUAL-EN free per 10 × FG-100" and a line of 25 FG-100, then a zero-priced line of 2 MANUAL-EN with `supplementary_of` exists and is canceled when the parent is canceled.
- **AC-27.11** (FR-27.15) Given customer C9 with a DENY listing for FG-120, when a line for FG-120 is committed, then 422 `PRODUCT_NOT_LISTED`; with duty SalesOrderApprove and a reason the line is accepted and the override logged.
- **AC-27.12** (FR-27.17) Given 5 backorder lines for FG-100 with 40 units of new supply and strategy REDISTRIBUTE, when the run commits, then priority-class-1 lines are confirmed first, no cumulative ATP is negative, and `atp.promised` is emitted only for changed lines.
- **AC-27.13** (FR-27.20) Given an agreement item with cumulative shipped 1,200 and a new release stating cumulative received 1,150, then the release is held with `CUM_QTY_MISMATCH` and no schedule line changes; a release stating 1,200 with required 1,500 by 2027-04-15 creates open schedule lines totaling 300.
- **AC-27.14** (FR-27.22) Given a one-time order for "Walk-in Buyer GmbH" with a document address, when invoiced (w29), then the invoice and open item carry that name and address and the one-time account's exposure is checked against `one_time_order_limit`.

### 5.27.9 Test plan and fixtures
Fixtures: `atp-100` (from SAP OVZ9 and AX ATP examples with expected dates); `credit-40` (profiles plus migrated AR open items); `orders-5k-intake` (generated channel payloads); `quotes-50`; `demo-plant` (wave 25 plant with FG-100…FG-120 stock and open production orders).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| ATP scenarios | unit | promising | 100/100 expected dates |
| Never over-promise | property | promising | Σ promised ≤ supply in every bucket prefix |
| Credit rules | unit | credit | 40/40 outcomes incl. tolerance edges |
| Hold release | E2E (Temporal) | workflow | released; SLA timer fires at 2 business days |
| Intake throughput | performance | POST /orders | 500/min, p99 < 500 ms, 0 duplicates |
| Sales ↔ pricing/inventory/production | contract (Pact) | APIs | green |
| Order entry UI | E2E (Playwright + axe) | shell | flows pass, 0 serious a11y violations |

Reconciliation: Σ open confirmed order amounts per customer = credit_profile.exposure.open_orders; wave 16 reservations = Σ RESERVED line quantities; nightly rebuilt ATP projection = incremental projection (diff 0).

### 5.27.10 Deployment and operations
Helm `passage-sales` (Go, 3 replicas, HPA on RPS); `sales-db`; Temporal namespace `sales`. Config: `ATP_TIME_FENCE_DEFAULT_DAYS=30`, `ATP_INCLUDE_PLANNED_ORDERS=false`, `CREDIT_CHECK_POINTS=confirm,release`, `INTAKE_RATE_LIMIT_PER_KEY=1000/min`. Flags: `sales.ctp`, `sales.alternative_items`, `sales.change_approval`. Jobs: ATP rebuild 01:00 entity-local; quotation expiry hourly; credit review warnings daily. SLOs: create p99 500 ms, 99.95% availability, projection lag < 30 s. Alerts: lag > 120 s, hold queue age > 2 business days, intake 5xx > 0.1%. Runbook: projection rebuild, stuck release workflow, re-promise after supply cancellation. Rollback: previous digest; additive migrations.

### 5.27.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| SalesTable (SalesId, CustAccount, InvoiceAccount, CurrencyCode, SalesStatus) + SalesLine (ItemId, InventDimId, SalesQty, SalesPrice, LinePercent, RemainSalesPhysical/Financial) | sales_order, lines | open orders only (Backorder, or Delivered with financial remainder); InventDim → dims + site/warehouse |
| SalesQuotationTable/Line (Sent); SalesAgreementHeader/Line | sales_quotation; sales_agreement | remaining commitment from released qty |
| CustTable.CreditMax, MandatoryCreditLimit, CustParameters.CreditLimitType | credit_profile | 1:1 type mapping |
| VBAK/VBAP/VBEP (VBUK-GBSTK ≠ C), VBKD, VBPA | sales_order, lines, schedule lines | TAS → drop-ship flag; VBEP confirmed qty → schedule lines |
| KNKK (KLIMK, CTLPC), UKM_BP | credit_profile | risk class → risk_group |
| VBAK VBTYP B / E / G | sales_quotation / sales_agreement | |

Validation: count and Σ open value per customer equal the legacy extract; every migrated line re-promises to ≤ legacy confirmed date + 1 day or is flagged. Not migrated: closed orders (lakehouse), CDHDR logs, SAP credit horizon settings (replaced by BR-27.3).

### 5.27.12 Dependencies, risks, open questions
- Depends on: w26 pricing; w16 on-hand/reservations; w17 PO receipts; w21 production orders; w22 planned orders and demand; w23 scheduling (CTP); w7 customers/terms; w8 configurator; w4 Temporal.
- Risks: projection staleness during planning runs (planning emits a batch-end marker; buckets flagged stale); credit exposure incomplete before waves 29/30 (migrated open AR loaded into `receivables_db.open_item`, schema created this wave).
- Open questions: (1) Default date control for migrated customers, ATP vs. lead time? Default ATP; decide by wave 28. (2) External fraud scoring? Default rule-only in v1; decide by wave 30.

### 5.27.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| default_order_type | legal entity | enum | ORDER | JOURNAL, ORDER, SUBSCRIPTION | Type on new orders | AR parameters → General → Sales order type default (SalesType) | VOV8 default sales document type (OR) |
| quotation_expiry_days | legal entity | int | 30 | 1–365 | FR-27.1 | Sales and marketing parameters → Quotations → expiry | VOV8 QT validity period |
| quotation_probability_default | legal entity | int % | 50 | 0–100 | Pipeline weighting | Sales and marketing parameters → Quotations → probability | VBAK-AWAHR default |
| delivery_date_control_default | legal entity | enum | ATP | NONE, SALES_LEAD_TIME, ATP, CTP | FR-27.3 | AR parameters → Updates → Delivery date control (DlvDateControlType) | OVZ9 checking rule / OVZG requirements class |
| atp_time_fence_days | legal entity (override per product coverage group w22) | int | 30 | 1–365 | FR-27.4 window | ATP time fence (InventModelGroup / coverage group) | OVZ9 "Check horizon" |
| atp_include_planned_orders | legal entity | bool | false | true/false | FR-27.4 supply set | ATP incl. planned orders | OVZ9 "Include planned orders" |
| atp_include_safety_stock | legal entity | bool | false | true/false | Demand set | Coverage group safety stock in ATP (ReqGroup) | OVZ9 "Check without safety stock" (inverse) |
| reserve_on_confirm | legal entity | bool | true | true/false | BR-27.7 | AR parameters → General → Reservation (Automatic/Manual) | Schedule line category reservation (VOV6) |
| partial_delivery_default | customer (w7) with entity default | enum | ALLOWED | ALLOWED, COMPLETE_ONLY, ONE_SHIPMENT | FR-27.6 | SalesTable / CustTable "Complete delivery" (CompleteDelivery) flag | KNVV-KZTLF partial delivery indicator |
| overdelivery_pct / underdelivery_pct | product × customer | numeric % | 0 / 0 | 0–100 | BR-27.9, BR-28.7 | InventTable Over/underdelivery | VOV8 / KNVV over/underdelivery tolerance |
| credit_limit_type_default | legal entity | enum | BALANCE | NONE, BALANCE, BALANCE_PACKING_SLIP, BALANCE_ALL | FR-27.7 | AR parameters → Credit rating → Credit limit type (CustParameters.CreditLimitType) | OVA8 "Open orders / deliveries / billing" update group (000012/000015/000018) |
| mandatory_credit_check | legal entity | bool | false | true/false | CR-4 | AR parameters → "Mandatory credit limit" | OVA8 "No credit check" (inverse) |
| credit_check_points | legal entity | set | confirm, release | entry, confirm, release, packing_slip, invoice | FR-27.7 | AR parameters → Credit management → check points | OVA8 check at order/delivery/GI |
| credit_tolerance_pct / amount | legal entity | numeric | 5 / 1,000.00 | ≥ 0 | CR-1 | D365 credit management "Tolerance" | OVA8 "Deviation in %" / "Number of days" |
| credit_release_validity_days | legal entity | int | 7 | 1–30 | BR-27.4 | D365 credit management release validity | OVA8 "Released documents still unchecked" days |
| credit_hold_sla_business_days | legal entity | int | 2 | 1–10 | FR-27.8 timer | n/a | n/a (FSCM workflow) |
| change_approval_required | customer (w7) | bool | false | true/false | FR-27.9 | D365 Sales order change approval | n/a (VA02 unrestricted) |
| intake_rate_limit_per_key | tenant | text | 1000/min | rate expression | FR-27.12 | n/a | n/a |
| intake_dedup_window_days | tenant | int | 30 | 1–90 | BR-27.10 | n/a | n/a |
| supplementary_prompt_optional | legal entity | bool | true | true/false | FR-27.14 prompt behavior | Supplementary items prompt (AR parameters → Updates) | NA00 free goods automatic |
| listing_override_duty | legal entity | duty | SalesOrderApprove | duties | FR-27.15 | n/a | OV04 "listing check" switch |
| allocation_enabled | legal entity | bool | false | true/false | FR-27.16 | n/a | OVZJ activation |
| backorder_strategy_default / lose_threshold | legal entity | enum / int | REDISTRIBUTE / 3 | strategies / 1–9 | FR-27.17, BR-27.11 | n/a | aATP BOP segment strategy |
| cum_tolerance / cum_reset_rule | legal entity, customer | numeric / enum | 0 / FISCAL_YEAR | ≥ 0; FISCAL_YEAR, CALENDAR_YEAR, NEVER | BR-27.12 | n/a | VA32 cumulative quantity reset / year change |
| one_time_order_limit | legal entity | money | 5,000.00 USD / 4,500.00 EUR | ≥ 0 | FR-27.22 | One-time customer limit (AR parameters) | OBD2 CPD account group |
| sales_order_stock_default | product (w8) | bool | false | true/false | FR-27.19 | n/a (marking manual) | OVZG requirements class special stock E |

### 5.27.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Sales orders list | list inquiry | customer, status, date range, site, hold, channel | order, customer, requested/confirmed dates, net, status, holds | sales_order | All sales orders (SalesTableListPage) | VA05 / VA05N |
| Open order lines / backorders | list inquiry | product, customer, promised date, site | line, qty, remaining, confirmed date, source | sales_order_line (open) | Backorder lines by item / by customer; SSRS Open sales orders | V.15 / VA14L (blocked) / CO06 |
| ATP overview | drill-down | product, dims, site, warehouse, horizon | bucket date, supply, demand, period ATP, cumulative ATP, sources | atp_bucket | Available ship and receipt dates dialog | CO09 availability overview |
| Credit release queue | list inquiry | customer, rule failed, age, amount | customer, order, exposure vs limit, rule, age | order_hold + credit evaluations | Sales orders on hold (SalesOrderHold) / D365 Credit holds | VKM1 / VKM4 |
| Credit exposure by customer | operational report | customer, as-of | limit, open AR, unapplied, DNI, open orders, utilization % | credit_profile.exposure | SSRS Customer balance list with credit limit | FD33 / S_ALR_87012215 (credit overview) |
| Quotations pipeline | KPI | status, expiry window, probability | count, weighted value, conversion rate | sales_quotation | SSRS Sales quotation / quotation follow-up | VA25 quotation list |
| Sales agreements and releases | drill-down | agreement, customer, item | commitment, released, remaining, release history, cum received/shipped | sales_agreement, schedule_release | Sales agreements (fulfillment tab) | VA35 scheduling agreement list, VA33 cum quantities |
| Order change history | drill-down | order, version range | version, field, old, new, actor, reason | sales_order versions | n/a (no versioned history) | CDHDR/CDPOS change documents |
| Intake errors | list inquiry | channel, error code, date | external_ref, payload hash, error, retries | intake log | AIF exceptions | WE02 / WE05 IDoc errors |
| Listing / allocation inquiry | list inquiry | customer, product, period | listing entries; quota, consumed, remaining | product_listing, allocation_quota | n/a | VB03 / product allocation reports |
| Stock at customer (consignment) | list inquiry | customer, product | qty at customer, value, age | w16 on-hand by owner dimension | n/a | MB58 |
| Backorder run results | drill-down | run id | line, before, after, strategy reason | backorder_run | Backorder workspace | V_V2 log / aATP BOP results |

### 5.27.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Sales quotation | :send | header: quotation number, customer, contact, validity, currency, terms; lines: product, qty, unit, price, discount, net, promised date; footer: totals, tax estimate, probability hidden, T&C block | PDF, email, portal (w30), UBL quotation JSON for w35 | `SQ-` per legal entity | none; retain 3 years |
| Order confirmation | :confirm and on each version with `send_confirmation` | header: order, customer, invoice account, addresses, terms, mode of delivery; lines: product, qty, confirmed ship/receipt date, price, net; footer: totals, holds not shown | PDF, email, EDI ORDRSP/855 hook (w35), portal | `OC-` per legal entity (document copy number per version) | commercial record; retain 7 years with invoice |
| Sales agreement confirmation | agreement activation | header: agreement, customer, validity, commitment type; lines: product/group, committed qty or amount, price; footer: release terms | PDF, email | `SA-` | 7 years |
| Scheduling agreement release acknowledgment | release accepted | header: agreement item, release number, cum received/shipped; lines: schedule dates and quantities | PDF, EDI (w35) | release number from customer | 7 years (automotive audit) |
| Credit hold notice (internal) | hold applied | customer, order, failing rule, exposure, approver | email, in-app | none | none |
| Quotation lost / expired notice | status change | quotation, reason | email (internal) | none | none |

### 5.27.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| ATP projection rebuild | 01:00 entity-local daily | (product, site) | full rebuild into shadow table, swap | < 15 min per 1M buckets | keep previous projection; alert | diff incremental vs rebuilt ≠ 0; lag > 120 s |
| Backorder processing run | 05:00 daily per site (after planning w22) | (product, site) | run id; preview persisted; commit idempotent per line version | < 10 min per 100k open lines | partial commit impossible (per-product transaction) | run > 30 min; deconfirmed lines > 5% |
| Quotation expiry | hourly | legal_entity | status transition by (id, version) | < 30 s | retry | none expired for 2 days while candidates exist |
| Credit review warnings / profile refresh | 06:00 daily | customer | key = (customer, date) | < 5 min | retry | profiles with review_date passed > 100 |
| Credit exposure recompute | event-driven; nightly reconcile 01:30 | customer | exposure.updated_at | < 10 min | rerun | Σ open orders ≠ exposure.open_orders |
| Scheduling release reconciliation | on release receipt; nightly cum check 23:00 | agreement item | release number | < 1 min | hold release | CUM_QTY_MISMATCH count > 0 |
| Allocation period roll | first day of period 00:05 | product | key = (quota, period) | < 1 min | retry | missing quota period |
| Intake retry / DLQ replay | every 5 min | channel | (channel, external_ref) | < 1 min | DLQ after 10 attempts | DLQ depth > 50 |
| Order auto-close | nightly 02:00 | legal_entity | (order, version) | < 5 min | retry | none |

### 5.27.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| SalesOrderView | GET orders, quotations, atp buckets; Sales workspace, ATP explorer | none | Sales rep, Customer service, Warehouse, Finance |
| SalesQuotationMaintain | POST quotations, :send, :convert; Quotation detail | none | Sales rep |
| SalesOrderMaintain | POST/PATCH orders, lines, :cancel-remainder; Order entry; backorders | with CreditRelease; with PricingOverride when `four_eyes_override` | Sales rep, Customer service |
| SalesOrderConfirm | :confirm | with CreditRelease | Sales rep (within limits), Sales manager |
| SalesOrderApprove | approve change requests; listing override | with SalesOrderMaintain on own orders | Sales manager |
| SalesOrderHold | POST holds (MANUAL, PRICE_REVIEW, FRAUD) | none | Sales manager, Fraud analyst |
| CreditRelease | holds:release (CREDIT); Credit release queue | with SalesOrderMaintain, SalesOrderConfirm, CreditMaintain on the same customer | Credit manager |
| CreditMaintain | PUT credit-profiles | with CreditRelease | Credit analyst |
| SalesAgreementMaintain | agreements, :release-order, schedule releases | none | Key account manager |
| ChannelIntake | POST orders via channel client credentials | none | Integration service accounts (w35) |
| BackorderRun | run and commit backorder processing | with SalesOrderMaintain on affected orders (logged, not blocked) | Order management lead |
| AllocationMaintain | allocation quotas | with BackorderRun | Demand planner |
| ConsignmentMaintain | consignment fill-up/issue/pick-up/return | with WarehouseReceive (w28) | Key account manager |

### 5.27.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| SLS-0001 | error | Line {n} has no price; enter a manual price or add an agreement | PRICE_NOT_FOUND at confirm | Add agreement or override | FR-27.2 / BR-26.1 |
| SLS-0002 | error | Customer {customer} is blocked for sales | w7 status BLOCKED | Unblock in party governance | §5.27.5 validation |
| SLS-0003 | warning | Requested date {date} is in the past; moved to today | BR-27.2 | none | BR-27.2 |
| SLS-0004 | info | Requested date beyond the ATP fence; promised at fence end {date} | BR-27.2 | Shorten lead time or extend fence | BR-27.2 |
| SLS-0005 | error | Credit hold: rule {rule} failed ({value} vs {threshold}) | BR-27.3 | Request release in the credit queue | FR-27.7 |
| SLS-0006 | error | Order was changed by another user; reload | If-Match mismatch | Reload and reapply | BR-27.6 |
| SLS-0007 | error | Duplicate order: {channel}/{external_ref} differs from the stored payload | BR-27.10 | Use a new external_ref | FR-27.12 |
| SLS-0008 | error | Agreement {id} commitment exhausted | BR-27.9 | Increase commitment or create a new agreement | FR-27.10 |
| SLS-0009 | error | Product {product} rejects fractional quantities | whole_units | Enter whole units | BR-27.8 |
| SLS-0010 | warning | Overdue > 25% of open AR for {customer} | CR-5 | Inform collections | BR-27.3 |
| SLS-0011 | error | Product {product} is not listed for {customer} | FR-27.15 | Override with SalesOrderApprove or change product | BR-27.13 |
| SLS-0012 | warning | Mandatory supplementary item {product} added ({qty}) | FR-27.14 | none | BR-27.14 |
| SLS-0013 | error | Allocation for {product} in {period} exhausted; promised {date} | FR-27.16 | Request quota change | BR-27.15 |
| SLS-0014 | error | Cumulative quantity mismatch: customer {cum_received}, shipped {cum_shipped} | BR-27.12 | Review the release; adjust tolerance or correct shipments | FR-27.20 |
| SLS-0015 | warning | Backorder run would deconfirm {n} lines | BR-27.11 preview | Review before commit | FR-27.17 |
| SLS-0016 | error | One-time order exceeds limit {limit} | FR-27.22 | Create a regular customer | FR-27.22 |
| SLS-0017 | error | Consignment issue exceeds stock at customer ({qty} available) | FR-27.18 | Fill up first or correct quantity | FR-27.18 |
| SLS-0018 | info | Cost estimate {amount}, margin {pct}% | FR-27.23 | none | FR-27.23 |

### 5.27.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| POST /sales/orders (intake, 10 lines) | 50k per day; peak 500/min | 120 / 300 / 500 ms | 20 req/s per replica | 20M lines × 1.5 KB = 30 GB | orders partitioned by legal entity; lines (order_id, line_no) | 429 with Retry-After per key; HPA; holds evaluated asynchronously if > 1,500/min |
| Inline price + ATP at line commit | 500k per day | 80 / 300 / 600 ms | 100/s | none | atp_bucket PK lookup | ATP badge shows "pending" after 300 ms; confirm still synchronous |
| :confirm (10 lines) | 50k per day | 200 / 600 / 1,200 ms | 30/s | events 10 KB each | advisory lock per product | confirm queue; p95 ≤ 2 s |
| ATP projection update | 1M events per day | 2 / 10 / 30 s lag | 1,000 events/s | 50M buckets × 200 B = 10 GB | PK (product, dims, site, warehouse, bucket_date) | buckets flagged stale; promise falls back to lead time with warning |
| Credit check | 100k per day | 20 / 80 / 200 ms | 100/s | evaluations 30 GB (jsonb, 2-year retention) | (customer, evaluated_at) | cached exposure (≤ 60 s) under load |
| Backorder run (100k lines) | 1 per site per day | — / 10 min / 20 min | 200 lines/s | run logs 5 GB | per-product batches | run split by product group; preview only |
| Scheduling release import | 5k releases per day | 50 / 200 / 500 ms | 20/s | 2 GB | (agreement_item, release_no) | queue; acknowledgment delayed |

### 5.27.20 Localization and compliance hooks

- Tax ids and VAT validation on one-time orders use the w13 validator per country (VIES for EU, EIN format for US); the document-level address feeds tax determination exactly as a master address.
- Calendars: customer receiving calendars and transport days (BR-27.2) come from the w6 calendar service with country holiday packs (w48); cumulative-quantity reset follows the customer's fiscal year (w14).
- Currency and rounding: credit exposure converts at the w12 spot rate on the check date; quantities at 6 decimals; thresholds in BR-27.3 hold USD/EUR defaults overridable per entity.
- Documents: quotation and confirmation templates per language and entity (w5 docgen); country packs may add mandatory legal footer text (for example EU distance-selling withdrawal notice) through the template `legal_footer` slot.
- Export control and sanctioned-party screening are out of scope here (w48 compliance hooks `order.confirmed` consumer may apply a FRAUD/COMPLIANCE hold through the holds API).
- Privacy: portal and one-time contacts are personal data (w48 retention); order history pseudonymizes contact names after 7 years.
- Automotive packs (DE/US) extend FR-27.20 with EDIFACT DELFOR/DELJIT and VDA 4905/4915 mappings through w35; the cumulative model here is the extension point.

### 5.27.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.3.4 #31 — Trade agreements → Supplementary items (PriceDiscSuppItem) | PARTIAL-26 (proposed 27) | ADDED as FR-27.14 | free-goods proposals from w26 FR-26.22 |
| AX §12.17.1 #168 — Sales order → Supplementary items (prompt) | PARTIAL-26 (proposed 27) | ADDED as FR-27.14 | supplementary_prompt_optional |
| AX §12.17.4 #171 — Sales orders → Supplementary items setup | PARTIAL-26 (proposed 27) | ADDED as FR-27.14 | rule entity supplementary_item_rule |
| SAP §13.8 #90 — CK51N / CK53N sales order cost estimate | PARTIAL-27 | ADDED as FR-27.23 (hook); full costing DEFERRED to wave 39 | estimate stored on line |
| SAP §13.8 #98 — OVZG sales order costing / valuation of sales-order stock | PARTIAL-27 | DEFERRED to wave 21 / 39 | segregation itself ADDED as FR-27.19; valuation stays with costing waves |
| SAP §13.13 #141 — MB58 consignment and returnable packaging stock at customer | PARTIAL-27 | ADDED as FR-27.18 (consignment); returnable packaging DEFERRED to wave 28 (FR-28.16) | needs w16 owner dimension |
| SAP §13.13 #143 — MIE1 / MIW1 / MIV1 physical inventory for sales-order stock and consignment | PARTIAL-27 | ADDED as FR-27.18 / FR-27.19 (dimensions); counting itself SUPERSEDED by w16 counting journals on the owner / sales-order segment | |
| SAP §13.13 #145 — Special stock E / Q (PARTIAL-16, note names wave 27) | PARTIAL-16 | ADDED as FR-27.19 for sales-order stock; project stock Q DEFERRED to wave 41 | cross-reference |
| SAP §13.19 #216 — VB01/VB02/VB03 listing/exclusion | PARTIAL-27 | ADDED as FR-27.15 | |
| SAP §13.19 #218 — VA01 KB/KE/KA/KR consignment processes | PARTIAL-27 | ADDED as FR-27.18 | Could; depends on w16 |
| SAP §13.19 #219 — VA31/VA32/VA33/VA35 scheduling agreements with JIT/forecast schedules, DELFOR/DELJIT | PARTIAL-27 | ADDED as FR-27.20 | EDI mapping in wave 35 |
| SAP §13.19 #222 — VB41 cross-selling, dynamic product proposal | PARTIAL-27 | ADDED as FR-27.21 | |
| SAP §13.19 #223 — OVZJ / OVZI product allocation | GAP (propose 27) | ADDED as FR-27.16 | allocation_enabled |
| SAP §13.19 #224 — OVZK / aATP backorder processing strategies, release for delivery | PARTIAL-27 | ADDED as FR-27.17 | release for delivery = w28 release rules |
| SAP §13.19 #225 — OVZG/OVZH/OVZI requirements classes / strategy groups | PARTIAL-27 | ADDED as FR-27.19 (special stock part); planning strategies remain wave 22 | |
| SAP §13.19 #226 — OV04 listing/exclusion procedures | PARTIAL-27 | ADDED as FR-27.15 | one entity, precedence BR-27.13 |
| SAP §13.2 #23 / #37 — One-time account (CPD, OBD3) and SAP §13.3 head-office/branch #50 (PARTIAL-7, flagged for order-to-cash) | PARTIAL-7 | ADDED as FR-27.22 (one-time orders); head-office/branch SUPERSEDED by sales_order.invoice_account_id (FR-27.2) and open_item.invoice_account_id (FR-30.1) | party roles themselves remain wave 7 |

## 5.28 Wave 28: Fulfillment: Picking, Packing & Shipping

### 5.28.1 Scope and business context
Wave 28 ships the `fulfillment` service and the `carrier-gateway` adapter. Confirmed orders release to the warehouse by rule, are picked (basic wave/batch picking; location directives and RF arrive in wave 31), packed into containers with SSCC labels, rated and labelled through a carrier abstraction, and shipped with a posted packing slip that deducts inventory and posts cost to the universal journal. Drop-ship and partial shipments are supported; tracking is published for the portal. Out of scope: location directives, license plates and mobile RF flows (wave 31), transportation planning and freight reconciliation (wave 34), hazmat and export compliance. Added from the coverage registers (§5.28.21): picking workbench, packaging materials with packaging fees, packing instructions (auto-pack) and returnable transport packaging balances.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Delivered-not-invoiced never ties: AX "Cost of units delivered" via InventPosting drifts from CustPackingSlipJour after invoice cancellations; SAP PGI posts COGS at once so revenue and COGS land in different periods | Physical and financial postings decoupled across SalesFormLetter classes; reconciliation is a month-end report | Packing slip and invoice share one cost_snapshot_id; DNI projection reconciled to GL 1405 daily; AX-style and PGI-style matrices by policy | Month-end DNI reconciliation 4–8 h → 0 (daily, diff 0.00) |
| Packing-slip posting blocks the user 5–20 s and fails opaquely ("Update has been cancelled"; VL02N "posting period closed" after picking) | Synchronous multi-table update with late validation | Pre-validation at PACKED (period, holds, dims, negative stock) and a Temporal saga with compensation | Failures discovered after pick ~3% → < 0.1%; post p95 < 2 s |
| Each carrier is a project (AX ShipCarrier via desktop add-on; SAP XSI/TM) | No provider abstraction | carrier-gateway rate/label/track contract, reference adapters, `tracking-updated` events | Adding a carrier 4–8 weeks → < 2 days; tracking visible on 100% of shipments |
| Short picks and partials need manual SalesLine splits or VBEP edits | Schedule lines are not first-class in fulfillment | Schedule lines drive shipments; short pick auto-creates the remainder and releases its reservation | Manual splits per 1,000 orders ~40 → 0 |
| SSCC errors at customer receiving (bad check digit, duplicate on re-print) | SSCC generated by label software, not validated by the ERP | Gap-free SSCC per GS1 prefix with mod-10 check, uniqueness and 12-month no-reuse in the container entity | Label rejections at customer DC ~1% of pallets → 0 |

### 5.28.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Release to warehouse | Release to warehouse, SalesLine.ReleaseStatus, InventPickingList | VL01N/VL10, LIKP/LIPS | shipment (release rules) | full |
| Picking | WMSPickingRoute, WMSOrderTrans, picking list registration | LT03 transfer order (LTAP); EWM warehouse task | pick_work | partial (advanced w31) |
| Packing / HU | WHSContainerTable, packing station | VEKP/VEPO handling units, HUMO | container (nested) | full |
| SSCC / labels | WHSContainerTable.LicensePlate | HU external id, GS1-128 | container.sscc, PDF/ZPL label | full |
| Packing slip posting | CustPackingSlipJour/Trans, SalesFormLetter_PackingSlip, InventTrans StatusIssue=Deducted | VL02N post goods issue, MKPF/MSEG 601, LIKP-WBSTK | packing_slip saga | full |
| COGS posting | InventPosting: cost of units delivered / cost of goods delivered | OBYC GBB-VAX, BSX | matrix BR-28.1 | full |
| Carrier interface | ShipCarrierTable, ShipCarrierShippingRequest | TM / Express Ship Interface | carrier-gateway | improved |
| Drop-ship | Direct delivery (PurchTable.DirectDelivery) | Third-party item category TAS, statistical GR | drop_ship flow | full |
| Delivery schedules / ASN | SalesDeliverySchedule; AIF DESADV | VBEP; DESADV IDoc | schedule lines → shipments; ASN event (EDI w35) | partial |
| Picking workbench | Sales picking workbench (R3), picking routes setup, grouped picking lists | VL10 delivery due list + LT03 grouped transfer orders | pick_workbench (filtered grouped waves) | full |
| Packaging materials and fees | Packaging materials, packing material fees (EU), PI container types / packaging groups | Packaging material types VHAR, packaging materials (MARA MTART VERP) | packaging_material, packaging_fee, container_type | full |
| Packing instructions / auto-pack | — (WHS packing policies w31) | POP1/POP2/POP3 packing instructions, POF1 determination, EWM packaging specifications | packing_rule (product → container type, qty per level) | full (EWM-level rules w31) |
| Returnable transport packaging | — | RTP special stock M/V, MB58 | returnable_packaging_balance per customer | partial (balances; charging w30 credit/debit) |

### 5.28.3 Functional requirements
- **FR-28.1 Release rules** — Must. Release confirmed, unheld lines by rule (confirmed ship date ≤ today + horizon, default 2 d; full ATP; priority) into one shipment per (order, warehouse, ship date) unless consolidation is enabled.
- **FR-28.2 Picking** — Must. Pick work by wave (≤ 200 lines or 50 orders) or batch; physical reservation (w16); confirm quantity, batch/serial and location; short picks create a remainder and release the unpicked reservation.
- **FR-28.3 Packing** — Must. Containers (type, tare, dims), nesting pallet → carton, SSCC from the GS1 company prefix with mod-10 check digit.
- **FR-28.4 Packing slip posting** — Must. Record delivered quantities, create inventory issues, post the journal per BR-28.1 at the current cost snapshot, atomically via a Temporal saga; sequence `PS-`.
- **FR-28.5 Cancellation** — Must. Reverse an uninvoiced packing slip with mirror inventory and journal entries referencing the original.
- **FR-28.6 Carrier abstraction** — Must. rate / label / manifest / track through a provider-agnostic contract with EasyPost-style, Shippo-style and `file` adapters; labels as PDF and ZPL; tracking by 30-min poll or webhook.
- **FR-28.7 Drop-ship** — Must. Linked PO (w17) for drop-ship lines; the vendor's ship confirmation triggers the packing slip; COGS at vendor cost without inventory.
- **FR-28.8 Partial shipments** — Must. Ship each schedule line separately; order stays PARTIALLY_SHIPPED until remaining_qty = 0.
- **FR-28.9 ASN** — Should. Emit an ASN with containers, SSCCs and quantities on post for the wave 35 EDI mapper.
- **FR-28.10 Tracking visibility** — Should. Publish tracking changes to portal and order detail.
- **FR-28.11 Mobile pick (basic PWA)** — Should. Pick confirmation with GS1-128 parsing (AI 00, 01, 10, 17, 21, 30, 37) and 5-minute offline tolerance.
- **FR-28.12 DNI reconciliation** — Must. delivered_not_invoiced projection = GL 1405, checked daily.
- **FR-28.13 Picking workbench** — Should. The system shall provide a picking workbench that filters released shipment lines (customer, carrier, ship date range, product group, zone, order priority), groups them by a selectable key (customer, carrier, zone, product) into waves of ≤ `WAVE_MAX_LINES`, previews the grouping and creates pick work in one action. Notes: AX sales picking workbench (R3) semantics; saved filters per user; a line belongs to at most one open wave.
- **FR-28.14 Packaging materials and packaging fees** — Should. The system shall define packaging materials (material code, type, weight, recyclable flag, unit) assigned to products per unit (w8 packaging units) and to container types, compute packaged weight per shipment and post packaging fees (EU packaging duty: fee per kg per material and country) as a statistical line on the packing slip and a FREIGHT/SURCHARGE condition for w29 when `packaging_fee_billable`. Notes: packaging report per period per country (§5.28.14); fee tables are country-pack data (w48).
- **FR-28.15 Packing instructions** — Should. The system shall hold packing rules (product or product group × customer optional → container type, quantity per container, nesting level, max weight) and auto-pack picked lines into containers at the packing station with a one-click accept; manual repack remains available. Notes: SAP POP1/POF1 and EWM packaging specifications; rule determination specific → general like BR-26.1.
- **FR-28.16 Returnable transport packaging** — Could. The system shall track returnable packaging (pallets, crates) shipped to and returned from customers as a balance per (customer, packaging material) from packing slips and return receipts, with an aging inquiry and a hook for w30 to charge unreturned items after `rtp_charge_days`. Notes: SAP special stock M/V; no inventory valuation movement; balances reconciled to Σ container counts.

### 5.28.4 Data model

**shipment**

| Field | Type | Required | Notes |
|---|---|---|---|
| id, shipment_number | uuid, text | Y | SH- sequence |
| legal_entity_id, warehouse_id | uuid | Y | one warehouse per shipment |
| customer_id, delivery_address_id | uuid | Y | |
| status | enum | Y | BR-28.6 |
| planned_ship_date, actual_ship_date | date, timestamptz | Y/N | |
| carrier_code, service_level, mode_of_delivery | text | N | |
| lines[] | shipment_line | Y | order_line_id, schedule_line_id, qty_planned/picked/packed/shipped, dims |
| consolidation_key, packing_slip_id, carrier_shipment_id, version | text, uuid, uuid, int | N | |

**container**: id, shipment_id, parent_container_id, container_type, sscc char(18), tare_weight, gross_weight, dimensions, uom, lines[] (shipment_line_id, qty, batch, serials[]), label_ref, status (OPEN | CLOSED | SHIPPED).

**packing_slip** (CustPackingSlipJour/Trans): id, packing_slip_number, shipment_id, order_id, posting_date, posted_at, status (PENDING | POSTED | CANCELLED | INVOICED), lines[] (order_line_id, qty, unit, cost_amount, cost_snapshot_id, inventory_transaction_ids[]), total_cost, journal_posting_id, reversal_of_id, document_ref.

**carrier_shipment**: id, shipment_id, provider, provider_ref, rate_quote, tracking_number, label_ref, status (QUOTED | LABELLED | MANIFESTED | IN_TRANSIT | DELIVERED | EXCEPTION), events[], cost_amount, currency.

Invariants: Σ container qty per line = qty_packed ≤ qty_picked ≤ qty_planned; packing slip qty = qty_shipped; one journal posting per slip plus one reversal if cancelled; SSCC check digit valid and unique per tenant; Σ cost of uninvoiced slips = GL 1405. Indexing: shipments (warehouse, status, planned_ship_date); packing slips (order_id), (posting_date), partitioned by month; containers unique sscc.

### 5.28.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/fulfillment/releases:run | Run release rules for a warehouse or orders | Idempotency-Key | WarehouseRelease |
| POST | /api/v1/fulfillment/waves, …/{id}:confirm-pick | Create wave; confirm picks | Idempotency-Key | WarehousePick |
| POST | /api/v1/fulfillment/shipments/{id}/containers, …/{cid}:close | Pack; close container (SSCC) | Idempotency-Key | WarehousePack |
| POST | /api/v1/fulfillment/shipments/{id}:rate, :label, :manifest | Carrier operations | Idempotency-Key | WarehouseShip |
| POST | /api/v1/fulfillment/shipments/{id}:post-packing-slip; /packing-slips/{id}:cancel | Post / reverse (saga) | Idempotency-Key | PackingSlipPost |
| GET | /api/v1/fulfillment/shipments/{id}/tracking | Tracking events | — | ShipmentView / Portal |
| POST | /api/v1/fulfillment/drop-ship/{orderLineId}:vendor-confirm | Vendor confirmation → packing slip | Idempotency-Key | PurchasingConfirm |
| POST | /webhooks/carriers/{provider} | Tracking webhook (HMAC) | provider event id | system |

Validation: post requires status PACKED, no ACTIVE hold, posting date in an open inventory period (w14), and no negative on-hand unless the product policy allows (w16).

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.fulfillment.shipment.released.v1 | release | shipment_id, order_id, warehouse, lines | sales, analytics |
| dev.passage.fulfillment.pick.confirmed.v1 | pick confirm | wave_id, lines[{order_line_id, qty, dims, location}] | inventory, sales |
| passage.fulfillment.packing-slip.posted.v1 / cancelled.v1 | saga complete / cancel | packing_slip_id, order_id, lines[{order_line_id, qty, cost_amount, cost_snapshot_id}], journal_posting_id, posting_date | billing, sales, inventory, ledger, portal, credit |
| dev.passage.fulfillment.asn.issued.v1 | post | shipment_id, containers[{sscc, lines}], carrier, tracking | EDI (w35) |
| passage.fulfillment.shipment.tracking-updated.v1 | poll / webhook | shipment_id, tracking_number, status, event | portal, sales |

### 5.28.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Fulfillment workspace | tiles: to release, picking, packed not shipped, carrier exceptions, DNI balance | counts | WarehouseView |
| Release board | run rules, release selected, hold | order, customer, ship date, ATP status, priority | WarehouseRelease |
| Wave / pick list | print, confirm, short pick, reassign | line, product, location, qty, dims, picker | WarehousePick |
| Packing station | scan line, add/close container, print label | container, type, weights, lines, SSCC | WarehousePack |
| Shipment detail | rate, label, manifest, post packing slip, cancel | carrier, service, cost, tracking, journal preview | WarehouseShip / PackingSlipPost |
| Mobile pick (PWA) / carrier exceptions | scan, confirm, skip; retry, switch provider, manual tracking | location, product, qty, batch/serial; provider error, age | WarehousePick / WarehouseShip |

Interaction rules: scan field keeps focus; Enter confirms a line; GS1-128 auto-fills batch/serial; bulk release on multi-select; posting shows the journal preview before confirm; layouts per warehouse personalizable.

### 5.28.7 Business rules and edge cases
- **BR-28.1 Packing-slip COGS posting matrix** (accounts from the wave 16 posting profile; dimensions from the order line):

| Scenario | At packing slip post | At invoice post (wave 29) |
|---|---|---|
| Stocked item, physical posting enabled (default, AX-style) | Dr 1405 Delivered-not-invoiced / Cr 1400 Inventory (physical) at cost snapshot | Reverse 1405/1400; Dr 5000 COGS / Cr 1400 Inventory (financial) at settled cost |
| Stocked item, policy `cogs_at_shipment` (SAP PGI-style) | Dr 5000 COGS / Cr 1400 Inventory | Revenue and AR only |
| Standard-cost item | Same, at standard; no variance at shipment | Same |
| Drop-ship line | No posting | Dr 5000 COGS / Cr 2110 GR/IR at PO cost when vendor confirmation posts |
| Non-stocked / service line | No posting | Revenue only |
| Catch-weight item | Cost on inventory quantity, not catch-weight quantity | Same |
| Cancellation | Mirror entries, negative amounts, same dims, reversal_of set | — |

- **BR-28.2 Cost snapshot** = running average or FIFO estimate (w20) at posting, stored as cost_snapshot_id; inventory close adjusts 5000/1400, never 1405 after invoicing.
- **BR-28.3 Posting date** = actual ship timestamp in the warehouse time zone; closed inventory period → `PERIOD_CLOSED`, shipment stays PACKED.
- **BR-28.4 Saga.** (1) packing slip PENDING; (2) inventory issues; (3) journal posting with Idempotency-Key = packing_slip_id; (4) POSTED + event. Failure at 3 compensates 2; a retried 3 is a ledger no-op. PENDING > 10 min alerts.
- **BR-28.5 SSCC** = extension digit + GS1 company prefix + serial reference padded to 17 digits + mod-10 check (weights 3,1 from the right); serial from a gap-free sequence per prefix; no reuse for 12 months.
- **BR-28.6 Shipment state machine.** OPEN → RELEASED → PICKING → PICKED → PACKED → SHIPPED → DELIVERED; CANCELLED before SHIPPED (releases reservations); SHIPPED → PACKED on packing-slip cancellation.
- **BR-28.7 Over-pick** rejected; over-ship rejected unless the customer's overdelivery_pct allows.
- **BR-28.8 Consolidation** requires matching customer, address, warehouse, planned date and carrier and no `ship_separately` order.
- **BR-28.9 Carrier failures** retry 3× (2 s, 8 s, 32 s) then land in the exceptions queue; the slip may still post with a manual tracking number.
- **BR-28.10 Tracked items** must carry batch/serial at pick; the slip rejects lines missing tracking dims (w8 tracking dimension group).
- **BR-28.11 Auto-pack** fills containers in rule order (most specific rule first), closes a container when quantity per container or max weight is reached, and never splits a serial-tracked unit; lines without a rule stay unpacked and are flagged.
- **BR-28.12 Packaging fee** = Σ (material weight per unit × shipped units) per material × country rate as of the packing slip date, rounded half-even to minor units per material; fees post statistically (no journal) unless `packaging_fee_billable`, in which case w29 bills them as a SURCHARGE line with its own tax code.
- **BR-28.13 Returnable packaging balance** increases on packing-slip post (containers flagged returnable) and decreases on return receipt (w30 RMA line or standalone return); a negative balance is rejected with `RTP_NEGATIVE`.
- **BR-28.14 Workbench grouping** keeps lines of one shipment in one wave; a wave exceeding `WAVE_MAX_LINES` is split by the group key; preview shows lines per wave before creation.

### 5.28.8 Acceptance criteria
- **AC-28.1** (FR-28.4) Given 10 units at cost 12.50 USD with physical posting, when the slip posts, then on-hand −10, journal Dr 1405 125.00 / Cr 1400 125.00, and the event carries cost_amount 125.00.
- **AC-28.2** (FR-28.5) Given that slip cancelled before invoicing, then on-hand +10, a mirror posting exists and GL 1405 returns to its prior balance.
- **AC-28.3** (FR-28.3) Given prefix 0614141 and serial 0123456789, then SSCC = 0061414101234567891 and the GS1-128 label scans back to it.
- **AC-28.4** (FR-28.2) Given a 100-unit line with 90 picked, then qty_picked = 90, 10 remain on backorder and their reservation is released.
- **AC-28.5** (FR-28.7) Given a drop-ship line, when the vendor confirms, then a slip posts with no inventory transaction and Dr 5000 / Cr 2110 at PO cost.
- **AC-28.6** (FR-28.6) Given recorded fixtures for both adapters, when rate/label/track run, then responses match the contract and labels are valid PDF and ZPL.
- **AC-28.7** (FR-28.1) Given 1,000 confirmed orders, when release → pick → pack → post runs, then all are SHIPPED and Σ inventory issues = Σ slip quantities with zero mismatch.
- **AC-28.8** (FR-28.12) Given 50 posted slips of which 20 invoiced, then DNI = cost of the 30 uninvoiced = GL 1405.
- **AC-28.9** (FR-28.11) Given scan `(01)00614141000012(10)LOT7(37)5`, then GTIN, batch LOT7 and qty 5 are filled.
- **AC-28.10** (FR-28.13) Given 60 released lines for carrier UPS across 12 customers, when grouped by customer with WAVE_MAX_LINES 20, then the preview shows waves of ≤ 20 lines, no shipment is split across waves, and one action creates all pick work.
- **AC-28.11** (FR-28.15) Given a rule "FG-100 → carton C12, 12 per carton, max 15 kg" and 30 picked units, when auto-pack runs, then containers of 12, 12 and 6 exist with valid SSCCs and gross weights.
- **AC-28.12** (FR-28.14) Given DMF-DE ships 100 units with 0.2 kg cardboard each at 0.50 EUR/kg, then the packing slip carries a statistical packaging fee of 10.00 EUR and the period packaging report shows 20 kg cardboard for DE.
- **AC-28.13** (FR-28.16) Given 10 returnable pallets shipped to C1 and 4 received back, then the C1 pallet balance is 6 and a return of 7 is rejected with `RTP_NEGATIVE`.

### 5.28.9 Test plan and fixtures
Fixtures: `warehouse-sim-1k` (1,000 orders, 120 products, 3 warehouses, batch/serial mix); `sscc-200` (GS1 examples, valid/invalid); `carrier-recorded` (HTTP recordings); `dropship-30`; `cogs-matrix-14` (per cell × costing method, accountant-reviewed).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Posting matrix | contract (ledger) | post / cancel | 14/14 balanced; cancel mirrors exactly |
| Saga compensation | integration (Temporal) | injected ledger failure | no inventory movement remains; slip not POSTED |
| SSCC / GS1-128 | unit, property | check digit, parser | 200/200; all AI combinations round-trip |
| Pick-pack-ship 1k | E2E | simulated warehouse | 0 mismatch, all SHIPPED < 60 min |
| Carrier adapters | contract | recordings | green |
| DNI = GL 1405 | reconciliation | daily job | 0.00 |

Reconciliation: Σ inventory issue cost = Σ journal Cr 1400 from fulfillment; DNI projection = GL 1405; line delivered_qty = Σ slip qty.

### 5.28.10 Deployment and operations
Helm `passage-fulfillment` (Go), `passage-carrier-gateway` (TS; egress via Envoy, provider secrets in Vault), `fulfillment-db`; PWA served by the shell. Config: `RELEASE_HORIZON_DAYS=2`, `WAVE_MAX_LINES=200`, `GS1_COMPANY_PREFIX` per entity, `COGS_AT_SHIPMENT=false`. Flags: `fulfillment.consolidation`, `fulfillment.asn`, `fulfillment.mobile_pick`. Jobs: release every 15 min per warehouse; tracking poll 30 min; DNI check 02:00. SLOs: post p95 < 2 s, saga completion 99.9% within 60 s. Alerts: PENDING > 10 min, carrier error rate > 5%, DNI diff ≠ 0. Runbook: resume stuck saga, re-print label, manual tracking. Rollback: previous digest; Temporal workflow versioning lets in-flight sagas finish on old code.

### 5.28.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustPackingSlipJour/Trans with uninvoiced remainder | packing_slip, DNI opening balance | cost from InventTrans.CostAmountPhysical |
| WMSShipment, WMSPickingRoute (open); WHSContainerTable | shipment, pick_work, container | open picks reset to RELEASED; SSCC retained |
| LIKP/LIPS not goods-issued (WBSTK ≠ C) | shipment | A/B → RELEASED/PICKING |
| LIKP/LIPS goods-issued, not billed (FKSTK ≠ C) | packing_slip | cost from MSEG 601 DMBTR |
| VEKP/VEPO | container | EXIDV SSCC retained |

Validation: DNI opening balance = legacy delivered-not-invoiced GL balance to the cent; open shipment quantities = legacy open deliveries. Not migrated: completed shipments, tracking history, carrier configuration (manual).

### 5.28.12 Dependencies, risks, open questions
- Depends on: w27 orders and holds; w16 inventory transactions/reservations; w20 cost snapshots; w17 POs; w11 journal; w14 period control; w8 tracking dimensions and GS1 ids; w5 PWA shell and document rendering.
- Risks: physical posting unfamiliar to SAP users (policy flag plus documented matrix); carrier sandbox instability (recorded fixtures gate CI; live sandbox nightly only).
- Open questions: (1) Default `cogs_at_shipment` for migrated SAP entities? Default true; decide by wave 29. (2) ASN EDI mapping owned by wave 28 or 35? Default 35; decide at wave 28 close.

### 5.28.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| release_horizon_days | warehouse | int | 2 | 0–30 | FR-28.1 release window | AR parameters → Updates → "Release to warehouse" / release rules | VL10 delivery creation date horizon (VL10 profile) |
| release_requires_full_atp | warehouse | bool | true | true/false | Partial release allowed when false | Release to warehouse "Partial release" | OVLK delivery split / partial delivery |
| consolidation_enabled | legal entity | bool | false | true/false | BR-28.8 | Consolidated shipments (WHS) | OVLK combination rules (VL04 collective) |
| wave_max_lines / wave_max_orders | warehouse | int | 200 / 50 | 1–2,000 / 1–500 | FR-28.2, FR-28.13 | WHS wave template thresholds | LT03 group limits / EWM wave template |
| short_pick_action | warehouse | enum | REMAINDER_BACKORDER | REMAINDER_BACKORDER, CANCEL_REMAINDER | FR-28.2 | Picking list registration "Reduce quantity" | VL02N picking quantity < delivery qty handling |
| gs1_company_prefix / sscc_extension_digit | legal entity | text / int | — / 0 | 6–12 digits / 0–9 | BR-28.5 | WHS license plate number sequence (GS1) | HU number range (OVLS SSCC) |
| sscc_no_reuse_months | tenant | int | 12 | 12–60 | BR-28.5 | n/a | n/a |
| cogs_at_shipment | legal entity | bool | false (true recommended for migrated SAP entities) | true/false | BR-28.1 matrix | AR parameters → Updates → "Post packing slip in ledger" (physical posting) | OBYC GBB-VAX at PGI (always) |
| posting_date_source | warehouse | enum | SHIP_TIMESTAMP | SHIP_TIMESTAMP, TODAY, MANUAL | BR-28.3 | Packing slip posting date default | VL02N actual GI date |
| allow_negative_onhand_on_post | product policy (w16) | bool | false | true/false | §5.28.5 validation | InventModelGroup "Physical negative inventory" | MM negative stock allowed (OMJ1) |
| carrier_default / service_level_default | customer (w7), warehouse | text | — | configured carriers | FR-28.6 | CustTable DlvMode / ShipCarrier defaults | KNVV-VSBED shipping conditions → route |
| carrier_retry_schedule | tenant | text | 2s,8s,32s | list | BR-28.9 | n/a | n/a |
| tracking_poll_minutes | tenant | int | 30 | 5–240 | FR-28.6 | n/a | n/a |
| asn_enabled | legal entity | bool | false | true/false | FR-28.9 | AIF DESADV outbound | DESADV output type LAVA |
| mobile_offline_tolerance_minutes | tenant | int | 5 | 0–30 | FR-28.11 | n/a | n/a |
| packaging_fee_billable | legal entity | bool | false | true/false | BR-28.12 | Packing material fees "Charge customer" | Packaging material pricing condition (KP00) |
| packaging_fee_rates | country (w48 pack) | table | — | material × rate per kg | FR-28.14 | Packing material fees setup (EU) | n/a (country add-on) |
| auto_pack_enabled | warehouse | bool | true | true/false | FR-28.15 | n/a | OVLS packing control per item category (auto-pack) |
| rtp_tracking_enabled / rtp_charge_days | legal entity | bool / int | false / 90 | — / 30–365 | FR-28.16 | n/a | RTP special stock M/V |
| workbench_group_key_default | warehouse | enum | CUSTOMER | CUSTOMER, CARRIER, ZONE, PRODUCT | FR-28.13 | Picking workbench grouping | VL10 list variant |
| dni_check_time | legal entity | time | 02:00 | — | FR-28.12 job | n/a (manual InventPosting report) | n/a (F.03) |

### 5.28.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Shipments list / delivery monitor | list inquiry | warehouse, status, planned date, carrier, customer | shipment, order, customer, status, planned/actual ship, carrier, tracking | shipment | Shipments list page (WMSShipment) | VL06O / VL06F outbound delivery monitor |
| To release | list inquiry | warehouse, horizon, ATP status, hold | order, line, confirmed date, ATP, priority, hold | sales lines (confirmed, unheld) | Release to warehouse form | VL10 / VL10A delivery due list |
| Pick work / waves | list inquiry | wave, picker, zone, status | wave, lines, picked/short, picker, age | pick_work | Picking list registration (WMSPickingRoute) | LT22 / LT23 transfer order lists; VL06P |
| Packed not shipped | list inquiry | warehouse, age | shipment, containers, weight, carrier, label status | shipment (PACKED) | Shipments awaiting ship confirm | VL06G (for goods issue) |
| Packing slip journal | list inquiry / drill-down | order, customer, date range, status | slip, order, lines, qty, cost, journal ref, reversal | packing_slip | Packing slip journal (CustPackingSlipJour) | VL03N document flow; MB51 movement 601 |
| Delivered not invoiced | operational report | legal entity, as-of, customer | slip, line, qty, cost, days since shipment, GL 1405 diff | DNI projection | Cost of units delivered (InventPosting) / Physical inventory by inventory dimension | VF04 due list value; F.03 recon |
| Carrier exceptions and cost | list inquiry / KPI | provider, error, age; cost per shipment | shipment, error, retries, cost quoted vs billed | carrier_shipment | Shipping carrier interface log | XSI / TM logs (VT02N) |
| SSCC register | list inquiry | prefix, date range, customer | sscc, container, shipment, printed_at, reused flag | container | License plate list | HU monitor HUMO |
| Short picks and backorders created | operational report | warehouse, period | line, planned, picked, remainder, reason | pick_work | Picking list registration log | VL06P picking variance |
| Packaging weights per material | statutory report (country pack) | legal entity, country, period | material, kg shipped, fee | packaging_fee lines | Packing material fees report | n/a (DE LUCID / FR CITEO exports via w48) |
| Returnable packaging balances | list inquiry | customer, material, age | balance, last shipped, last returned, days outstanding | returnable_packaging_balance | n/a | MB58 |
| Picking workbench preview | drill-down | filter set, group key | wave, lines, orders, weight | pick_workbench | Sales picking workbench | VL10 grouped selection |

### 5.28.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Pick list | wave creation / print | header: wave, warehouse, picker, zone; lines: product, dims, location, qty, barcode; footer: counts | PDF, ZPL (label printer), PWA | wave number `WV-` | none; keep 1 year |
| Packing slip / delivery note | packing-slip post | header: slip number, order, customer, ship-to, carrier, tracking, date; lines: product, description, qty, unit, batch/serial; footer: container summary, SSCCs, signature block | PDF, email, EDI DESADV/856 (w35), portal | `PS-` per legal entity, gap-free | commercial document; retain 7 years (10 in DE) with the invoice |
| GS1-128 container label | container close | SSCC (AI 00), GTIN (01), batch (10), qty (37), ship-to postal (420), order ref | ZPL, PDF | SSCC serial per prefix | GS1 General Specifications; no retention beyond SSCC register |
| Carrier label | :label | carrier-rendered label with tracking barcode | PDF, ZPL (provider payload) | provider tracking number | retain label_ref 1 year |
| Manifest / end-of-day close | :manifest | carrier, pickup date, package count, weights, tracking list | PDF, provider API | provider manifest id | retain 1 year |
| ASN | packing-slip post (asn_enabled) | shipment, containers with SSCC hierarchy, lines, quantities, carrier, tracking | JSON event; EDI 856 / DESADV through w35 | shipment number | trading-partner agreement; retain 7 years |
| Bill of lading | shipment with `bol_required` | shipper, consignee, carrier, freight terms, commodity lines, weights | PDF (basic; full BOL w34) | `BOL-` | US carriage record; retain 3 years |
| Packaging fee statement | period close per country | material, kg, rate, fee | PDF, CSV | none | per country pack (w48) |

### 5.28.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Release to warehouse | every 15 min per warehouse | warehouse | key = (warehouse, line, confirmed version) | < 1 min per 10k lines | retry; lines stay confirmed | lines eligible but unreleased > 1 h |
| Tracking poll | every 30 min | provider | last event timestamp per shipment | < 5 min | provider error → exceptions queue | poll failures > 5% |
| DNI reconciliation | 02:00 daily | legal entity | key = (entity, date) | < 5 min | rerun; diff report | diff ≠ 0.00 |
| Saga watchdog | every 5 min | — | PENDING slips | < 10 s | resume or compensate | PENDING > 10 min |
| Manifest / end-of-day close | per warehouse cutoff (default 17:00 local) | warehouse × carrier | manifest id per (warehouse, carrier, date) | < 2 min | retry; manual manifest | shipments LABELLED but unmanifested after cutoff |
| SSCC sequence check | nightly 00:30 | prefix | — | < 1 min | alert only | duplicates or check-digit failures > 0 |
| Packaging fee accrual report | first day of month | country | key = (country, period) | < 2 min | rerun | missing rate for material |
| Returnable packaging aging | nightly 03:00 | customer | key = (customer, date) | < 1 min | rerun | balances outstanding > rtp_charge_days |
| Label and document cleanup | weekly | — | — | < 10 min | skip | storage growth > plan |

### 5.28.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| WarehouseView | GET shipments, tracking, DNI; Fulfillment workspace | none | Warehouse worker, Customer service, Finance |
| WarehouseRelease | releases:run; Release board; Picking workbench | none | Warehouse supervisor |
| WarehousePick | waves, :confirm-pick; Pick list, Mobile pick | with PackingSlipPost when `pick_post_separation` | Picker |
| WarehousePack | containers, :close; Packing station; auto-pack | none | Packer |
| WarehouseShip | :rate, :label, :manifest; Carrier exceptions | none | Shipping clerk |
| PackingSlipPost | :post-packing-slip, /packing-slips/{id}:cancel | with SalesOrderMaintain on the same order; with InvoicePost (w29) when four-eyes | Shipping clerk (post), Warehouse supervisor (cancel) |
| PackingRuleMaintain | packing rules, container types, packaging materials | none | Warehouse admin |
| CarrierAdmin | provider configuration, secrets rotation (Vault reference only) | with WarehouseShip (logged) | Logistics admin |
| PackagingFeeMaintain | packaging fee rates (country), statements | with InvoiceMaintain (w29) | Compliance / Finance |
| ReturnablePackagingMaintain | RTP balances adjustments | with WarehouseReceive | Logistics admin |

### 5.28.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| FUL-0001 | error | Shipment must be PACKED with no active hold to post | status or hold | Pack lines; release hold | §5.28.5 validation |
| FUL-0002 | error | Inventory period {period} is closed for posting date {date} | BR-28.3 | Use an open period or reopen (w14) | BR-28.3 |
| FUL-0003 | error | Negative on-hand for {product} at {warehouse}/{location} | w16 policy | Adjust stock or allow negative | §5.28.5 validation |
| FUL-0004 | error | Over-pick: {qty} exceeds planned {planned} | BR-28.7 | Reduce quantity | BR-28.7 |
| FUL-0005 | error | Over-ship exceeds customer tolerance {pct}% | BR-28.7 | Reduce or raise tolerance | BR-28.7 |
| FUL-0006 | error | Batch or serial required for {product} | BR-28.10 | Scan tracking dims | BR-28.10 |
| FUL-0007 | error | SSCC {sscc} failed check digit or already used | BR-28.5 | Regenerate label | BR-28.5 |
| FUL-0008 | warning | Carrier {provider} failed after 3 retries; shipment moved to exceptions | BR-28.9 | Retry, switch provider or enter manual tracking | BR-28.9 |
| FUL-0009 | error | Packing slip PENDING for {minutes} min; saga stalled | BR-28.4 | Runbook: resume stuck saga | BR-28.4 |
| FUL-0010 | error | Cannot cancel: packing slip {id} is invoiced | FR-28.5 | Credit note (w29) then cancel | FR-28.5 |
| FUL-0011 | error | Consolidation mismatch: {field} differs | BR-28.8 | Ship separately | BR-28.8 |
| FUL-0012 | warning | No packing rule for {product}; line left unpacked | BR-28.11 | Pack manually or add a rule | FR-28.15 |
| FUL-0013 | error | Container exceeds max weight {max} kg | BR-28.11 | Close container and start a new one | BR-28.11 |
| FUL-0014 | error | Packaging fee rate missing for material {material} in {country} | BR-28.12 | Load the country rate table | FR-28.14 |
| FUL-0015 | error | Returnable packaging balance would become negative for {customer} | BR-28.13 | Correct the return quantity | FR-28.16 |
| FUL-0016 | info | Workbench created {n} waves from {lines} lines | FR-28.13 | none | BR-28.14 |
| FUL-0017 | warning | GS1-128 scan could not be parsed: {raw} | FR-28.11 | Rescan or enter manually | FR-28.11 |

### 5.28.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Release run (10k lines) | 96 runs per warehouse per day | — / 60 s / 120 s | 200 lines/s | shipments 5M × 2 KB = 10 GB | shipments (warehouse, status, planned_ship_date) | runs queue per warehouse; next run skipped if previous running |
| Pick confirm (line) | 500k per day; peak 50/s | 50 / 150 / 400 ms | 100/s | pick_work 3 GB | (wave, status) | PWA queues offline up to 5 min |
| Container close (SSCC + label) | 200k per day | 100 / 300 / 800 ms | 50/s | containers 2 GB; labels object store 20 GB | unique sscc | label rendering async; SSCC issued synchronously |
| Packing-slip post (saga) | 100k per day; peak 20/s | 600 ms / 2 s / 4 s | 20/s per replica | slips 10M × 1 KB + journal refs | partition by month; (order_id) | saga workers scale; slips remain PENDING ≤ 60 s p99 under 3× |
| Carrier rate / label | 100k per day | 400 ms / 2 s / 5 s (provider-bound) | provider limit | carrier_shipment 3 GB | (provider, status) | circuit breaker per provider; exceptions queue |
| Tracking events | 1M per day | 5 / 30 / 60 s lag | 50 events/s | 10 GB (events jsonb, 1-year retention) | (shipment_id, event_time) | poll interval doubles; webhooks unaffected |
| DNI reconciliation | 1 per entity per day | — / 5 min / 10 min | — | none | DNI projection (slip, status) | run moved to low-traffic window |
| Auto-pack (100 lines) | 50k per day | 100 / 400 / 1,000 ms | 20/s | none | packing_rule (product, customer) | manual pack fallback |

### 5.28.20 Localization and compliance hooks

- Packaging fees and extended-producer-responsibility reporting (DE VerpackG/LUCID, FR CITEO, ES RAP) are country-pack data and exports (w48) on top of FR-28.14; the rate table and the period statement are the extension points.
- Delivery note legal content: DE and IT require sequential delivery-note numbering and specific fields (IT DDT reason for transport); the `PS-` sequence is gap-free per entity and the docgen template exposes a `country_fields` slot.
- GS1 prefixes are issued per country member organization; the entity holds one or more prefixes, each with its own gap-free serial.
- Dangerous goods, export declarations (EU AES / US AES) and sanctioned-party screening are explicitly out of scope and arrive through w33/w34/w48 consumers of `shipment.released`.
- Weights and dimensions store SI units; US entities display lb/in through the w6 unit conversion with half-even rounding at 2 decimals.
- Time zones: posting date derives from the warehouse time zone (BR-28.3); carrier cutoffs are warehouse-local.
- Privacy: ship-to contact names and phone numbers on labels are personal data; label artifacts purge after 1 year (w48 retention policy).

### 5.28.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.14.4 #140 — Picking routes setup / Sales picking workbench (R3) | PARTIAL-28 | ADDED as FR-28.13 | grouped wave creation with preview |
| AX §12.15.1 #145 — Product → Packaging (packaging materials, PI container packaging) | PARTIAL-28 | ADDED as FR-28.14 | packaging_material on product units |
| AX §12.15.4 #149 — Packaging units / Packing material fees setup (EU) | PARTIAL-28 | ADDED as FR-28.14 | rates per country pack |
| AX §12.23.1 #200 — Container packaging (PI container types) | PARTIAL-28 | ADDED as FR-28.14 / FR-28.15 | container_type with packaging material |
| AX §12.23.4 #210 — Container types, Packaging groups | PARTIAL-28 | ADDED as FR-28.15 | packaging group = rule scope |
| SAP §13.17 #207 — OVLS/OVLV packing instructions (POP1/POP2/POP3/POF1), packaging material types, HU number ranges | PARTIAL-28 | ADDED as FR-28.15 (packing instructions); SSCC ranges already FR-28.3 | |
| SAP §13.16 #188 — EWM packaging specifications (/SCWM/PACKSPEC) (PARTIAL-31, note proposes 28/31) | PARTIAL-31 | ADDED as FR-28.15 (basic rules); multi-level EWM specifications DEFERRED to wave 31 | |
| SAP §13.13 #147 — Returnable transport packaging (special stock M/V) | GAP (propose 28) | ADDED as FR-28.16 | balances only; charging via wave 30 |

## 5.29 Wave 29: Billing, Invoicing & Revenue Recognition

### 5.29.1 Scope and business context
Wave 29 ships the `billing` and `revrec` services. Posted packing slips and eligible order lines become invoices (one-off, consolidated, subscription, milestone); free-text invoices and credit notes post directly; layouts render to PDF with an e-invoice document for wave 35. Every posted invoice creates AR open items, tax postings (w13) and journal entries and, where the contract has multiple performance obligations or deferred terms, a revenue contract with standalone selling price allocation and recognition schedules. Out of scope: cash application (wave 30), intercompany invoicing (hook only; wave 40), project billing (wave 41), Peppol transmission (wave 35), metering sources beyond API-posted usage. Added from the coverage registers (§5.29.21): subscription groups, fee categories and indexation; expense deferrals; customer self-billing; bundle (solution) lines; installment terms at post; document delivery routing.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Bundle allocation lives in spreadsheets: D365 RevRecSchedule needs revenue prices per item and cannot explain allocation; SAP RAR (FARR_D_*) is a separate engine with its own posting run and FARR_RECON reconciliation | Rev rec bolted on after billing; allocation inputs not on the order | Revenue contract created at post from order lines with SSP order BR-29.2; allocation and schedules visible on the contract and in the invoice trace | Close-time rev rec effort 2–3 days → < 2 h review; 40/40 fixtures reproducible |
| Invoice totals differ from the order by cents (SalesLine rounding per form letter; MWST vs. BSEG rounding) | Each document re-rounds | Engine net reused; per-line tax; totals property-tested | Cent discrepancies ~0.5% of invoices → 0 |
| Billing is batch: VF04 due list and AX summary update run nightly | Scheduled jobs over document-flow tables | `packing-slip.posted` creates the proposal at once; post p95 < 3 s; consolidation is policy, not batch | Shipment-to-invoice 12–24 h → < 5 min for non-consolidated accounts |
| Deferred revenue roll-forward rebuilt by hand from GL 2400 and exports | No system roll-forward that ties to GL | `GET /revrec/roll-forward` reconciled nightly to GL 2400 with drill-down | Preparation 1 day → 0; nightly diff 0.00 |
| Credit note on a partially recognized invoice needs manual journals (manual schedule edits; RAR contract change) | Credit notes ignore recognition state | BR-29.6 reverses recognized and deferred portions automatically | Manual rev rec journals per month 10–30 → 0 |

### 5.29.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Invoice from shipment | SalesFormLetter_Invoice, CustInvoiceJour/CustInvoiceTrans, summary update | VF01/VF04, VBRK/VBRP, copy control VTFL | invoice (SHIPMENT, CONSOLIDATED) | full |
| Free-text invoice | CustInvoiceTable/CustInvoiceLine | FB70; order-related billing (DR) | invoice type FREE_TEXT | full |
| Credit note | Credit note (negative CustInvoiceJour), free-text credit | Credit memo request G2 → VF01, VBRK VBTYP O | credit_note | full |
| Billing plans / subscriptions | D365 Subscription billing; milestone billing | FPLA/FPLT billing plans; BRIM | subscription, milestone_schedule | partial (usage basic) |
| AR open item creation | CustTrans / CustTransOpen | BSID / BSAD, BKPF/BSEG | receivables.open_item via invoice.posted | full |
| Tax | TaxTrans, TaxGroup/TaxItemGroup | FTXP, MWST, BSET | tax engine (w13) at post | full |
| Revenue recognition | RevRecSchedule, RevRecContract, revenue prices (D365) | RAR: FARR_D_CONTRACT / POBS / DEFITEM | revenue_contract, performance_obligation, revenue_schedule | full |
| Deferred revenue postings | Deferred revenue posting on item; schedule processing | RAR posting run, contract liability | recognition run to 2400/4000 | full |
| Layout / e-invoice | SSRS SalesInvoice; Electronic reporting | Smart Forms/Adobe Forms; DRC | docgen template + UBL 2.1 document | partial (transmission w35) |
| Audit trace | CustInvoiceTrans.InventTransId | Document flow VBFA | GET /invoices/{id}/trace | improved |
| Service subscriptions (AX) | SMASubscriptionTable, subscription groups, fee categories, index rules, subscription periods, fee transactions | Contract billing plans (VF04 periodic), price adjustment via conditions | subscription groups, fee categories, index rules, period templates on `subscription` | full |
| Expense deferrals | D365 Subscription billing deferral schedules (expense), RED DeferralSchedule | Prepaid expense accruals (ACCR / FBS1 recurring) | expense_deferral schedule from w19 AP invoice lines | full |
| Customer self-billing | — | VSB1 / VKA self-billing (GSVERF, EDI 820) | self_billing_statement reconciliation | full (EDI mapping w35) |
| Solution / bundle orders | Bundle product (kit) on SalesLine | Solution order (bundles), item category group for bundles | bundle line explosion with mixed obligations (FR-29.17) | full |
| Installment terms | Payment schedule (CustPaymSched) on invoice | OBB9 installment terms (T052R) | installment split at post → N open items | full (terms definition w7) |
| Document routing | Print management (sales documents), form notes | NAST output determination (VV31), medium per partner | document_delivery_preference per customer × document | partial (conditional routing w46) |

### 5.29.3 Functional requirements
- **FR-29.1 Invoice from shipment** — Must. Proposals from posted packing slips (and order lines for non-stocked items), one invoice per order by default, lines carrying qty, price, discounts, charges and tax codes. Notes: gap-free `INV-` per entity and fiscal year; invoice date ≥ packing slip date.
- **FR-29.2 Consolidated invoices** — Must. Combine slips for the same invoice account, currency, terms and cycle (daily/weekly/monthly).
- **FR-29.3 Posting** — Must. Atomic: journal (Dr 1200 gross; Cr 4000 per line account; Cr 2300 tax; Cr 2400 deferred obligations; COGS/DNI per BR-28.1), open item creation, `invoice.posted`. Totals = Σ line nets + charges + tax after per-currency rounding (property-tested).
- **FR-29.4 Free-text invoices** — Must. Account, dimensions, amount, tax code; recurring templates.
- **FR-29.5 Credit notes** — Must. Reference an original (whole or lines) or standalone; negative open item; reverse revenue, tax and recognition.
- **FR-29.6 Tax** — Must. Call the tax engine at proposal and post; reject post if the result moved > 0.01 without re-review; no silent zero tax when the engine is unavailable.
- **FR-29.7 Revenue contracts** — Must. Performance obligations per order (one per line or bundle templates per product), SSP (explicit, list, % of list, residual), proportional allocation, schedules.
- **FR-29.8 Recognition methods** — Must. POINT_IN_TIME (packing slip or invoice), RATABLE (daily or monthly), MILESTONE (manual/event), USAGE (posted usage); period-end run or event posts Dr 2400 / Cr 4000.
- **FR-29.9 Roll-forward** — Must. Opening, additions, recognized, adjustments, closing per period and contract, tied to GL 2400.
- **FR-29.10 Subscriptions** — Should. Monthly/quarterly/annual, in advance or arrears, daily proration on mid-period start, cancel and plan change, usage lines, daily billing job.
- **FR-29.11 Documents** — Must. PDF per entity and language; UBL 2.1 JSON for wave 35.
- **FR-29.12 Audit trace** — Should. invoice → packing slip → order line → price explain → schedules in one response.
- **FR-29.13 Intercompany hook** — Could. `invoice.posted` carries `intercompany=true` for intercompany parties (wave 40).
- **FR-29.14 Subscription groups, fee categories and indexation** — Should. The system shall extend `subscription` with subscription groups (defaults for cycle, terms, revenue account), fee categories per line (recurring, one-time, usage, index-linked), period templates (calendar month, 4-4-5, anniversary) and index rules (percentage or external index with effective date) that re-price subscription lines on the next bill with an indexation note on the invoice. Notes: AX SMA subscription semantics; an index run previews affected subscriptions; indexation history is kept per line; accrual of subscription revenue uses FR-29.8 RATABLE.
- **FR-29.15 Expense deferrals** — Should. The system shall create deferral schedules for prepaid expenses from w19 vendor invoice lines (or manual), with method STRAIGHT_LINE (daily or monthly) over a term, short-term / long-term split (within 12 months → 1300, beyond → 1310), and a monthly recognition run posting Dr expense account / Cr 1300; schedules are visible on the same roll-forward as revenue (FR-29.9) with `kind = EXPENSE`. Notes: D365 deferral schedule parity; a credit from the vendor reverses remaining schedule lines; roll-forward ties to GL 1300 + 1310.
- **FR-29.16 Customer self-billing** — Could. The system shall import a customer self-billing statement (lines: delivery or packing slip reference, quantity, unit price, amount, tax; EDI 820 / GSVERF via w35 or CSV) and reconcile it line by line against posted or proposed invoices: matching lines mark the invoice as self-billed; price differences within `self_billing_tolerance` post an adjustment credit/debit; other differences are listed for review. Notes: SAP VSB1 / VKA; the customer's document number is stored as `external_invoice_ref`; open items settle against the customer's remittance (w30) by that reference.
- **FR-29.17 Bundle (solution) lines** — Should. The system shall accept a bundle product on an order line (w8 kit/bundle definition) and at invoice proposal explode it into components with mixed revenue types (goods POINT_IN_TIME, service MILESTONE, subscription RATABLE) under one revenue contract (FR-29.7), printing the bundle as one line with components itemized beneath it when `print_components`. Notes: SAP solution order; allocation per BR-29.1; a component shipped later is invoiced when shipped while the contract already exists.
- **FR-29.18 Installment terms** — Should. The system shall, when the invoice's payment terms (w7) carry an installment plan (n installments with percentage and offset days, or fixed dates), create one open item per installment at post with the same invoice number and an installment suffix, each with its own due date and cash-discount date; dunning and aging operate per installment. Notes: SAP OBB9 / T052R and AX payment schedules; Σ installments = gross exactly with residual cents on the last installment; a credit note settles installments latest-first unless specified.
- **FR-29.19 Document delivery routing** — Should. The system shall hold a delivery preference per customer (and optionally per invoice account and document type: invoice, credit note, statement, confirmation, packing slip) with channel EMAIL, PORTAL, PRINT, EDI, NONE, recipients, copies and language, and route generated documents accordingly from the post saga with a delivery log and resend action. Notes: AX print management "per account" destinations and form notes; SAP NAST medium per partner; conditional query-based routing and designer-level overrides arrive with wave 46 and read the same preference entity.

### 5.29.4 Data model

**invoice** (CustInvoiceJour / VBRK)

| Field | Type | Required | Notes |
|---|---|---|---|
| id, invoice_number | uuid, text | Y | gap-free per entity + fiscal year |
| legal_entity_id, invoice_account_id, customer_id | uuid | Y | |
| invoice_type | enum | Y | SHIPMENT, CONSOLIDATED, FREE_TEXT, SUBSCRIPTION, MILESTONE, CREDIT_NOTE |
| status | enum | Y | PROPOSAL, POSTED, PARTIALLY_SETTLED, SETTLED, REVERSED |
| invoice_date, posting_date, due_date | date | Y | due from terms |
| currency, exchange_rate_id | char(3), uuid | Y | three-currency amounts (w12) |
| net_amount, charge_amount, tax_amount, gross_amount | numeric(19,4) | Y | gross = net + charges + tax |
| cash_discount_date, cash_discount_amount | date, numeric | N | |
| journal_posting_id, tax_document_id, original_invoice_id | uuid | Y/N | original for credit notes |
| document_ref, einvoice_ref | text | N | PDF, UBL |

**invoice_line** (CustInvoiceTrans / VBRP): id, invoice_id, line_no, order_line_id, packing_slip_line_id, product_id, description, qty, unit, unit_price, disc_pct_1/2, line_net, tax_code, tax_amount, revenue_account, dimensions, cost_amount, performance_obligation_id, price_explain_ref.

**revenue_contract**: id, order_id, customer_id, currency, transaction_price, allocation_method, status (OPEN | COMPLETE | CANCELLED). **performance_obligation**: id, contract_id, product_id | bundle_component, ssp_amount, ssp_source (EXPLICIT | LIST | PCT_OF_LIST | RESIDUAL), allocated_amount, method, term_start, term_end, satisfied_at, deferred_account 2400, revenue_account 4000. **revenue_schedule**: id, obligation_id, period_id, scheduled_date, amount, status (SCHEDULED | RECOGNIZED | REVERSED), journal_posting_id.

**subscription**: id, customer_id, order_id, plan, billing_cycle, bill_in_advance, start_date, end_date, next_bill_date, proration (DAILY), status (ACTIVE | PAUSED | CANCELLED), lines[] (product, qty, unit_price, usage_based, usage_unit).

Invariants: gross = Σ line_net + charges + tax exactly after per-line rounding; Σ allocated = transaction_price (residual cents to the largest obligation); Σ schedules per obligation = allocated_amount; POSTED invoices are immutable; per period opening + additions − recognized ± adjustments = closing = GL 2400. Indexing: invoices (invoice_account_id, status, due_date), unique number per (entity, fiscal_year), partitioned by posting month; schedules (period_id, status).

### 5.29.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| POST | /api/v1/billing/proposals | Build from slips / orders / subscriptions with consolidation | Idempotency-Key | InvoiceMaintain |
| POST | /api/v1/billing/invoices/{id}:post | Post (tax, journal, AR) | key = invoice id | InvoicePost |
| POST | /api/v1/billing/free-text-invoices, /credit-notes, …/{id}:post | Free-text and credit notes | Idempotency-Key | InvoiceMaintain / CreditNotePost |
| GET | /api/v1/billing/invoices/{id}/document?format=pdf\|ubl; …/trace | Documents; audit trace | — | InvoiceView / Portal |
| POST | /api/v1/revrec/contracts/{id}:allocate; /milestones/{ob}:satisfy | Allocation; milestones | Idempotency-Key | RevRecMaintain |
| POST/GET | /api/v1/revrec/recognition-runs; /roll-forward?period= | Period run; roll-forward | key = period | RevRecPost / RevRecView |
| POST | /api/v1/billing/subscriptions, …/{id}:change, :cancel; /usage | Subscriptions and usage | Idempotency-Key | SubscriptionMaintain |

Validation: post rejects closed AR/GL period, lines without revenue account, tax engine unavailable (503 `TAX_UNAVAILABLE`), credit beyond the original's remaining creditable amount.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.billing.invoice.posted.v1 | post | invoice_id, number, type, customer, invoice_account, currency, dates, lines[{order_line_id, product, qty, net, tax, revenue_account, obligation_id}], totals, journal_posting_id, intercompany | receivables, revrec, ledger, pricing, portal, sales |
| passage.billing.credit-note.posted.v1 | post | credit_note_id, original_invoice_id, lines, totals, journal_posting_id | receivables, revrec, ledger, portal |
| dev.passage.revrec.contract.allocated.v1 | allocate | contract_id, obligations[{id, ssp, allocated, method}] | analytics, audit |
| dev.passage.revrec.schedule.recognized.v1 | run / event | schedule_id, obligation_id, period_id, amount, journal_posting_id | ledger, analytics |
| dev.passage.billing.subscription.billed.v1 | daily job | subscription_id, invoice_id, period, proration | portal, analytics |

### 5.29.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| Billing workspace | tiles: ready to invoice, proposals, failed posts, deferred balance, subscriptions due | counts, amounts | InvoiceView |
| Billing due list | filter, select, propose, consolidate | customer, slips, amount, cycle | InvoiceMaintain |
| Invoice proposal / detail | edit charges, review tax, post, print, credit | lines, tax breakdown, totals, obligations | InvoicePost |
| Free-text invoice | add lines, template, post | account, dims, amount, tax code | InvoiceMaintain |
| Revenue contract / roll-forward | view allocation, adjust SSP, satisfy milestone; run period, drill | obligation, SSP, allocated, recognized, deferred; opening…closing, GL 2400 | RevRecMaintain / RevRecView |
| Subscriptions | create, change plan, pause, cancel | plan, cycle, next bill, proration preview | SubscriptionMaintain |

Interaction rules: proposals show tax and journal preview before post; multi-select post ≤ 500 invoices with per-row results; roll-forward drill-down opens schedules by period; column sets personalizable.

### 5.29.7 Business rules and edge cases
- **BR-29.1 ASC 606 allocation worked example.** Bundle: hardware (SSP 8,000.00 USD, POINT_IN_TIME at packing slip), installation (SSP 1,500.00, MILESTONE), 12-month support (SSP 2,500.00, RATABLE). Σ SSP 12,000.00; contract price 10,800.00 (10% bundle discount). Factor 10,800 / 12,000 = 0.9 → hardware 7,200.00, installation 1,350.00, support 2,250.00. Invoice at shipment 2027-03-10: Dr 1200 10,800.00 (plus tax), Cr 4000 7,200.00, Cr 2400 3,600.00. Installation satisfied 2027-03-25: Dr 2400 / Cr 4000 1,350.00. Support 2027-03-10 to 2028-03-09 at 2,250.00 / 12 = 187.50 per month; first partial month 22/31 × 187.50 = 133.06; the last period absorbs the residual so Σ = 2,250.00. With a contract price of 10,799.99 the allocation 7,199.99 / 1,349.99 / 2,249.99 sums to 10,799.97; the 0.02 residual goes to the largest obligation (hardware 7,200.01).
- **BR-29.2 SSP determination order.** Explicit SSP per product × customer group → list price (PRICE access 7) → % of list per product group → residual (at most one obligation per contract).
- **BR-29.3 Rounding.** Line net half-even to minor units; tax per line (w13) then summed unless the engine returns document-level rounding; allocation at 2 decimals with residual rule; ratable schedules at 2 decimals, last period absorbs the residual.
- **BR-29.4 Dates.** invoice_date ≥ slip posting date; posting_date in an open AR period; due_date = invoice_date + terms (incl. end-of-month variants, w7); cash discount date per terms.
- **BR-29.5 Consolidation** groups by (invoice_account, currency, terms, cycle, address if `split_by_address`); ≤ 2,000 lines; a slip belongs to at most one invoice.
- **BR-29.6 Credit note on partially recognized revenue** reverses recognized revenue proportionally (Dr 4000) and unrecognized deferred (Dr 2400) in the credit period; later schedules become REVERSED.
- **BR-29.7 State machines.** Invoice: PROPOSAL → POSTED → PARTIALLY_SETTLED → SETTLED; POSTED → REVERSED only same posting date with no settlements. Schedule: SCHEDULED → RECOGNIZED → REVERSED (credit note only).
- **BR-29.8 Proration** is daily: amount × service days / period days, including 29 February; a mid-period plan change credits unused old-plan days and bills new-plan remaining days on one invoice; cancellation effective date ≥ today.
- **BR-29.9 Gap-free numbering.** Two posts of one proposal share the Idempotency-Key and return the same invoice; the number is drawn from a serialized sequence per (entity, fiscal_year) in the saga's final step, after tax and journal succeed, so a failed post consumes no number.
- **BR-29.10 Usage records** accepted up to 45 days after period end; later ones bill in the next period with `late=true`.
- **BR-29.11 Installment split.** Installment i amount = round_half_even(gross × pct_i, minor units); the last installment = gross − Σ previous; due_date_i = invoice_date + offset_i (or the fixed date) rolled per terms rules; tax is reported once on the invoice, not per installment; the installments share one journal posting.
- **BR-29.12 Indexation** applies the index rule on the first bill date ≥ effective date; new unit price = round_half_even(old × (1 + pct/100) or old × index_new / index_base, 6 decimals); the invoice line text shows the old price, factor and new price; indexation never re-prices already-billed periods.
- **BR-29.13 Expense deferral** schedule amount per period = total × days in period / term days (daily) or total / periods (monthly) at 2 decimals with the residual on the last period; the short-term portion is recomputed at each period close as the next 12 months of unrecognized amounts and reclassified 1310 → 1300.
- **BR-29.14 Self-billing reconciliation.** Match key = packing slip or delivery number + product; quantity must equal exactly; price difference ≤ `self_billing_tolerance` (default 0.5% or 5.00) posts an adjustment credit note (customer lower) or debit note (customer higher) referencing the invoice; unmatched statement lines create review tasks; an invoice may be self-billed once.
- **BR-29.15 Document routing** evaluates (customer, invoice account, document type) → (customer, document type) → (customer) → entity default; EDI channel requires an active w35 trading-partner agreement else falls back to EMAIL with a warning; PRINT queues to the w5 print service; delivery failures retry 3× then appear in the Billing workspace tile "undelivered documents".

### 5.29.8 Acceptance criteria
- **AC-29.1** (FR-29.3) Given 1,000 random invoices in USD, EUR and JPY, then gross = Σ line_net + charges + tax for every invoice (property).
- **AC-29.2** (FR-29.7) Given the BR-29.1 bundle, then obligations are 7,200.00 / 1,350.00 / 2,250.00 and the first postings match the example to the cent.
- **AC-29.3** (FR-29.7, FR-29.8) Given the 40 ASC 606 fixtures, then allocation and recognition per period equal the accountant-reviewed workbook for 40/40.
- **AC-29.4** (FR-29.9) Given 2027-03 additions 3,600.00 and recognized 1,483.06, then closing 2,116.94 = GL 2400.
- **AC-29.5** (FR-29.2) Given 3 slips for one account on a weekly cycle, then one invoice is proposed and each slip becomes INVOICED on post.
- **AC-29.6** (FR-29.5) Given the posted 10,800.00 invoice with installation recognized, when a full credit note posts, then AR −10,800.00, recognized revenue reversed and remaining deferred reversed, with schedules REVERSED.
- **AC-29.7** (FR-29.10) Given a monthly 300.00 EUR subscription starting 2028-02-10 billed in advance, then the first invoice is 300 × 20/29 = 206.90 EUR.
- **AC-29.8** (FR-29.6) Given the tax engine is down, when posting, then 503 `TAX_UNAVAILABLE` and no journal or open item exists.
- **AC-29.9** (FR-29.4) Given a DMF-DE free-text line to 4900 with 19% VAT of 100.00 EUR, then Dr 1200 119.00 / Cr 4900 100.00 / Cr 2300 19.00.
- **AC-29.10** (FR-29.18) Given terms "3 installments 30/30/40 at +0/+30/+60 days" and a gross of 1,000.01 USD invoiced 2027-03-10, then open items of 300.00 (due 03-10), 300.00 (04-09) and 400.01 (05-09) exist with one invoice number and one journal posting.
- **AC-29.11** (FR-29.14) Given a monthly subscription line at 100.00 EUR with a 3% index rule effective 2027-04-01, when the April bill runs, then the line bills 103.00 with the indexation note and March remains 100.00.
- **AC-29.12** (FR-29.15) Given a 12,000.00 USD annual insurance invoice (w19) dated 2027-03-01 deferred monthly, then 1,000.00 recognizes each month Dr 6300 / Cr 1300, and at 2027-03 close the long-term portion is 0.00 and short-term 11,000.00.
- **AC-29.13** (FR-29.16) Given a self-billing statement with 10 lines of which 9 match and one prices 0.3% lower, then 9 invoices are marked self-billed, one adjustment credit note posts for the difference, and no review task exists.
- **AC-29.14** (FR-29.19) Given customer C3 with preference EMAIL for invoices and PORTAL for statements, when an invoice posts, then the PDF is emailed to the two configured recipients within 60 s and the delivery log shows status DELIVERED.
- **AC-29.15** (FR-29.17) Given a bundle product B-1 (hardware, installation, 12-month support) ordered at 10,800.00 USD, when invoiced, then one revenue contract with three obligations exists per BR-29.1 and the PDF prints one bundle line with three itemized components.

### 5.29.9 Test plan and fixtures
Fixtures: `asc606-40` (bundles with SSPs, discounts, milestones, modifications; workbook `fixtures/revrec/asc606-40.xlsx`); `invoices-random-1k`; `subscription-edges-25` (mid-period, leap day, plan change, cancel); `consolidation-12`; `credit-notes-15`.

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Totals | property | post | gross = Σ components, 3 currencies |
| ASC 606 | unit | revrec | 40/40 match workbook |
| Roll-forward = GL 2400 | reconciliation | nightly | 0.00 |
| Proration edges | unit | subscription | 25/25 |
| Posting saga | integration | billing ↔ tax ↔ ledger ↔ receivables | no partial state on injected failures |
| Documents | snapshot | docgen templates | PDF text and UBL schema valid |
| Billing ↔ fulfillment/receivables | contract (Pact) | APIs | green |

Reconciliation: Σ POSTED gross − credit notes = AR additions = GL 1200 debits from billing; recognized revenue = GL 4000 credits from billing + revrec; DNI reversals = slip costs invoiced.

### 5.29.10 Deployment and operations
Helm `passage-billing`, `passage-revrec` (Go); `billing-db`, `revrec-db`; docgen templates in a ConfigMap per entity. Config: `BILLING_MAX_LINES=2000`, `BILLING_BATCH_MAX=500`, `REVREC_RESIDUAL_RULE=largest`, `USAGE_LATE_DAYS=45`. Flags: `billing.consolidation`, `billing.subscriptions`, `revrec.usage`, `billing.einvoice`. Jobs: subscription billing 03:00; recognition run on period-close trigger (w14) plus nightly preview; roll-forward check 02:30. SLOs: post p95 < 3 s, 99.95% availability. Alerts: failed posts > 1% in 15 min, roll-forward diff ≠ 0, run > 30 min. Runbook: re-run proposal, same-day reversal, regenerate document. Rollback: previous digest; recognition run idempotent per period.

### 5.29.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustInvoiceJour/Trans and CustInvoiceTable/Line with open CustTransOpen | invoice (POSTED), lines | header amounts and due dates; lines for trace |
| RevRecSchedule / RevRecContract (D365) | revenue_contract, obligations, unrecognized schedules | unrecognized = opening 2400 |
| VBRK/VBRP with open BSID | invoice, lines | VBTYP O → CREDIT_NOTE |
| FARR_D_CONTRACT / POBS / DEFITEM (RAR) | revenue_contract, obligations, schedules | deferred items per POB → schedules |
| FPLA/FPLT open billing plans | subscription / milestone_schedule | |

Validation: Σ migrated open balances = legacy AR trial balance per customer; Σ unrecognized schedules = legacy deferred revenue balance. Not migrated: settled invoices before the current fiscal year (lakehouse), SSRS/Smart Forms layouts (re-templated), RAR posting history.

### 5.29.12 Dependencies, risks, open questions
- Depends on: w28 packing slips; w27 lines and terms; w13 tax; w11/w12 journal and currencies; w14 period control; w26 explain refs and rebate basis; w5 document rendering and gap-free sequences.
- Risks: gap-free numbering under saga failure (BR-29.9); missing SSP for migrated products (residual method with warning report).
- Open questions: (1) Document- vs. line-level tax rounding per country? Default line level unless the country pack says otherwise; decide by wave 30. (2) Subscription invoices gap-free per entity or per subscription? Default per entity; decide at wave 29 mid-point.

### 5.29.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| summary_update_default | customer (w7) with entity default | enum | ORDER | ORDER, INVOICE_ACCOUNT, AUTOMATIC, NONE | FR-29.2 consolidation | AR parameters → Summary update tab (Summary update for invoices) | VOFA billing type collective invoice / VTFL copy control split criteria |
| consolidation_cycle_default | customer | enum | NONE | NONE, DAILY, WEEKLY, MONTHLY | FR-29.2 | Summary update parameters | Billing schedule (KNVV-PERFK calendar) |
| split_by_address | legal entity | bool | false | true/false | BR-29.5 | Summary update parameters "Split by delivery address" | VTFL split criteria (ZUKRI) |
| invoice_date_source | legal entity | enum | SHIP_DATE | SHIP_DATE, TODAY, MANUAL | BR-29.4 | Invoice posting "Invoice date" default | VOFA / VTFL billing date determination (FKDAT) |
| invoice_number_scope | legal entity | enum | ENTITY_FISCAL_YEAR | ENTITY_FISCAL_YEAR, ENTITY, ENTITY_DOC_TYPE | BR-29.9 | Number sequences (CustInvoiceId) | VOFA number range per billing type (VN01) |
| max_invoice_lines / batch_post_max | tenant | int | 2,000 / 500 | 1–10,000 / 1–2,000 | FR-29.3 | n/a | n/a |
| tax_recheck_tolerance | legal entity | money | 0.01 | ≥ 0 | FR-29.6 | n/a | n/a |
| tax_rounding_level | country pack | enum | LINE | LINE, DOCUMENT | BR-29.3 | Sales tax parameters "Rounding by" | OVK1 / T001 tax rounding (TXKRS) |
| revrec_residual_rule | legal entity | enum | LARGEST | LARGEST, FIRST, LAST | BR-29.1 | n/a (D365 allocation rounding) | RAR rounding of allocation |
| ssp_source_order | legal entity | list | EXPLICIT, LIST, PCT_OF_LIST, RESIDUAL | ordered subset | BR-29.2 | D365 revenue prices | RAR SSP condition types |
| ratable_basis | legal entity | enum | DAILY | DAILY, MONTHLY | FR-29.8 | Revenue schedule (RevRecSchedule) "Monthly / daily" | RAR fulfillment type time-based |
| deferred_revenue_account / revenue_account defaults | legal entity | account | 2400 / 4000 | chart (w11) | FR-29.3 | Item posting profile deferred revenue | VKOA account keys ERL / ERF |
| usage_late_days | tenant | int | 45 | 0–180 | BR-29.10 | n/a | n/a |
| subscription_billing_time | legal entity | time | 03:00 | — | FR-29.10 job | D365 subscription billing batch | VF04 periodic billing schedule |
| index_rule_default | subscription group | ref | none | index rules | FR-29.14 | SMA index rules | Condition price adjustment (VK12 with ref) |
| expense_deferral_accounts | legal entity | account pair | 1300 / 1310 | chart | FR-29.15 | D365 deferral schedule accounts | ACCR accrual object accounts |
| self_billing_tolerance | customer | numeric | 0.5% or 5.00 | ≥ 0 | BR-29.14 | n/a | VSB1 tolerance per self-biller |
| print_components | product (bundle) | bool | true | true/false | FR-29.17 | Kit print options | Item category "Print BOM" (VOV7) |
| installment_residual_to | legal entity | enum | LAST | FIRST, LAST | BR-29.11 | Payment schedule rounding | OBB9 installment rounding |
| document_delivery_default | legal entity | enum | EMAIL | EMAIL, PORTAL, PRINT, EDI, NONE | FR-29.19 | Print management (original destination) | NAST output medium default (VV31) |
| document_resend_retries | tenant | int | 3 | 0–10 | BR-29.15 | n/a | NAST repeat processing |
| einvoice_enabled | legal entity | bool | false | true/false | FR-29.11 hook | Electronic reporting format setup | DRC / eDocument activation |

### 5.29.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Billing due list | list inquiry | customer, cycle, date range, warehouse | slip, order, amount, cycle, consolidation key | packing_slip (POSTED not INVOICED) | Invoice "Select" / summary update proposal | VF04 billing due list |
| Invoices list | list inquiry | customer, type, status, date, currency | number, type, dates, gross, open, status | invoice | Invoice journal (CustInvoiceJour) | VF05 / VF05N |
| Invoice trace | drill-down | invoice | invoice → slip → order line → price explain → schedules | trace API | CustInvoiceTrans → InventTransId (no single view) | VBFA document flow |
| Deferred revenue roll-forward | statutory / operational report | entity, period, contract | opening, additions, recognized, adjustments, closing, GL 2400 diff | revenue_schedule | D365 revenue recognition reconciliation | FARR_RECON / RAR disaggregation reports |
| Revenue contracts | list inquiry / drill-down | customer, status, obligation method | price, SSP, allocated, recognized, remaining | revenue_contract | RevRecContract (D365) | FARR_CONTRACT_MAIN |
| Subscriptions and fee transactions | list inquiry | group, status, next bill date, fee category | subscription, lines, next bill, last index, MRR | subscription | SMA subscription inquiries / SSRS Subscription fee transactions | VA45 contract list / FPLA billing plan |
| Expense deferrals | list inquiry | vendor, account, period | schedule, total, recognized, short-term, long-term | expense_deferral | D365 deferral schedules | Accrual engine (ACACTREE) reports |
| Self-billing reconciliation | drill-down | customer, statement | line, match status, price difference, adjustment | self_billing_statement | n/a | VSB1 log / self-billing monitor |
| Failed posts / undelivered documents | KPI / list | date, error | proposal, error code, retries; document, channel, failure | proposal + delivery log | Posting log / print management log | VF04 log, NAST error (V.21) |
| Invoice register (sales journal) | statutory report | entity, period, tax code | number, date, customer, tax id, net, tax, gross | invoice | SSRS Invoice journal / sales tax transactions | RFBELJ00 / S_ALR_87012357 |
| Credit notes analysis | operational report | reason, customer, period | count, amount, % of revenue, recognized reversed | credit_note | Credit note journal | VF05 filter VBTYP O |

### 5.29.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Sales invoice | post | header: entity legal name, address, tax ids, invoice number, date, due date, customer, invoice account, ship-to, order, terms, currency; lines: product, description, qty, unit, unit price, discount, net, tax code; footer: net, charges, tax by rate, gross, installments, cash discount, bank details, legal text | PDF, UBL 2.1 (w35 Peppol), email, portal, print | `INV-` gap-free per entity and fiscal year | statutory: EU VAT Directive art. 226 content; retain 10 years (DE GoBD), 7 years (US) |
| Free-text invoice | post | as invoice; lines: description, account-driven, tax code | PDF, UBL, email | same `INV-` series (or `FTI-` if `invoice_number_scope = ENTITY_DOC_TYPE`) | as invoice |
| Credit note | post | as invoice plus original invoice reference and reason | PDF, UBL CreditNote, email | `CN-` gap-free | as invoice; must reference original (art. 219) |
| Subscription invoice | daily billing job | as invoice plus service period, proration, indexation note | PDF, UBL, email | `INV-` | as invoice |
| Bundle invoice | post | bundle line with itemized components | PDF, UBL | `INV-` | as invoice |
| Self-billing adjustment note | reconciliation | statement reference, invoice, difference | PDF, email | `CN-` / `DN-` | as invoice; self-billing agreement on file (EU art. 224) |
| Revenue contract summary | on demand | obligations, SSP, allocation, schedule | PDF, CSV | none | audit support 7 years |
| Deferred revenue roll-forward | period close | opening → closing per contract | PDF, XLSX | none | audit workpaper 7 years |

### 5.29.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Consolidated billing run | per cycle (daily 22:00, weekly Sunday 22:00, monthly last day 22:00) | invoice account | key = (account, cycle, period) | < 20 min per 50k slips | per-invoice saga; failures listed | failed > 1% |
| Subscription billing | 03:00 daily | subscription | key = (subscription, period) | < 30 min per 100k subscriptions | retry; skip and report | subscriptions due not billed by 06:00 |
| Indexation run | per index effective date, preview 7 days ahead | subscription group | key = (rule, effective date) | < 5 min | preview stays open | index missing |
| Revenue recognition run | period-close trigger (w14) + nightly preview 01:00 | legal entity × period | key = period; per schedule line status | < 30 min per 1M schedule lines | resumable from last schedule id | run > 30 min; diff ≠ 0 |
| Expense deferral recognition | period close + nightly preview | legal entity | key = period | < 10 min | resumable | unrecognized past periods > 0 |
| Roll-forward check | 02:30 daily | legal entity | key = (entity, date) | < 5 min | report only | diff ≠ 0.00 |
| Document delivery retry | every 10 min | channel | delivery id | < 1 min | 3 retries then workspace tile | undelivered > 50 |
| Self-billing statement import | on file arrival (SFTP poll 15 min) | customer | SHA-256 of file | < 2 min | quarantine malformed | review tasks > 20 |

### 5.29.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| InvoiceView | GET invoices, documents, trace; Billing workspace | none | Billing clerk, AR clerk, Sales, Auditor |
| InvoiceMaintain | proposals, free-text invoices, due list | with ARPaymentPost (w30) | Billing clerk |
| InvoicePost | :post (single and batch) | with PackingSlipPost (w28) under four-eyes; with CustomerMaintain (w7) | Billing clerk |
| CreditNotePost | credit notes :post | with ARSettle and with InvoicePost on the same invoice when `credit_note_four_eyes` | Billing supervisor |
| RevRecMaintain | :allocate, SSP adjustments, milestones | with RevRecPost | Revenue accountant |
| RevRecPost | recognition runs, expense deferral runs | with RevRecMaintain | Controller |
| RevRecView | roll-forward, contracts | none | Controller, Auditor |
| SubscriptionMaintain | subscriptions, :change, :cancel, usage, index rules | with InvoicePost when subscription invoices auto-post | Subscription manager |
| SelfBillingReconcile | statement import, match, adjustments | with CreditNotePost | Billing supervisor |
| DocumentDeliveryMaintain | delivery preferences, resend | with CustomerMaintain (bank detail fraud risk on recipients) | AR manager |

### 5.29.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| BIL-0001 | error | Tax engine unavailable; invoice not posted | w13 outage | Retry later | FR-29.6 / AC-29.8 |
| BIL-0002 | error | Tax changed by {amount} since proposal; review required | tax recheck > tolerance | Re-review and post | FR-29.6 |
| BIL-0003 | error | Posting period {period} is closed | BR-29.4 | Choose an open period | BR-29.4 |
| BIL-0004 | error | Line {n} has no revenue account | posting profile gap | Set account in w16/w11 profile | §5.29.5 validation |
| BIL-0005 | error | Credit exceeds remaining creditable amount {amount} | FR-29.5 | Reduce credit | FR-29.5 |
| BIL-0006 | error | Invoice date {date} precedes packing slip date {slip_date} | BR-29.4 | Correct date | BR-29.4 |
| BIL-0007 | error | Consolidation exceeds {max} lines; split into {n} invoices | BR-29.5 | none (auto split) or reduce | BR-29.5 |
| BIL-0008 | error | Same-day reversal not allowed: invoice has settlements | BR-29.7 | Issue a credit note | BR-29.7 |
| BIL-0009 | warning | SSP missing for {product}; residual method used | BR-29.2 | Maintain SSP | FR-29.7 |
| BIL-0010 | warning | Usage record is late; billed next period | BR-29.10 | none | BR-29.10 |
| BIL-0011 | error | Installment percentages total {pct}%, expected 100% | FR-29.18 | Fix terms (w7) | BR-29.11 |
| BIL-0012 | warning | Index value for {index} missing for {date}; indexation skipped | FR-29.14 | Load index | BR-29.12 |
| BIL-0013 | error | Self-billing line {n} does not match any invoice | BR-29.14 | Review task | FR-29.16 |
| BIL-0014 | error | Invoice {number} already self-billed | BR-29.14 | none | FR-29.16 |
| BIL-0015 | warning | EDI delivery not configured for {customer}; sent by email | BR-29.15 | Activate trading partner (w35) | FR-29.19 |
| BIL-0016 | error | Document delivery failed after {n} retries | BR-29.15 | Resend or change preference | FR-29.19 |
| BIL-0017 | error | Expense deferral term must be ≥ 2 periods | FR-29.15 | Post as expense | FR-29.15 |

### 5.29.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Invoice post (saga, 10 lines) | 50k per day; peak 20/s | 900 ms / 3 s / 6 s | 20/s per replica | 12M invoices × 4 KB + lines 60 GB | partition by posting month; (invoice_account_id, status, due_date) | saga workers scale; batch posts throttle to 50% |
| Proposal from packing slip | 100k per day | 100 / 400 / 1,000 ms | 50/s | proposals purged after 90 days | (slip_id) | event lag allowed to 5 min |
| PDF render + delivery | 60k per day | 1 / 4 / 10 s | 30/s | object store 300 GB (10-year retention, cold after 1 year) | content hash | rendering queued; post never waits for PDF |
| Recognition run | 1M schedule lines per period | — / 30 min / 45 min | 1k lines/s | schedules 200M rows × 150 B = 30 GB | partition by period | run split by contract range |
| Subscription billing | 100k subscriptions per day | — / 30 min / 60 min | 60 subs/s | included above | (next_bill_date, status) | finish by 06:00 or alert |
| Roll-forward query | 200 per day | 300 ms / 2 s / 5 s | — | none | materialized per (entity, period) | serve last materialization |
| Trace API | 20k per day | 80 / 300 / 800 ms | 50/s | none | FK lookups | trace depth limited to explain summary |

### 5.29.20 Localization and compliance hooks

- Invoice content and numbering: EU VAT Directive arts. 220–240 (mandatory fields, gap-free sequences, credit-note references, self-billing agreements art. 224); country packs (w48) add fields (IT codice destinatario, ES SII, FR SIREN, DE Leistungsdatum) through the docgen `country_fields` slot and the UBL extension.
- E-invoicing mandates (IT SDI, FR PPF/PDP 2026–2027, DE XRechnung/ZUGFeRD B2B from 2025, PL KSeF, MX CFDI) are transmitted in w35; this wave produces the UBL 2.1 payload and the `einvoice_ref` hook.
- Tax rounding line vs document per country (BR-29.3, open question 1); US sales tax by jurisdiction via the w13 engine; JPY zero-decimal handling.
- Revenue recognition: ASC 606 and IFRS 15 share one model; country GAAP variants (for example local GAAP point-in-time only) are a second ledger posting in w11 parallel ledgers, not a second engine.
- Down-payment invoices and tax on down payments (DE §13 UStG) are generated by w30 FR-30.21 using the invoice document type DOWN_PAYMENT_REQUEST.
- Retention: invoices and credit notes 10 years immutable (GoBD, w48 archive); PDF/A-3 for DE when `einvoice_enabled`.
- Privacy: invoices for one-time customers and individuals contain personal data; erasure requests are refused within the statutory retention period and pseudonymized after it.

### 5.29.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.28.1 #227 — Service subscriptions (SMASubscriptionTable, fee transactions, index) | PARTIAL-29 | ADDED as FR-29.14 | service agreements link from wave 45 |
| AX §12.28.2 #228 — Subscription inquiries | PARTIAL-29 | ADDED as FR-29.14 | §5.29.14 Subscriptions and fee transactions |
| AX §12.28.2 #229 — SSRS Subscription report / Subscription fee transactions | PARTIAL-29 | ADDED as FR-29.14 | inquiry export (PDF/CSV) |
| AX §12.28.3 #230 — Create subscription fee transactions / Accrue subscription revenue / Index subscriptions | PARTIAL-29 | ADDED as FR-29.14 (fees, index) and FR-29.8 (accrual) | §5.29.16 indexation run |
| AX §12.28.4 #231 — Subscription groups, fee categories, index rules, subscription periods | PARTIAL-29 | ADDED as FR-29.14 | |
| AX §12.37.1 #314 — Deferral schedules (revenue and expense, short/long-term split) | PARTIAL-29 | ADDED as FR-29.15 | 1300 / 1310 split |
| AX §12.37.3 #315 — Expense deferral recognition | PARTIAL-29 | ADDED as FR-29.15 | §5.29.16 job |
| SAP §13.19 #220 — VKA / VSB1 customer self-billing | GAP (propose 29) | ADDED as FR-29.16 | EDI GSVERF / 820 mapping in wave 35 |
| SAP §13.19 #221 — Solution orders / bundled sales | PARTIAL-29 | ADDED as FR-29.17 | |
| SAP §13.1 #17 — OBB8 / OBB9 terms of payment / installment terms (PARTIAL-7) | PARTIAL-7 | ADDED as FR-29.18 (application at invoice post); terms definition remains wave 7 | flagged for order-to-cash |
| SAP §13.3 #47 — OBB9 installment terms (customers) (PARTIAL-7) | PARTIAL-7 | ADDED as FR-29.18 | |
| SAP §13.2 #35 — OBB9 installment terms (vendors) (PARTIAL-7) | PARTIAL-7 | DEFERRED to wave 19 | AP invoice installments reuse the w7 terms model |
| AX §12.3.4 #30 — Form setup (sales documents) / Form notes / Print management | PARTIAL-46 | ADDED as FR-29.19 (per-account destinations); query-based conditional routing DEFERRED to wave 46 | |
| AX §12.17.4 #173 — Form setup, form notes, print management (sales), form sorting | PARTIAL-46 | ADDED as FR-29.19; form sorting SUPERSEDED by wave 46 document designer | |

## 5.30 Wave 30: Accounts Receivable, Collections, Returns & Customer Portal

### 5.30.1 Scope and business context
Wave 30 closes the loop. The `receivables` service applies cash (manual, BAI2 lockbox, remittance advice, camt.053 with ML-assisted suggestions) and settles open items; `collections-workflows` (TS on Temporal) runs aging, dunning, interest notes, promises to pay, disputes and risk scoring; return orders flow through RMA dispositions to inventory and credit notes; wave 26 accruals settle; statements are produced; and the customer portal at `customer.<env>.passage.dev` exposes orders, invoices, payments (Stripe/Adyen sandbox), returns and documents. The wave ends with the Phase 5 exit: the demo manufacturer's one-month simulation with reconciled subledgers and stakeholder sign-off. Out of scope: non-customer bank reconciliation and cash forecasting (wave 36), collections-agency integration, portal self-registration beyond invited contacts, bills of exchange (wave 36), generic case management (wave 45). Added from the coverage registers (§5.30.21): SEPA direct debit mandates and pain.008, deductions workbench, minimal collection cases, correspondence framework with email templates, balance confirmations, down payments and security deposits, distributor chargebacks, vendor dunning, GL/intercompany interest, portal guided configuration and returnable packaging charges.

**Pain points solved (10x lens)**

| Pain point in AX/SAP today | Root cause | Passage answer in this wave | Measurable 10x target |
|---|---|---|---|
| Cash application is manual: AX settle open transactions (CustOpenTrans) makes the clerk find invoices per check; SAP F-28 plus FLB2 post-processing lands 30–50% of payments on account | Exact-reference matching only; no scoring, subset-sum or remittance parsing | BR-30.1 scored rules R1–R7, auto-apply ≥ 85, ML-ranked suggestions across lockbox, camt.053, remittance and portal | Auto-match 40–60% → ≥ 90%; clerk time per payment 3 min → < 20 s; false matches 0 |
| Dunning produces wrong letters: F150 MHNK proposals ignore disputes without FSCM; CustCollectionLetterJour cannot skip promised items | Exclusion state not part of selection | Exclusions from open_item.dispute_id/promise_id at issue time; grace, interval and minimum rules (BR-30.2) | Retracted letters ~5% → 0; run preparation 2 h → < 10 min |
| Returns need three hand-keyed documents (ReturnTable → arrival journal → credit note; RE order → LR delivery → G2 request → VF01) | Separate forms with manual references | One RMA with dispositions (BR-30.3) driving receipt, scrap/repair/replace and credit note in a saga | Clicks per return ~45 → ≤ 8; cycle time 3–5 days → same day after receipt |
| Customers phone for statements, invoice copies and status; clerks re-send PDFs | No customer-facing surface on live open items | Portal on the same `open-items`, `invoices`, `shipments` APIs with card/ACH payment and return requests | "Copy of invoice/status" requests −80%; DSO −5 days via portal payments |
| AR-to-GL reconciliation is a month-end exercise (CustTrans vs. LedgerTrans; BSID vs. recon account via F.03) | Subledger and GL updated by different code paths | Every settlement posts through the journal API; Σ open_amount = GL 1200 daily, in the reconciliation pack | Effort 1–2 days/month → 0; diff 0.00 for 31/31 days |

### 5.30.2 AX / SAP parity map

| Capability | AX 2012 / D365 F&SCM | SAP ECC / S/4 | Passage equivalent | Parity |
|---|---|---|---|---|
| Payments and settlement | CustPaymJournal (LedgerJournalTrans), CustTrans/CustSettlement, CustPaymModeTable | F-28, F-32, BSID → BSAD, OB41 | payment, settlement, open_item | full |
| Lockbox | CustLockbox (BAI2 import) | FLB2 / FLB1 (BAI2) | lockbox_batch | full |
| Bank statement auto-match | Advanced bank reconciliation (camt.053) | FF_5 / EBS camt.053 auto-clearing | camt.053 import + matcher | improved (ML suggest) |
| Aging / collections | CustAgingLegalEntity, Collections workspace, CustCollectionsPool | FBL5N; FSCM Collections (UDM_SPECIALIST) | aging snapshot, worklist, risk score | full |
| Collection letters / dunning | CustCollectionLetterJour/Line, sequence, grace days | F150 (MHNK/MHND), FBMP procedure | dunning_run, dunning_letter | full |
| Interest notes | CustInterestJour/Trans | F.24 / F.2B | interest_note | full |
| Promise to pay / disputes | Collections promise; cases | FSCM dispute (UDM_DISPUTE) | promise_to_pay, dispute (Temporal) | full |
| Return orders / RMA | ReturnTable (SalesType=ReturnItem), ReturnDispositionCode, ReturnReasonCode | VA01 type RE, LR delivery, G2 credit memo request; S/4 advanced returns | return_order + return receipt + credit_note | full |
| Rebate settlement | Rebate settlement to credit note / AP | VBO1 settlement, B1/B2 credit memo | rebate_settlement → credit_note or AP payment | full |
| Statements / portal / card payments | CustAccountStatementExt; D365 Customer portal; Adyen connector | F.27; SAP Commerce; digital payments add-on | statement job; customer-portal; payments-gateway | improved |
| SEPA direct debit | CustDirectDebitMandate, CustPaymModeTable (SEPA DD), generate payments (pain.008) | FSEPA_M1..M4 mandates, F110 with payment method DD, SEPA_* customizing | dd_mandate, direct_debit_run → pain.008 | full |
| Deductions | TAMDeductionWorkbench, deduction types, denial reasons | FSCM dispute case with residual items (F-32 residual, UDM_DISPUTE) | deduction (from short pay) + deduction workbench | full |
| Collection cases | CaseDetail (collections category) | FSCM UDM_DISPUTE / collection worklist case | collection_case (minimal case entity; generic cases w45) | partial |
| Correspondence | Customer payment advice, collection letters, CustAccountStatementExt | FB12 / F.61 / F.64, OB77 correspondence types (T048) | correspondence_type + correspondence_request via docgen | full |
| Balance confirmations | — (custom SSRS) | F.17 / F.18 (classic), F.99 / F.97 | balance_confirmation run with reply tracking | full |
| Special G/L customer items | Customer prepayments (CustPrepayment), deposit journals | OBXR special G/L indicators A (down payment), H (security deposit), W (BoE) | open_item.special_gl_type DOWN_PAYMENT, SECURITY_DEPOSIT | partial (BoE w36) |
| Distributor chargebacks | Rebate management deals (D365) | Settlement management chargeback claims, EDI 844/849 | chargeback_claim validation against w26 agreements | full (EDI mapping w35) |
| Vendor dunning | — | F150 for vendors with debit balances | dunning engine over AP debit open items (w19) | full |

### 5.30.3 Functional requirements
- **FR-30.1 Open items and settlement** — Must. Open items from invoices, credit notes, payments and adjustments; full or partial settlement with cash discount, write-off within tolerance and FX gain/loss (w12) posted at settlement.
- **FR-30.2 Manual payments** — Must. Payment journal by mode (CHECK, ACH, WIRE, CARD, CASH): Dr 1000/1010 bank, Cr 1200; unapplied residual to 1205.
- **FR-30.3 Lockbox BAI2** — Must. Import record types 1–9, one payment per check with remittance detail, then match.
- **FR-30.4 Remittance advice** — Should. CSV/JSON/EDI 820 hook attached to payments for matching.
- **FR-30.5 camt.053 auto-match** — Must. Import statements, identify customer credits (CdtDbtInd=CRDT with RmtInf and debtor data), match per BR-30.1; a Python ranker reorders suggestions but never auto-applies on its own.
- **FR-30.6 Aging and worklist** — Must. Nightly aging (current, 1–30, 31–60, 61–90, 91–180, > 180 by due or invoice date) and a worklist ranked by risk score.
- **FR-30.7 Dunning** — Must. Levels, grace days, minimum amounts, blocks and intervals (BR-30.2); letters via docgen; fees posted; item level raised only at issue.
- **FR-30.8 Interest notes** — Should. Annual rate, ACT/365, minimum 5.00 USD / 5.00 EUR, posted as open items.
- **FR-30.9 Promises and disputes** — Must. Promises (amount, date) and disputes (reason, amount, owner, SLA) exclude items from dunning and interest while open; Temporal workflow to resolution (credit, paid, write-off, rejected).
- **FR-30.10 Risk scoring** — Should. 0–100 from overdue ratio, average days to pay, dunning level, disputes and broken promises; published to credit.
- **FR-30.11 Return orders (RMA)** — Must. RMA referencing invoice lines (or blind), 30-day validity, receipt to quarantine/inspection, dispositions (BR-30.3), inventory receipt at return cost, credit note with restocking charges.
- **FR-30.12 Rebate settlement** — Must. Settle wave 26 accruals per agreement period into a credit note (default) or AP payment (w19 hook), reversing 2150.
- **FR-30.13 Statements** — Should. Monthly and on-demand PDF statements and portal view.
- **FR-30.14 Customer portal** — Must. Invited contacts place orders, view invoices/documents, pay by card/ACH via payments-gateway, request returns and track shipments; Keycloak realm `portal`, OPA scoped to the contact's accounts; WCAG 2.1 AA.
- **FR-30.15 Phase-exit simulation** — Must. Harness `sim/phase5-month` drives Phases 3–5 end-to-end and produces the §5.0 reconciliation pack.
- **FR-30.16 SEPA direct debit mandates and collection** — Must. The system shall store SEPA mandates per customer bank account (unique mandate reference ≤ 35 chars, creditor identifier per legal entity, scheme CORE or B2B, signature date, sequence state FRST/RCUR/FNAL/OOFF, status PENDING → ACTIVE → SUSPENDED / CANCELLED / EXPIRED), run a direct-debit proposal over due open items of customers with payment mode SEPA_DD and an active mandate, and generate pain.008.001.08 files through the w19 ISO 20022 generator, creating DD payments that settle the items on collection date. Notes: pre-notification sent ≥ 14 calendar days before collection (configurable down to 2 by agreement); mandates unused for 36 months expire automatically; returns and rejects (pain.002 / camt.054 R-transactions) reopen the items and post the return fee as FEE; AX CustDirectDebitMandate and SAP FSEPA_M1..M4 map 1:1.
- **FR-30.17 Deductions workbench** — Should. The system shall convert the unexplained part of a short payment (R7 short-pay with reason, or a remittance line marked deduction) into a `deduction` open item (source DEDUCTION) with type and reason code, and provide a workbench to match deductions to w26 claims (rebate accruals, promotion fund usage, chargebacks), approve (credit note against the claim), deny (re-bill to the customer as a debit open item with a denial reason), split, or write off within `deduction_write_off_limit`. Notes: AX TAMDeductionWorkbench parity; matched deductions reduce the fund or accrual they consume; aging of deductions is separate from invoice aging; deductions never raise a dunning level while open.
- **FR-30.18 Collection cases** — Should. The system shall group collection work into a `collection_case` (customer, category COLLECTION or DISPUTE or DEDUCTION, owner, priority, status, SLA, linked open items, disputes, promises, activities and notes) with a Temporal timer per SLA. Notes: this is the minimal case entity that the AX/D365 generic case framework (categories, processes, knowledge articles) will replace in wave 45 through a migration of `collection_case` rows into generic cases; the API path `/receivables/cases` stays as a façade.
- **FR-30.19 Correspondence framework** — Should. The system shall define correspondence types (ACCOUNT_STATEMENT, PAYMENT_NOTICE, OPEN_ITEM_LIST, INDIVIDUAL_LETTER, DUNNING_LETTER, INTEREST_NOTE, BALANCE_CONFIRMATION, DD_PRE_NOTIFICATION) each with a docgen template per language and an email template (subject, body with merge fields), and accept correspondence requests on demand (single customer, from any open-item or payment screen) or periodic (job), delivered by the FR-29.19 routing with a delivery log. Notes: SAP FB12 / F.61 / F.64 and OB77; D365 collections email templates; the payment notice is generated automatically after each applied payment when the customer preference says so.
- **FR-30.20 Customer balance confirmations** — Should. The system shall run balance confirmations as of a key date for a selected customer set (balance ≥ threshold, or random sample of n), generate a confirmation letter with the open-item list and a reply form (portal link with a one-time token, or email reply), record replies (AGREED, DISAGREED with customer balance and comment, NO_REPLY) and produce a difference list for audit. Notes: SAP F.99 / F.17 parity; replies are immutable once recorded; a second reminder is sent after `confirmation_reminder_days` (default 14).
- **FR-30.21 Down payments and security deposits** — Should. The system shall record customer down-payment requests (noted item, no GL posting), down payments received (Dr bank / Cr 2190 Customer down payments, tagged to an order), clearing of down payments against the final invoice (Dr 2190 / Cr 1200, with tax on down payment where the country pack requires), and security deposits (Dr bank / Cr 2195, released by refund or offset). Notes: SAP special G/L indicators A, F, H (OBXR); AX customer prepayments; open items carry `special_gl_type` and are excluded from dunning and from credit exposure as a reduction only when `down_payment_reduces_exposure`; bills of exchange (indicator W) are out of scope here (wave 36).
- **FR-30.22 Distributor chargeback claims** — Could. The system shall import chargeback claims from distributors (CSV now; EDI 844 in, 849 response via w35) with lines (end customer, product, quantity, contract price, invoice reference), validate each line against the w26 agreement for the end customer (price, validity, eligibility), accept or reject per line with reason codes, and settle accepted amounts by credit note to the distributor or as deduction match (FR-30.17). Notes: chargeback claim types configurable; accepted amount per line = (distributor acquisition price − contract price) × quantity; duplicates detected by (distributor, claim number, line).
- **FR-30.23 Dunning of vendors with debit balances** — Could. The system shall let a dunning run include vendors (w19) whose net open AP balance is a debit beyond `vendor_dunning_min` (default 100.00 USD / 90.00 EUR) using the same levels and templates. Notes: SAP F150 vendor dunning; no fees and no interest on vendors by default.
- **FR-30.24 Interest on GL and intercompany balances** — Could. The system shall extend the interest engine (FR-30.8) to selected GL accounts and intercompany due-to/due-from balances with balance-based interest (daily balance × rate × days / basis ACT/360 or ACT/365), producing an interest calculation journal for review and post. Notes: SAP F.50 / F.52 and OB46 interest indicators; intercompany pairing completes in wave 40.
- **FR-30.25 Portal guided configuration** — Could. The system shall let portal contacts configure a configurable product through the w8 configurator API (attribute questions, constraint validation, price preview from w26) and add the configuration to a portal order. Notes: SAP CPQ / variant configuration web UI parity; uses the same configuration_id as FR-27.11; constraint errors shown inline.
- **FR-30.26 Returnable packaging charges** — Could. The system shall, for returnable packaging balances (FR-28.16) older than `rtp_charge_days`, propose a free-text invoice charging the deposit price per unit, and credit it when the packaging returns within 365 days.

### 5.30.4 Data model

**open_item** (CustTrans/CustTransOpen, BSID)

| Field | Type | Required | Notes |
|---|---|---|---|
| id, legal_entity_id, customer_id, invoice_account_id | uuid | Y | RLS |
| source_type, source_id | enum, uuid | Y | INVOICE, CREDIT_NOTE, PAYMENT, INTEREST, FEE, ADJUSTMENT, REBATE |
| document_number, document_date, due_date | text, date, date | Y | |
| currency, amount, amount_entity_currency | char(3), numeric(19,4) ×2 | Y | debit +, credit − |
| open_amount | numeric(19,4) | Y | = amount − Σ settlements |
| cash_discount_date, cash_discount_amount | date, numeric | N | |
| dunning_level, last_dunned_at, dunning_block | smallint, date, bool | Y | defaults 0 / null / false |
| dispute_id, promise_id | uuid | N | exclusion flags |
| status | enum | Y | OPEN, PARTIALLY_SETTLED, SETTLED, REVERSED |

**payment**: id, payment_number, customer_id (nullable until identified), payment_mode, amount, currency, received_date, value_date, bank_account_id, source (MANUAL | LOCKBOX | CAMT053 | PORTAL | REMITTANCE), source_ref (unique with source), payer_name, payer_account_hash, remittance[] (reference, amount), status (RECEIVED | SUGGESTED | APPLIED | ON_ACCOUNT | REVERSED), match_score, journal_posting_id. **settlement** (CustSettlement): id, payment_id | credit_item_id, open_item_id, settled_amount, cash_discount_taken, write_off_amount, fx_gain_loss, settled_at, journal_posting_id, reversal_of_id.

**dunning_run**: id, legal_entity_id, run_date, parameters, status (DRAFT | REVIEWED | ISSUED | CANCELLED). **dunning_letter**: id, run_id, customer_id, level, items[] (open_item_id, open_amount, days_overdue), fee_amount, document_ref, issued_at. **dispute**: id, customer_id, open_item_id, reason_code, disputed_amount, owner, status, sla_due, workflow_run_id.

**return_order** (ReturnTable): id, rma_number, customer_id, original_invoice_id, return_reason_code, status (BR-30.6), expires_at, lines[] (original_invoice_line_id, product, qty_expected, qty_received, disposition_code, return_cost_method (ORIGINAL_COST | CURRENT_COST | ZERO), restocking_pct, restocking_amount, inventory_receipt_ids[], credit_note_line_id).

Invariants: Σ settlements per item ≤ |amount|; open_amount = 0 ⇔ SETTLED; Σ OPEN open_amount per entity = GL 1200; Σ ON_ACCOUNT = GL 1205; a payment is APPLIED only when Σ settlements + residual = amount; dunning_level never decreases while open; disputed or promised items are excluded from dunning and interest; credited qty ≤ received qty × unit credit. Indexing: open items (customer_id, status, due_date), (document_number); payments (status, received_date), unique (source, source_ref); settlements partitioned by month.

### 5.30.5 APIs and events

| Method | Path | Purpose | Idempotency | Auth duty |
|---|---|---|---|---|
| GET | /api/v1/receivables/open-items?customer=&status=&asOf= | Open items (FBL5N) | — | ARView / Portal (own) |
| POST | /api/v1/receivables/payments, …/{id}:match, :apply, :on-account, :reverse | Payment lifecycle; scored candidates | Idempotency-Key | ARPaymentPost |
| POST | /api/v1/receivables/lockbox/imports; /bank-statements/imports | BAI2 and camt.053 files | SHA-256 of content | ARImport |
| POST | /api/v1/receivables/settlements:batch | Settle with credits/payments, discount, write-off | Idempotency-Key | ARSettle |
| GET | /api/v1/receivables/aging?asOf=&bucketBy= | Aging snapshot | — | ARView |
| POST | /api/v1/receivables/dunning-runs, …/{id}:review, :issue, :cancel | Dunning | Idempotency-Key | DunningRun |
| POST | /api/v1/receivables/interest-notes:calculate, :post; /promises; /disputes, …/{id}:resolve | Interest, promises, disputes | Idempotency-Key | InterestPost / CollectionsMaintain |
| POST | /api/v1/sales/return-orders, …/{id}:authorize; /lines/{l}:receive, :disposition | RMA saga (sales + fulfillment + billing) | Idempotency-Key | ReturnMaintain / WarehouseReceive |
| POST | /api/v1/pricing/rebates/settlements | Settle accruals for a period | key = (agreement, period) | RebateSettle |
| GET/POST | /portal/api/v1/{orders,invoices,payments,returns,shipments,documents} | Portal BFF (scoped) | Idempotency-Key | PortalCustomer |
| POST | /api/v1/payments-gateway/intents; /webhooks/{provider} | Card/ACH intents; signed provider webhooks | provider id | system |

Validation: apply requires Σ settlements + on-account residual = payment amount; cross-currency settlement only with FX settlement enabled; dunning issue requires REVIEWED; disposition requires qty_received > 0; portal payments require Σ selected open ≥ amount.

| Event | Trigger | Payload fields | Consumers |
|---|---|---|---|
| dev.passage.receivables.payment.received.v1 | import / manual / portal | payment_id, source, amount, currency, payer, remittance[] | matcher, analytics |
| dev.passage.receivables.payment.applied.v1 | apply | payment_id, settlements[{open_item_id, amount, discount, write_off, fx}], residual, journal_posting_id | ledger, sales credit, portal, collections |
| passage.receivables.customer-balance.changed.v1 | any open item change | customer_id, balance, overdue, oldest_item_days, dunning_level, risk_score | sales credit, portal |
| passage.receivables.dunning-letter.issued.v1 | issue | run_id, customer_id, level, items[], fee, document_ref | docgen, portal, sales (hold at level 4) |
| dev.passage.receivables.dispute.opened.v1 / resolved.v1 | dispute lifecycle | dispute_id, open_item_id, amount, reason, resolution | collections, billing, analytics |
| passage.receivables.return-order.authorized.v1 / dispositioned.v1 | RMA | rma_id, line, disposition, qty, restocking, credit_note_id | fulfillment, inventory, billing, portal |
| dev.passage.pricing.rebate.settled.v1 | settlement | agreement_id, period_id, amount, credit_note_id | ledger, receivables, analytics |

### 5.30.6 User experience

| Screen | Primary actions | Key fields/columns | Duty |
|---|---|---|---|
| AR workspace | tiles: unapplied cash, overdue by bucket, disputes, dunning due, RMAs awaiting inspection | amounts, counts | ARView |
| Cash application | import, accept/adjust suggestions, apply, on-account, split | payment, payer, amount, candidates with score and rule ids, residual | ARPaymentPost |
| Open items / settlement | select, settle, discount, write-off, reverse | document, due, open amount, discount available, level | ARSettle |
| Collections worklist / dispute detail | log contact, promise, dispute, hold order; assign, evidence, resolve | customer, risk score, overdue, oldest item, promises; amount, reason, SLA, timeline | CollectionsMaintain |
| Dunning run | create, preview letters, exclude, issue | customer, level, items, fee, blocks | DunningRun |
| Return order (RMA) | authorize, receive, inspect, disposition, credit | lines, disposition, restocking, credit preview | ReturnMaintain |
| Customer portal | place order, view invoices/documents, pay, request return, track | orders, invoices, balance, payments, RMAs, tracking | PortalCustomer |

Interaction rules: cash application keyboard flow (J/K candidates, Enter accept, A apply) and bulk accept of all ≥ 85 suggestions; worklist saved filters; dunning preview opens the rendered letter; portal is mobile-first; provider hosted fields so no card data touches Passage.

### 5.30.7 Business rules and edge cases
- **BR-30.1 Cash application matching rules and scores.** Candidates are open items of the identified customer (all customers when unidentified). Score per candidate set: R1 exact document number in remittance/RmtInf/check memo +60; R2 amount = open amount of one item +30, or = Σ open of a subset of ≤ 5 items (0.00 tolerance) +25; R3 amount = open − available cash discount with discount date ≥ value_date +28; R4 payer bank account hash (MICR/IBAN) matches the customer's bank account +15; R5 payer name Jaro-Winkler ≥ 0.92 to customer or invoice account +10; R6 value_date within 7 days of due date +5; R7 short by < 1% or < 50.00 USD / 45.00 EUR +20 with a mandatory short-pay reason. Decision: auto-apply when exactly one set scores ≥ 85 and the runner-up ≤ winner − 20; 60–84 → SUGGESTED for one-click; < 60 → ON_ACCOUNT (1205) with top-3 suggestions kept. The ML ranker may reorder sets below 85 but cannot raise one above it. Every auto-application is reversible within the open period.
- **BR-30.2 Dunning levels and grace.** Level 1 Reminder: due date + grace days (default 5 per customer group) passed, no fee; Level 2: ≥ 15 days overdue, no fee; Level 3: ≥ 30 days, fee 25.00 USD / 20.00 EUR as open item FEE; Level 4: ≥ 45 days, fee 50.00 USD / 40.00 EUR, legal text, hold recommendation (sales applies CREDIT via CR-3). Rules: minimum letter amount 50.00 USD / 45.00 EUR per customer; ≥ 10 days since the last letter at any level; an item rises at most one level per run; letter level = max item level; items < 1.00 ignored; customers or items with dunning_block, open disputes or unbroken promises (promise date + 2 days) are excluded; a run is DRAFT until reviewed and issued, and only issue posts fees and levels; full payment resets the item, not the customer's history.
- **BR-30.3 RMA dispositions** (ReturnDispositionCode equivalents): CREDIT_ONLY — no physical return, credit at original price less restocking (default 0%); RETURN_TO_STOCK — receipt to the return warehouse at return cost (ORIGINAL_COST default), credit less restocking (default 15%, minimum 25.00 USD / 20.00 EUR); SCRAP — receipt then scrap issue to 5900, credit less restocking; REPAIR — receipt to quarantine, repair order hook (Phase 8), return to customer, no credit; REPLACE — receipt as RETURN_TO_STOCK or SCRAP plus a zero-price replacement order line referencing the RMA, credit offset by the replacement; REJECT_RETURN_TO_CUSTOMER — receipt to quarantine, ship back, no credit. Disposition is per line and immutable after credit; credit = qty received × original net unit price − restocking.
- **BR-30.4 Settlement accounting.** Cash discount within terms → Dr 4100; write-off within tolerance (≤ 5.00 or 0.5% of the item) → Dr 6100; FX difference on foreign-currency items → 7100/7200 at the settlement-date rate; partial settlement leaves PARTIALLY_SETTLED with reduced open_amount.
- **BR-30.5 Interest** = open_amount × annual_rate × days_overdue / 365 from due date + grace to the calculation date, once per item per period, excluded below the minimum and for disputed/promised items.
- **BR-30.6 State machines.** Payment: RECEIVED → SUGGESTED → APPLIED | ON_ACCOUNT → REVERSED; ON_ACCOUNT may later be applied. Dispute: OPEN → UNDER_REVIEW → RESOLVED_CREDIT | RESOLVED_PAID | RESOLVED_WRITE_OFF → CLOSED; REJECTED from OPEN/UNDER_REVIEW. RMA: REQUESTED → AUTHORIZED → RECEIVED → INSPECTED → DISPOSITIONED → CREDITED → CLOSED; REJECTED from REQUESTED; EXPIRED from AUTHORIZED after 30 days.
- **BR-30.7 Concurrency.** Settlement takes a row lock; a second clerk gets 409; imports deduplicated by SHA-256; camt.053 entries unique per (statement id, entry reference).
- **BR-30.8 Rebate settlement** runs after period close: amount = Σ accruals − prior settlements; a REBATE credit note posts Dr 2150 / Cr 1200; over-accrual posts a free-text invoice.
- **BR-30.9 Portal payments** create an intent for selected items; the signed webhook creates a PORTAL payment that auto-applies by R1 (score 100); refunds flow only through credit notes and the provider refund API with duty ARRefund.
- **BR-30.10 Statements** list items as of the statement date and never change balances; the portal reads the same `open-items` endpoint.
- **BR-30.11 SEPA collection date** = max(due date, today + pre-notification days, today + 1 TARGET2 business day); FRST and RCUR share D-1 submission since the 2016 rulebook; a mandate amended to a new debtor bank resets to FRST only if `smndr` indicator applies; an R-transaction within 8 weeks (CORE, no reason) or 13 months (unauthorized) reverses the DD payment, reopens the items and posts the bank return fee (default 3.50 EUR) as FEE.
- **BR-30.12 Deduction lifecycle.** OPEN → MATCHED (to a claim) → APPROVED (credit note posted) | DENIED (debit item re-billed) | SPLIT (children OPEN) | WRITTEN_OFF (Dr 6110 Deductions written off, ≤ `deduction_write_off_limit`, default 50.00 USD / 45.00 EUR); approval cannot exceed the claim's remaining amount; Σ children of a split = parent.
- **BR-30.13 Balance confirmation selection** uses the as-of balance in entity currency; customers with zero balance are included only when `include_zero_balances`; a disagreement creates a collection case of category DISPUTE with the customer's balance attached.
- **BR-30.14 Down payment clearing.** A final invoice for an order with down payments proposes clearing up to min(down payment remaining, invoice gross); tax on down payments posts at receipt only when the country pack sets `tax_on_down_payment` (for example DE, AT) and reverses at clearing.
- **BR-30.15 Chargeback validation.** A claim line is ACCEPTED when an ACTIVE w26 agreement exists for (end customer, product) at the claim's invoice date, contract price ≤ claimed price, and quantity ≤ the distributor's purchased quantity in the prior 12 months; otherwise REJECTED with codes CB-PRICE, CB-NOAGR, CB-QTY, CB-DUP.
- **BR-30.16 Correspondence** is rendered from a snapshot of the data at request time; re-sending reuses the snapshot unless `refresh=true`; every request is logged with its delivery result for 10 years.

### 5.30.8 Acceptance criteria
- **AC-30.1** (FR-30.3, FR-30.5) Given `cash-app-500`, when matching runs, then ≥ 450 payments auto-apply and zero apply to the wrong items versus the answer key.
- **AC-30.2** (FR-30.1) Given a 1,000.00 USD invoice at 2% 10 net 30 and 980.00 paid on day 8, then the item settles fully with 20.00 to 4100.
- **AC-30.3** (FR-30.7) Given 60 customers (5 blocked, 4 disputed, 3 with promises), when the run issues, then letters exist for exactly the 31 expected customers at expected levels, level-3 fees are 25.00, and disputed items appear on no letter.
- **AC-30.4** (FR-30.7) Given level 2 issued 2027-03-05, when a run executes 2027-03-12, then no letter (10-day interval).
- **AC-30.5** (FR-30.11) Given an RMA of 4 units at 100.00 USD with RETURN_TO_STOCK and 15% restocking, when received and dispositioned, then an inventory receipt of 4 at original cost exists and a credit note posts Dr 4000 400.00 / Cr 4200 Restocking 60.00 / Cr 1200 340.00 plus tax lines.
- **AC-30.6** (FR-30.11) Given SCRAP, then the receipt is followed by Dr 5900 / Cr 1400 at return cost.
- **AC-30.7** (FR-30.12) Given 10,000.00 accrued for 2027-03 on a 2% agreement, when settled, then a REBATE credit note of 10,000.00 posts and GL 2150 for the agreement is 0.00.
- **AC-30.8** (FR-30.14) Given an invited contact paying two invoices totalling 1,250.00 EUR by card in the Stripe sandbox, then a PORTAL payment is created from the webhook and both items are SETTLED within 60 s.
- **AC-30.9** (FR-30.9) Given a dispute on a 500.00 item, then dunning excludes it; when resolved as credit, a credit note posts and the item settles.
- **AC-30.10** (FR-30.15) Given `sim/phase5-month` on stage, then every §5.0 check passes and the reconciliation pack is produced and signed.
- **AC-30.11** (FR-30.16) Given DMF-DE customer C20 with an ACTIVE CORE mandate (FRST) and two items due 2027-03-20 totaling 2,380.00 EUR, when the DD run executes on 2027-03-05, then a pain.008 file with one FRST transaction of 2,380.00 and collection date 2027-03-20 validates against the XSD, a pre-notification is sent, and the mandate moves to RCUR after collection.
- **AC-30.12** (FR-30.16) Given that collection is returned with reason AC04, then the payment reverses, both items reopen and a 3.50 EUR FEE item exists.
- **AC-30.13** (FR-30.17) Given a payment short by 300.00 USD with reason "promo", when the deduction is matched to a promotion fund accrual of 300.00 and approved, then a credit note settles the deduction and the fund usage increases by 300.00.
- **AC-30.14** (FR-30.20) Given 20 customers selected for balance confirmation as of 2027-03-31, when 15 reply AGREED, 2 DISAGREED and 3 do not reply, then the difference list shows 2 rows with both balances and 2 DISPUTE cases exist.
- **AC-30.15** (FR-30.21) Given a 2,000.00 USD down payment on order SO-4000 and a final invoice of 5,000.00, then clearing posts Dr 2190 2,000.00 / Cr 1200 2,000.00 and the open item shows 3,000.00.
- **AC-30.16** (FR-30.22) Given a 10-line chargeback claim where 1 line has no agreement and 1 line duplicates a prior claim, then 8 lines are ACCEPTED, 2 REJECTED (CB-NOAGR, CB-DUP) and one credit note for the accepted total posts.
- **AC-30.17** (FR-30.19) Given a customer preference PAYMENT_NOTICE = EMAIL, when a payment applies, then a payment notice listing the settled items is emailed within 60 s and logged.

### 5.30.9 Test plan and fixtures
Fixtures: `cash-app-500` (300 BAI2 checks, 150 camt.053 entries, 50 remittances; short pays, discounts, multi-invoice, unidentified payers; answer key); `dunning-60`; `rma-30` (per disposition × costing method); `disputes-20`; `rebate-settle-12`; `portal-e2e` (Stripe and Adyen sandbox recordings); `sim/phase5-month` (generated month for DMF-US and DMF-DE: 200 POs, 150 production orders, 1,000 orders, 600 payments, 30 RMAs).

| Test | Type | Target | Pass criterion |
|---|---|---|---|
| Matching | unit | BR-30.1 | ≥ 90% auto-match, 0 false matches |
| Subset-sum bound | property | matcher | ≤ 5 items, < 50 ms per payment |
| Dunning | unit / E2E | run | exact letter set and levels |
| RMA E2E | E2E | sales + fulfillment + billing + inventory + ledger | 30/30 balanced |
| Settlement accounting | contract (ledger) | discount, write-off, FX | balanced |
| BAI2 / camt.053 parsers | unit / property | importers | spec samples parse; malformed rejected with line numbers |
| Portal | E2E (Playwright + axe) | order, pay, return | pass; 0 serious a11y violations |
| Month simulation | E2E + reconciliation | sim/phase5-month | all §5.0 checks; 31/31 daily reconciliations clean |

Reconciliation: Σ open_amount = GL 1200; Σ ON_ACCOUNT = GL 1205; daily settlements = GL 1000/1010 debits from receivables; fees = GL 4300; interest = GL 4310; RMA receipts = wave 16 receipts = GL 1400 debits from returns; rebate settlements = GL 2150 debits; deferred revenue, DNI, inventory, WIP, AP and tax checks re-run on simulation data; trial balance balanced.

### 5.30.10 Deployment and operations
Helm `passage-receivables` (Go), `passage-collections-workflows` (TS, Temporal namespace `collections`), `passage-customer-portal` (React + TS BFF; ingress `customer.<env>.passage.dev`; Keycloak realm `portal`), `passage-payments-gateway` (TS; provider keys in Vault; PCI scope limited to tokens), matcher ranker (Python, CPU). Databases `receivables-db`, `portal-db`. Config: `MATCH_AUTO_THRESHOLD=85`, `MATCH_MARGIN=20`, `DUNNING_INTERVAL_DAYS=10`, `RMA_EXPIRY_DAYS=30`, `WRITE_OFF_TOLERANCE=5.00/0.5%`. Flags: `ar.ml_ranker`, `ar.camt053`, `collections.interest`, `portal.payments`, `portal.returns`. Jobs: aging and risk score 01:30; lockbox/camt.053 SFTP poll every 15 min; dunning proposal weekly (issue manual); statements on the 1st; rebate settlement after period close; interest monthly. SLOs: open-items read p95 < 100 ms, apply p95 < 1 s, portal 99.9%, webhook processing < 60 s. Alerts: unapplied cash > 2% of monthly receipts for 3 days, webhook failures, reconciliation diff ≠ 0, run stuck in DRAFT > 7 days. Runbook: reverse a mis-application, re-import a file, re-issue a letter, rotate provider keys. Rollback: previous digest per deployable; Temporal workflows versioned.

### 5.30.11 Migration from AX / SAP

| Source | Passage entity | Notes |
|---|---|---|
| CustTrans + CustTransOpen (open); CustSettlement (current year) | open_item; settlement history | AmountCur/AmountMST → amounts; CollectionLetterCode → dunning_level |
| CustCollectionLetterJour (last), CustInterestJour (open) | dunning history; open_item INTEREST | |
| ReturnTable / SalesLine (ReturnStatus not Closed) | return_order | disposition codes → BR-30.3 |
| BSID (open), BSAD (current year) | open_item; settlement history | MANSP → dunning_block; MSCHL/MANST → level |
| MHNK/MHND (last run); UDM_DISPUTE (open); VBAK RE/LR (open) | dunning history; dispute; return_order | |
| KONA/VBOX open accruals | rebate_accrual opening | settled in Passage |

Validation: Σ open items per customer = legacy AR aging to the cent; open dispute and RMA counts equal; dunning levels per item equal. Not migrated: cleared items before the current fiscal year, collection notes (lakehouse), legacy statements, CustPaymModeTable/lockbox setup (manual).

### 5.30.12 Dependencies, risks, open questions
- Depends on: w29 invoices and credit notes; w28 receipts for RMAs; w27 credit profile and holds; w26 accruals; w19 AP payments (rebate payout); w16 inventory; w12 FX; w11 journal; w5 documents and Keycloak realms; Phases 3–4 for the simulation.

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| False auto-match in production | Low | High | threshold + margin, reversible, weekly audit sample of 50 |
| Dunning sent to disputed customers | Low | High | exclusions evaluated at issue time |
| RMA credit without receipt | Medium | Medium | CREDIT_ONLY requires duty ReturnCreditOnly and reason code |
| PCI scope creep in the portal | Low | High | hosted fields only; tokens stored; annual SAQ-A |
| Sign-off slips because accountant review is late | Medium | High | accountant engaged from wave 29 fixtures onward |
| Interim collection_case diverges from the wave 45 generic case model | Medium | Medium | collection_case fields are a subset of the w45 case schema; façade path kept; migration script owned by wave 45 |
| SEPA R-transactions reopen items after dunning or statements already issued | Medium | Low | reopened items restart dunning at their prior level; statement shows the reversal on the next cycle |

- Open questions: (1) Primary demo payment provider, Stripe or Adyen? Default Stripe sandbox; decide by wave 30 start. (2) Are dunning fees taxable in DMF-DE? Default untaxed in v1 pending the country pack (wave 48); decide at wave 30 mid-point. (3) Does the simulation include DMF-DE → DMF-US intercompany sales? Default no (wave 40); decide at wave 29 close.

### 5.30.13 Parameters and setup

| Parameter | Scope | Type | Default | Allowed values | Effect | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|---|
| automatic_settlement | legal entity | bool | true | true/false | Apply credits to oldest items automatically on post | AR parameters → Settlement → Automatic settlement | F.13 automatic clearing / OB74 clearing rules |
| settlement_priority | legal entity | list | DUE_DATE, CASH_DISCOUNT_DATE | ordered fields | Order of items for automatic settlement | AR parameters → Settlement → Settlement priority | OB74 clearing criteria |
| cash_discount_after_due | legal entity | bool | false | true/false | BR-30.4 discount eligibility | AR parameters → Settlement → Cash discount administration | OBA3 tolerance for cash discount |
| write_off_tolerance | legal entity / customer group | money + % | 5.00 / 0.5% | ≥ 0 | BR-30.4 | AR parameters → Settlement → Max. penny difference | OBA3 customer tolerance groups (T043G) |
| fx_settlement_enabled | legal entity | bool | true | true/false | cross-currency settlement | AR parameters → Settlement → Exchange rate at settlement | OB09 / OBA1 exchange-rate difference accounts |
| match_auto_threshold / match_margin | legal entity | int | 85 / 20 | 0–100 | BR-30.1 | Advanced bank reconciliation matching rules | FF_5 / FEBAN search strings, interpretation algorithm |
| aging_period_definition | legal entity | list | 0, 30, 60, 90, 180 | ascending days | FR-30.6 buckets | AR parameters → Collections → Aging period definition | S_ALR_87012168 day intervals |
| aging_basis | legal entity | enum | DUE_DATE | DUE_DATE, INVOICE_DATE | FR-30.6 | Aging "Aged by" | Net due date vs baseline |
| dunning_procedure | customer group | ref | STD-4 | dunning procedures | BR-30.2 levels, grace, fees | Collection letter sequence (CustCollectionLetterTable) | FBMP dunning procedure (T047) |
| dunning_interval_days / dunning_min_amount | dunning procedure | int / money | 10 / 50.00 USD / 45.00 EUR | ≥ 1 / ≥ 0 | BR-30.2 | Collection letter "Days" / minimum | FBMP dunning interval / minimum amounts |
| collection_letter_basis | legal entity | enum | TRANSACTION | TRANSACTION, CUSTOMER | Level per item vs per customer | AR parameters → Collections → "Collection letter from" | FBMP "Dunning by dunning area / level per item" |
| interest_rate / interest_basis / interest_min | legal entity, customer group | numeric / enum / money | 0 / ACT_365 / 5.00 | ACT_365, ACT_360 | BR-30.5 | Interest codes (CustInterest) | OB46 interest indicators, OB82 terms, OBAA |
| sepa_creditor_identifier | legal entity | text | — | ISO creditor id (e.g. DE98ZZZ09999999999) | FR-30.16 | Direct debit mandate setup → Creditor identifier | SEPA_CUST creditor id (FSEPA_M1 settings) |
| sepa_prenotification_days | legal entity | int | 14 | 2–14 | BR-30.11 | Mandate pre-notification | SEPA pre-notification (F110 / FSEPA) |
| sepa_mandate_expiry_months | tenant | int | 36 | 36 | FR-30.16 | Mandate expiry | SEPA_MANDATE validity |
| sepa_return_fee | legal entity | money | 3.50 EUR | ≥ 0 | BR-30.11 | Payment fees (CustPaymFee) | Return reason fee (OBWQ) |
| deduction_write_off_limit | legal entity | money | 50.00 USD / 45.00 EUR | ≥ 0 | BR-30.12 | Deduction workbench write-off limit | FSCM dispute auto-write-off (UDM_AUTO_WRITEOFF) |
| deduction_types / denial_reasons | legal entity | code list | PROMO, PRICE, SHORT_SHIP, DAMAGE, UNKNOWN / NOT_AUTHORIZED, EXPIRED, DUPLICATE | configurable | FR-30.17 | Deduction types, denial reasons (TAM setup) | Dispute reason codes (UDM attributes) |
| case_sla_hours | case category | int | COLLECTION 72 / DISPUTE 120 / DEDUCTION 240 | ≥ 1 | FR-30.18 | Case category SLA | FSCM dispute planned close date |
| correspondence_types | legal entity | table | 8 types (FR-30.19) | templates per language | FR-30.19 | Collections email templates; form setup | OB77 correspondence types (T048), OB78 programs |
| confirmation_threshold / reminder_days | legal entity | money / int | 10,000.00 / 14 | ≥ 0 / 1–60 | FR-30.20 | n/a | F.17 selection (balance limit) |
| down_payment_account / security_deposit_account | legal entity | account | 2190 / 2195 | chart | FR-30.21 | Customer prepayment posting profile | OBXR special G/L A / H reconciliation accounts |
| down_payment_reduces_exposure | legal entity | bool | true | true/false | BR-27.3 exposure | n/a | Credit management update (special G/L in credit exposure) |
| rma_expiry_days / restocking defaults | legal entity | int / % | 30 / 15% | 1–365 / 0–100 | FR-30.11, BR-30.3 | Return order parameters (return item number, days) | VOV8 RE validity; KP00 restocking condition |
| statement_cycle | customer | enum | MONTHLY | NONE, WEEKLY, MONTHLY, QUARTERLY | FR-30.13 | Customer account statement settings | KNB1-XAUSZ statement indicator (F.27) |
| portal_payment_providers | legal entity | set | STRIPE | STRIPE, ADYEN | FR-30.14 | Payment services (credit card processor) | Digital payments add-on provider |
| vendor_dunning_min | legal entity | money | 100.00 USD / 90.00 EUR | ≥ 0 | FR-30.23 | n/a | F150 vendor dunning minimum |

### 5.30.14 Inquiries and reports

| Name | Type | Filters & parameters | Columns / measures | Source projection | AX equivalent | SAP equivalent |
|---|---|---|---|---|---|---|
| Customer open items / line items | list inquiry | customer, status, due as-of, document type, special G/L | document, dates, amount, open, discount, level, dispute/promise flags | open_item | Customer transactions / open transactions (CustTransOpen) | FBL5N / FBL5H |
| Aged balances | operational report | entity, as-of, basis, buckets, customer group | current, 1–30 … > 180, total, % overdue | aging snapshot | SSRS Customer aging (Aged balances) | S_ALR_87012168 / S_ALR_87012178 |
| Customer balance list with credit limit | operational report | entity, as-of | balance, limit, utilization, overdue | open_item + credit_profile | SSRS Customer balance list with credit limit | S_ALR_87012172 / FD33 |
| Cash application worklist | list inquiry | source, status, score band, date | payment, payer, amount, candidates, score, residual | payment | Payment journal / CustOpenTrans settlement | FEBAN / FLB2 post-processing |
| Unapplied cash | KPI / list | entity, age | on-account amount, count, oldest | payment (ON_ACCOUNT) | Customer transactions not settled (payments) | FBL5N with payment on account |
| Dunning proposal and history | drill-down | run, customer, level | letters, items, fees, exclusions with reason | dunning_run, dunning_letter | Collection letter journal (CustCollectionLetterJour) | F150 log / MHNK / MHND |
| Interest notes | list inquiry | period, customer | item, days, rate, amount, status | interest_note | Interest notes (CustInterestJour) | F.24 / F.2B output |
| Disputes, promises and cases | list inquiry / KPI | owner, category, SLA status, reason | case, items, amount, age, SLA due, resolution | collection_case, dispute, promise | Collections workspace / Cases | UDM_SPECIALIST worklist, UDM_DISPUTE |
| Deductions | list inquiry / operational report | type, status, customer, age, claim match | deduction, amount, type, matched claim, decision | deduction | Deduction workbench / SSRS Deductions report | FSCM dispute case list (residual items) |
| Direct debit mandates and runs | list inquiry | status, sequence, scheme, last used | mandate ref, IBAN masked, scheme, sequence, last collection, expiry | dd_mandate, direct_debit_run | Direct debit mandate inquiry | FSEPA_M4 mandate display / F110 DD log |
| Customer account statement | operational report | customer, period, cycle | opening, transactions, closing, aging footer | open_item | SSRS Customer account statement | F.27 |
| Balance confirmations | operational report | key date, run | customer, our balance, reply, their balance, difference | balance_confirmation | n/a | F.17 / F.18 / F.99 reply list |
| Return orders | list inquiry | status, reason, disposition, customer | RMA, lines, received, disposition, credit | return_order | Return orders list (ReturnTable) / SSRS Return acknowledgment | VA05 type RE / advanced returns monitor (MSR_TRAC) |
| Chargeback claims | list inquiry | distributor, status, period | claim, lines accepted/rejected, amount | chargeback_claim | Rebate management claim inquiry | Settlement management claim list |
| DSO and collection effectiveness | KPI | entity, period | DSO, best possible DSO, CEI %, average days to pay | open_item history | Collections KPIs (D365 workspace) | FSCM collection KPIs |
| Lockbox / statement import log | list inquiry | source, date, status | file, hash, items, matched, errors | lockbox_batch | SSRS Lockbox report | FLB2 log / FEBKO / FEBEP |

### 5.30.15 Printed and electronic documents

| Document | Trigger | Layout elements | Formats | Numbering sequence | Legal / retention requirement |
|---|---|---|---|---|---|
| Customer account statement | monthly job (1st) / on demand / portal | header: customer, statement date, currency; lines: date, document, description, debit, credit, running balance; footer: aging buckets, remittance instructions | PDF, email, portal | none (statement date + customer) | informational; retain 7 years |
| Dunning / collection letter | dunning run :issue | header: level, letter date, customer, contact; lines: invoice, due date, days overdue, open amount, fee; footer: total due, payment instructions, legal text (level 4) | PDF, email, print, portal | `DL-` per entity | retain 7 years; level-4 text per country pack |
| Interest note | interest :post | lines: item, period, days, rate, interest | PDF, email | `IN-` gap-free (it is an open item document) | as invoice (10 years DE) |
| Payment notice / payment advice | payment applied (preference) | payment, date, settled items, discounts, residual | PDF, email | none | 7 years |
| Open item list / individual letter | correspondence request | template-driven | PDF, email | none | 7 years |
| Balance confirmation letter | confirmation run | key date, balance, open items, reply form / portal link | PDF, email | `BCF-` per run | audit evidence; retain 10 years |
| SEPA pre-notification | DD proposal | mandate ref, creditor id, amount, collection date | email, PDF | none | SEPA rulebook; retain 14 months minimum (with mandate) |
| SEPA direct debit file | DD run | GrpHdr, PmtInf per sequence type, DrctDbtTxInf with mandate data | pain.008.001.08 XML | message id per run | bank file; retain 10 years |
| SEPA mandate form | mandate creation | creditor, debtor, IBAN, BIC, mandate ref, scheme, signature | PDF (signed upload) | mandate ref | retain 14 months after last collection (rulebook), longer per w48 |
| RMA authorization / return acknowledgment | :authorize / receipt | RMA number, lines, reasons, ship-to return address, expiry, label | PDF, email, portal | `RMA-` | 7 years |
| Deduction decision (denial / approval) | workbench decision | deduction, claim, decision, reason | PDF, email | none (the re-bill is an invoice) | 7 years |
| Chargeback response | claim decision | lines with ACCEPT/REJECT and reason codes | CSV, EDI 849 (w35) | claim number from distributor | 7 years |

### 5.30.16 Periodic and batch jobs

| Job | Schedule default | Partitioning key | Idempotency / checkpoint | Typical runtime target | Failure handling | Monitor / alert |
|---|---|---|---|---|---|---|
| Aging snapshot and risk score | 01:30 daily | customer | key = (customer, date) | < 10 min per 100k customers | rerun | snapshot missing by 03:00 |
| Lockbox / camt.053 SFTP poll | every 15 min | bank account | SHA-256 of file; entry (statement id, ref) | < 2 min per 1,000 entries | quarantine malformed file with line numbers | import failure; unapplied > 2% for 3 days |
| Dunning proposal | weekly Monday 06:00 (issue manual) | legal entity | run id | < 10 min per 50k items | DRAFT retained | DRAFT > 7 days |
| Interest calculation | monthly last day 20:00 | legal entity | key = (item, period) | < 10 min | rerun idempotent | none calculated while overdue items exist |
| Statements | 1st of month 04:00 | customer | key = (customer, period) | < 30 min per 50k statements | retry delivery | undelivered > 1% |
| SEPA DD proposal and file | daily 07:00 (business days) | creditor id | run id; transaction end-to-end id | < 5 min per 20k items | file not released until reviewed | DD items due without active mandate |
| SEPA mandate expiry / R-transaction import | nightly 00:45; camt.054 with statements | mandate | (mandate, date); R-transaction ref | < 2 min | reverse idempotent | returns > 2% per run |
| Rebate settlement | after period close (w14 event) | agreement | key = (agreement, period) | < 10 min | per-agreement retry | accruals unsettled 5 days after close |
| Balance confirmation reminders | daily 08:00 | run | key = (run, customer, reminder n) | < 1 min | retry | runs with no replies after 30 days |
| Case SLA timers | Temporal timers per case | case | workflow id | n/a | escalation to manager | SLA breach count > 0 |
| AR to GL reconciliation | 02:15 daily | legal entity | key = (entity, date) | < 5 min | report | Σ open ≠ GL 1200; ON_ACCOUNT ≠ GL 1205 |
| RMA expiry | hourly | legal entity | status transition | < 1 min | retry | none |

### 5.30.17 Security duties and privileges

| Duty | Privileges (entry points) | Segregation-of-duties conflicts | Default roles |
|---|---|---|---|
| ARView | GET open-items, aging, statements; AR workspace | none | AR clerk, Collector, Controller, Auditor |
| ARPaymentPost | payments, :match, :apply, :on-account, :reverse | with InvoicePost (w29); with CustomerMaintain bank details (w7) | Cash application clerk |
| ARImport | lockbox / bank-statement imports | with ARPaymentPost when `import_apply_separation` | Treasury clerk |
| ARSettle | settlements:batch, write-off, discount | with CreditNotePost; write-off above tolerance needs ARWriteOffApprove | AR clerk |
| ARWriteOffApprove | write-offs above tolerance, deduction write-offs above limit | with ARSettle on the same item | AR manager |
| ARRefund | provider refunds, customer refunds via w19 | with ARPaymentPost; with VendorMaintain (w7) | AR manager |
| DunningRun | dunning-runs, :review, :issue, :cancel | with ARSettle (letter suppression via dunning_block is logged and reviewed) | Collections manager |
| InterestPost | interest-notes :calculate, :post | with DunningRun under four-eyes | AR manager |
| CollectionsMaintain | promises, disputes, cases, contact log | none | Collector |
| DeductionMaintain | deduction workbench match, split, deny | with RebateSettle (w26/w30) and FundMaintain for the same fund | Deductions analyst |
| DirectDebitRun | mandates, DD proposal, file release | with CustomerMaintain bank details; with ARPaymentPost | Treasury clerk |
| ReturnMaintain / ReturnCreditOnly | return-orders, :authorize, :disposition; CREDIT_ONLY | ReturnCreditOnly with WarehouseReceive | Customer service; Returns supervisor |
| RebateSettle | rebates/settlements | with RebateMaintain (w26) | AR manager |
| BalanceConfirmationRun | confirmation runs, reply recording | with ARSettle (auditor independence) | Controller / external auditor read-only |
| PortalCustomer | /portal/api/v1/* scoped to own accounts | n/a (external) | Invited customer contacts |

### 5.30.18 Message catalog

| Code | Severity | User text | Cause | Remediation | Raised by |
|---|---|---|---|---|---|
| AR-0001 | error | Settlements plus residual must equal the payment amount | apply validation | Adjust settlements or residual | §5.30.5 validation |
| AR-0002 | error | Item {doc} is locked by another user | BR-30.7 row lock | Retry later | BR-30.7 |
| AR-0003 | error | File already imported ({hash}) | duplicate import | none | BR-30.7 |
| AR-0004 | error | Line {n}: malformed BAI2 record type {type} | parser | Fix file and re-import | FR-30.3 |
| AR-0005 | warning | Payment placed on account; top suggestion score {score} | BR-30.1 < 60 | Apply manually | BR-30.1 |
| AR-0006 | error | Write-off {amount} exceeds tolerance {tol} | BR-30.4 | Request ARWriteOffApprove | BR-30.4 |
| AR-0007 | error | Dunning run must be REVIEWED before issue | FR-30.7 | Review the run | BR-30.2 |
| AR-0008 | info | {n} customers excluded: dispute, promise or block | BR-30.2 | none | BR-30.2 |
| AR-0009 | error | Disposition requires received quantity > 0 | §5.30.5 validation | Receive first | BR-30.3 |
| AR-0010 | error | Credit-only return requires reason code and ReturnCreditOnly | risk control | Add reason / duty | BR-30.3 |
| AR-0011 | error | No active SEPA mandate for {customer} / {iban_masked} | FR-30.16 | Activate mandate or change payment mode | BR-30.11 |
| AR-0012 | error | Mandate {ref} expired after 36 months without collection | FR-30.16 | Obtain a new mandate | FR-30.16 |
| AR-0013 | warning | Collection returned ({reason}); items reopened and fee posted | R-transaction | Contact customer | BR-30.11 |
| AR-0014 | error | Deduction approval {amount} exceeds claim remaining {remaining} | BR-30.12 | Split or deny remainder | FR-30.17 |
| AR-0015 | error | Chargeback line rejected: {code} | BR-30.15 | Correct claim and resubmit | FR-30.22 |
| AR-0016 | warning | Balance confirmation disagreed by {customer}: difference {amount} | BR-30.13 | Investigate case | FR-30.20 |
| AR-0017 | error | Down payment clearing exceeds remaining down payment {amount} | BR-30.14 | Reduce clearing | FR-30.21 |
| AR-0018 | error | Case SLA breached for {case} | FR-30.18 timer | Escalate | FR-30.18 |
| AR-0019 | error | Portal payment amount exceeds selected open items | BR-30.9 | Adjust selection | FR-30.14 |
| AR-0020 | error | Provider webhook signature invalid | payments-gateway | Check provider secret rotation | BR-30.9 |

### 5.30.19 Performance budgets and capacity

| Operation | Volume assumption | Latency target (p50 / p95 / p99) | Throughput target | Storage growth per year | Index / partition strategy | Degradation behavior under 3× load |
|---|---|---|---|---|---|---|
| Open-items read (FBL5N-style, 200 rows) | 500k per day; portal peak 100/s | 30 / 100 / 250 ms | 300/s | open items 30M rows × 500 B = 15 GB | (customer_id, status, due_date); partition by legal entity | portal served from read replica; staleness ≤ 5 s |
| Payment match (scored) | 30k per day | 20 / 50 / 120 ms | 100/s | match logs 3 GB | candidates via (customer, open_amount) | ML ranker bypassed; rule score only |
| Payment apply | 30k per day | 200 ms / 1 s / 2 s | 30/s | settlements partitioned by month, 5 GB | (open_item_id) | queue per customer; 409 on conflict |
| camt.053 / BAI2 import (5,000 entries) | 50 files per day | — / 60 s / 120 s | 100 entries/s | raw files object store 20 GB | (source, source_ref) unique | files processed sequentially per bank account |
| Dunning run (50k items) | weekly | — / 10 min / 20 min | 100 items/s | letters 1 GB + PDFs | (customer, dunning_level) | proposal only; issue throttled |
| SEPA DD file (20k transactions) | daily | — / 3 min / 6 min | 200 tx/s | 2 GB XML | (mandate, collection_date) | split into multiple PmtInf / files of 10k |
| Portal page (dashboard) | 50k sessions per day | 150 / 400 / 800 ms | 200 req/s | portal_db 2 GB | BFF cache 30 s | degrade to list views; payments remain available |
| Webhook to settled | 5k per day | 5 / 30 / 60 s | 20/s | — | provider event id unique | queue; SLO 60 s at p99 under 3× |

### 5.30.20 Localization and compliance hooks

- SEPA (EU/EEA): SDD Core and B2B rulebooks, pain.008.001.08 and pain.002 / camt.054 R-transactions; creditor identifier per country format; mandate retention 14 months after last collection; UK Bacs Direct Debit and US ACH debit (NACHA PPD/CCD with authorization records) are country-pack variants on the same mandate entity (w48).
- Dunning and interest: statutory late-payment interest (EU Directive 2011/7/EU: reference rate + 8 pp B2B; DE §288 BGB 40.00 EUR flat fee; FR 40.00 EUR indemnity and mandatory penalties text) set by country pack interest rules; dunning fee taxability (open question 2).
- Statements and confirmations: language, date and number formats per customer locale; balance confirmations support audit standards (ISA 505 external confirmations) with immutable replies.
- Bills of exchange (IT RiBa, ES pagaré, FR LCR, JP tegata) are not supported in v1; wave 36 adds treasury instruments and country packs enable them.
- Down payments: tax on advance payments (EU art. 65 VAT Directive) enabled per pack; DE requires a down-payment invoice with tax.
- Privacy and PCI: card data never touches Passage (SAQ-A); IBANs are stored encrypted and displayed masked; collection notes and case activities are personal data subject to w48 retention (7 years, then pseudonymized); portal contacts can export their data.
- Retention: open-item history and settlements 10 years (GoBD); dunning letters 7 years; R-transaction evidence 13 months minimum.
- Extension points: correspondence template slots, dunning procedure legal-text slot, interest rule provider, payment-file format registry (w19) and the chargeback EDI mapper (w35).

### 5.30.21 Coverage closure

| Register row (inventory section + AX form or SAP T-code) | Status before | Disposition | Note |
|---|---|---|---|
| AX §12.3.1 #18 — Customer → Direct debit mandates (SEPA, CustDirectDebitMandate) | PARTIAL-30 | ADDED as FR-30.16 | |
| AX §12.3.1 #19 — Collections → Cases (collection cases) | PARTIAL-30 | ADDED as FR-30.18 (minimal collection_case); generic case framework DEFERRED to wave 45 | migration of collection_case to generic cases in 45 |
| AX §12.3.2 #22 — Direct debit mandate inquiry | PARTIAL-30 | ADDED as FR-30.16 | §5.30.14 Direct debit mandates and runs |
| AX §12.3.3 #24 — Payments → Generate direct debit (pain.008) | PARTIAL-30 | ADDED as FR-30.16 | w19 ISO 20022 generator |
| AX §12.3.4 #27 — Direct debit mandate setup (SEPA creditor identifier) | PARTIAL-30 | ADDED as FR-30.16 | sepa_creditor_identifier |
| AX §12.3.4 #28 — Collections email templates (D365) | PARTIAL-30 | ADDED as FR-30.19 | email templates per correspondence type; SMTP transport is wave 4 |
| AX §12.18.1 #176 — Deduction workbench (TAMDeductionWorkbench) | PARTIAL-30 | ADDED as FR-30.17 | |
| AX §12.18.2 #178 — Deduction inquiry (open, matched) | PARTIAL-30 | ADDED as FR-30.17 | §5.30.14 Deductions |
| AX §12.18.2 #181 — SSRS Deductions report | PARTIAL-30 | ADDED as FR-30.17 | Deductions report export |
| AX §12.18.3 #182 — Match deductions to claims / Write off small balances | PARTIAL-30 | ADDED as FR-30.17 | BR-30.12 |
| AX §12.18.4 #187 — Deduction types / reason codes / denial reasons | PARTIAL-30 | ADDED as FR-30.17 | deduction_types, denial_reasons |
| AX §12.29.1 #233 — Cases list page / Case details (generic case management, needed by w30) | PARTIAL-45 | DEFERRED to wave 45 | FR-30.18 provides the interim entity; risk logged in §5.30.12 |
| AX §12.3.1 #8, §12.3.2 #10 / #12, §12.3.3 #13, §12.3.4 #14 — Bill of exchange journals, inquiry, documents, remittance/protest, setup | GAP (propose 36) | DEFERRED to wave 36 | treasury instruments theme |
| SAP §13.2 #29 — F150 dunning vendors with debit balances | PARTIAL-30 | ADDED as FR-30.23 | |
| SAP §13.3 #40 — FB12 / F.61 / F.64 request / print correspondence | PARTIAL-30 | ADDED as FR-30.19 | |
| SAP §13.3 #44 — SEPA mandate management (FSEPA_M1..M4) | PARTIAL-30 | ADDED as FR-30.16 | |
| SAP §13.3 #46 — OBXR special G/L customers (down payments, security deposits, BoE) | PARTIAL-30 | ADDED as FR-30.21 (down payments A/F, security deposits H); BoE (W) DEFERRED to wave 36 | |
| SAP §13.3 #48 — OB77 correspondence types / periodic account statements | PARTIAL-30 | ADDED as FR-30.19 | statements remain FR-30.13 |
| SAP §13.3 #49 — SEPA mandate customizing | PARTIAL-30 | ADDED as FR-30.16 | §5.30.13 SEPA parameters |
| SAP §13.32 #358 — Customer-facing configurator (web / SAP CPQ) | PARTIAL-30 | ADDED as FR-30.25 | |
| SAP §13.1 #9 — F.50 / F.52 G/L interest calculation | GAP (propose 30) | ADDED as FR-30.24 | intercompany pairing wave 40 |
| SAP §13.1 #19 — OB46 / OB82 / OBAA interest indicators | GAP (propose 30) | ADDED as FR-30.24 | interest_rate / interest_basis parameters |
| SAP §13.3 #43 — F.99 customer balance confirmation | GAP (propose 30) | ADDED as FR-30.20 | |
| SAP §13.1 #11 — F.97 / F.98 / F.99 balance confirmations (GL / AP / AR; propose 19, names w30) | GAP (propose 19) | ADDED as FR-30.20 for customers; vendor (F.98) and GL (F.97) DEFERRED to wave 19 | same run engine reused |
| SAP §13.20 #231 — Chargeback / ship-and-debit claims from distributors (EDI 844/849) | GAP (propose 30) | ADDED as FR-30.22 | EDI mapping in wave 35 |
| SAP §13.20 #235 — Chargeback / claim types | GAP (propose 30) | ADDED as FR-30.22 | configurable claim types |
| SAP §13.3 #39 — F-34 / F-36 / F-33 bill of exchange payment / discount (AR) | GAP (propose 36) | DEFERRED to wave 36 | |
| SAP §13.3 #41 — S_ALR_87012199 customer bill of exchange list | GAP (propose 36) | DEFERRED to wave 36 | |
| SAP §13.3 #45 — FBWD / FBWE AR bill of exchange presentation | GAP (propose 36) | DEFERRED to wave 36 | |
