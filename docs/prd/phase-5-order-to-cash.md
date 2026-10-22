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
| passage.sales.order.confirmed.v1 | sales | planning, fulfillment, credit, analytics | order, customer, lines with confirmed dates, totals |
| passage.sales.order-hold.applied.v1 / released.v1 | sales | fulfillment, collections, portal | order, hold type, reason, actor |
| passage.fulfillment.packing-slip.posted.v1 | fulfillment | billing, inventory, ledger, sales, portal | slip, lines, qty, cost snapshot, journal ref |
| passage.billing.invoice.posted.v1 | billing | receivables, revrec, ledger, pricing, portal | invoice, lines, tax, totals, due date |
| passage.revrec.schedule.recognized.v1 | revrec | ledger, analytics | schedule line, obligation, amount, period |
| passage.receivables.payment.applied.v1 | receivables | ledger, sales (credit), portal | payment, settlements[], residual |
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
Every order, quotation, invoice and rebate in Phase 5 depends on a price that is explainable and repeatable. Wave 26 ships the `pricing` service: condition records with validity and quantity breaks, access sequences resolving from the most specific key to the most general, line/multiline/total discounts, surcharges, exclusion groups, rounding rules, campaigns, rebate agreements with accrual, atomic price journals, a simulation/explain API and commission hooks. At wave end a pricing administrator maintains agreements in the Pricing workspace and any caller receives, for a customer/product/date/quantity/currency tuple, a net price plus the ordered list of conditions that produced it. Out of scope: rebate settlement (wave 30), vendor pricing (wave 17), configurator price components beyond attribute-keyed records, storefront promotions.

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
| passage.pricing.journal.posted.v1 | post / reverse | journal_id, line_count, reversed_journal_id | audit |
| passage.pricing.rebate-accrual.posted.v1 | invoice.posted consumed | agreement_id, period_id, invoice_line_id, amount, currency, journal_posting_id | ledger, receivables |
| passage.pricing.commission.calculated.v1 | invoice.posted consumed | invoice_line_id, sales_rep_id, basis, rate, amount | Phase 8 |

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

## 5.27 Wave 27: Sales Orders, Quotations, ATP & Credit

### 5.27.1 Scope and business context
Wave 27 ships the `sales` service: quotations with conversion, sales agreements, order types (journal, order, subscription, returned — completed in wave 30), delivery date control against cumulative ATP/CTP, credit management with limits, aging rules, holds and a Temporal release workflow, versioned change management, line-level status and a multi-channel intake API. A sales rep can quote, convert, promise a date and see holds; a credit manager releases orders. Out of scope: picking and shipping (wave 28), invoicing (wave 29), portal UI (wave 30), EDI intake (wave 35), aATP product allocation.

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
| passage.sales.order.confirmed.v1 | confirm | order_id, customer_id, currency, lines[{line_id, product, dims, qty, site, warehouse, confirmed_ship_date}], totals, version | fulfillment, planning (w22), credit, analytics |
| passage.sales.order.changed.v1 / cancelled.v1 | PATCH / cancel | order_id, version, diff[] or line_ids, reason | fulfillment, planning, portal |
| passage.sales.atp.promised.v1 | confirm with ATP/CTP | line_id, requested, confirmed, bucket_date, source | planning, portal |
| passage.sales.order-hold.applied.v1 / released.v1 | hold lifecycle | order_id, hold_type, reason, actor, workflow_run_id | fulfillment, collections, portal |
| passage.sales.credit-check.evaluated.v1 | any check | customer_id, order_id, rules[{id, result, value, threshold}], outcome | audit, collections |
| passage.sales.quotation.converted.v1 | convert | quotation_id, order_id, lines | analytics, CRM |

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

## 5.28 Wave 28: Fulfillment: Picking, Packing & Shipping

### 5.28.1 Scope and business context
Wave 28 ships the `fulfillment` service and the `carrier-gateway` adapter. Confirmed orders release to the warehouse by rule, are picked (basic wave/batch picking; location directives and RF arrive in wave 31), packed into containers with SSCC labels, rated and labelled through a carrier abstraction, and shipped with a posted packing slip that deducts inventory and posts cost to the universal journal. Drop-ship and partial shipments are supported; tracking is published for the portal. Out of scope: location directives, license plates and mobile RF flows (wave 31), transportation planning and freight reconciliation (wave 34), hazmat and export compliance.

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
| passage.fulfillment.shipment.released.v1 | release | shipment_id, order_id, warehouse, lines | sales, analytics |
| passage.fulfillment.pick.confirmed.v1 | pick confirm | wave_id, lines[{order_line_id, qty, dims, location}] | inventory, sales |
| passage.fulfillment.packing-slip.posted.v1 / cancelled.v1 | saga complete / cancel | packing_slip_id, order_id, lines[{order_line_id, qty, cost_amount, cost_snapshot_id}], journal_posting_id, posting_date | billing, sales, inventory, ledger, portal, credit |
| passage.fulfillment.asn.issued.v1 | post | shipment_id, containers[{sscc, lines}], carrier, tracking | EDI (w35) |
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

## 5.29 Wave 29: Billing, Invoicing & Revenue Recognition

### 5.29.1 Scope and business context
Wave 29 ships the `billing` and `revrec` services. Posted packing slips and eligible order lines become invoices (one-off, consolidated, subscription, milestone); free-text invoices and credit notes post directly; layouts render to PDF with an e-invoice document for wave 35. Every posted invoice creates AR open items, tax postings (w13) and journal entries and, where the contract has multiple performance obligations or deferred terms, a revenue contract with standalone selling price allocation and recognition schedules. Out of scope: cash application (wave 30), intercompany invoicing (hook only; wave 40), project billing (wave 41), Peppol transmission (wave 35), metering sources beyond API-posted usage.

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
| passage.billing.invoice.posted.v1 | post | invoice_id, number, type, customer, invoice_account, currency, dates, lines[{order_line_id, product, qty, net, tax, revenue_account, obligation_id}], totals, journal_posting_id, intercompany | receivables, revrec, ledger, pricing, portal, sales |
| passage.billing.credit-note.posted.v1 | post | credit_note_id, original_invoice_id, lines, totals, journal_posting_id | receivables, revrec, ledger, portal |
| passage.revrec.contract.allocated.v1 | allocate | contract_id, obligations[{id, ssp, allocated, method}] | analytics, audit |
| passage.revrec.schedule.recognized.v1 | run / event | schedule_id, obligation_id, period_id, amount, journal_posting_id | ledger, analytics |
| passage.billing.subscription.billed.v1 | daily job | subscription_id, invoice_id, period, proration | portal, analytics |

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

## 5.30 Wave 30: Accounts Receivable, Collections, Returns & Customer Portal

### 5.30.1 Scope and business context
Wave 30 closes the loop. The `receivables` service applies cash (manual, BAI2 lockbox, remittance advice, camt.053 with ML-assisted suggestions) and settles open items; `collections-workflows` (TS on Temporal) runs aging, dunning, interest notes, promises to pay, disputes and risk scoring; return orders flow through RMA dispositions to inventory and credit notes; wave 26 accruals settle; statements are produced; and the customer portal at `customer.<env>.passage.dev` exposes orders, invoices, payments (Stripe/Adyen sandbox), returns and documents. The wave ends with the Phase 5 exit: the demo manufacturer's one-month simulation with reconciled subledgers and stakeholder sign-off. Out of scope: non-customer bank reconciliation and cash forecasting (wave 36), collections-agency integration, portal self-registration beyond invited contacts.

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
| passage.receivables.payment.received.v1 | import / manual / portal | payment_id, source, amount, currency, payer, remittance[] | matcher, analytics |
| passage.receivables.payment.applied.v1 | apply | payment_id, settlements[{open_item_id, amount, discount, write_off, fx}], residual, journal_posting_id | ledger, sales credit, portal, collections |
| passage.receivables.customer-balance.changed.v1 | any open item change | customer_id, balance, overdue, oldest_item_days, dunning_level, risk_score | sales credit, portal |
| passage.receivables.dunning-letter.issued.v1 | issue | run_id, customer_id, level, items[], fee, document_ref | docgen, portal, sales (hold at level 4) |
| passage.receivables.dispute.opened.v1 / resolved.v1 | dispute lifecycle | dispute_id, open_item_id, amount, reason, resolution | collections, billing, analytics |
| passage.receivables.return-order.authorized.v1 / dispositioned.v1 | RMA | rma_id, line, disposition, qty, restocking, credit_note_id | fulfillment, inventory, billing, portal |
| passage.pricing.rebate.settled.v1 | settlement | agreement_id, period_id, amount, credit_note_id | ledger, receivables, analytics |

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

- Open questions: (1) Primary demo payment provider, Stripe or Adyen? Default Stripe sandbox; decide by wave 30 start. (2) Are dunning fees taxable in DMF-DE? Default untaxed in v1 pending the country pack (wave 48); decide at wave 30 mid-point. (3) Does the simulation include DMF-DE → DMF-US intercompany sales? Default no (wave 40); decide at wave 29 close.
