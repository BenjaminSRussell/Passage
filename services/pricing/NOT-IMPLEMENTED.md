# NOT-IMPLEMENTED (PSG-26, wave 26, slice 1)

Every PRD deliverable and acceptance criterion from PSG-26 / PRD §5.26 that this slice does
not build. Status is either `later slice of PSG-26` (in this wave, next slices) or
`handoff` (needs another owner or a decision outside `services/pricing/` and
`apps/web/src/modules/pricing/`).

## Ticket deliverables

| # | Deliverable | Status | What is built / what is missing |
|---|---|---|---|
| D1 | Pricing engine: condition tables, access sequences, price/line/multiline/total discounts, surcharges, rounding rules | partial: later slice of PSG-26 | Built: PRICE (9-access sequence), LINE_DISC, TOTAL_DISC, search_next, exclusive flag, unit rounding. Missing: MULTILINE_DISC grouping (BR-26.4, FR-26.5), SURCHARGE and small-order threshold (FR-26.6), pricing_procedure and access_sequence_step tables and versions (FR-26.1), CEL requirements, exclusion groups BEST/FIRST (FR-26.8), GROSS total-discount basis, smart rounding wired into journals (SmartRound exists as a pure helper). |
| D2 | Promotions and campaigns: validity, customer/product groups, quantity breaks, currency-specific prices | partial: later slice of PSG-26 | Quantity breaks and currency-specific records are built. Missing: campaign entity, target groups, budget and kill switch (FR-26.10), customer/product group master data (needs waves 7 and 8). |
| D3 | Rebate agreements and accrual calculation (settlement in w30) | partial: later slice of PSG-26 | `RebateAccrual` tier math is built as a pure function (AC-26.8 and tier crossing). Missing: rebate_agreement and rebate_accrual tables, API, posting Dr 4150 / Cr 2150 via the w11 journal, invoice.posted consumer, retroactive recompute (FR-26.23, BR-26.8). Settlement is w30. |
| D4 | Price simulation and explain; mass price updates | partial: later slice of PSG-26 | Built: simulate and explain trace with sha256 reference. Missing: version comparison (needs procedure versions), explain store and `price_explain_ref` on lines, price_journal (FR-26.9): create, validate, post, reverse. |
| D5 | Commission calculation hooks | partial: later slice of PSG-26 | `CommissionAmount` and `SplitCommission` pure functions are built (AC-26.12 split and credit reversal). Missing: rules and resolution order (BR-26.12), `commission.calculated` event (FR-26.12), invoice.posted consumer. |
| D6 | Vendor rebate, broker and royalty agreements with accrual; period claims settled through AP (w19) | later slice of PSG-26 (claims: handoff to w19) | `RebateAccrual` covers the vendor tier math. Missing: party_role VENDOR on rebate_agreement, vendor-invoice consumer (2160/5100, AC-26.11), broker_contract (FR-26.16), royalty_agreement with minimum royalty (FR-26.17), claim statements `RC-`/`BC-`/`RY-`. Claim payment through AP is handoff to wave 19 (APInvoicePost / claim intake). |
| D7 | Commission transactions and posting: sales groups and reps with shares, commission customer and item groups, revenue or margin basis, settlement export | later slice of PSG-26 (payout: handoff to w19 / w43) | Share split is built. Missing: sales_group and rep assignment, commission_transaction table and posting Dr 6200 / Cr 2170, MARGIN basis (needs cost_snapshot from w20), settlement export. Payout is handoff to w43 (payroll) and w19 (AP). Rep names must never appear in events (PRD §5.26.20). |
| D8 | Trade promotion funds with budget and usage; promotion planning with baseline, lift and actuals | later slice of PSG-26 | Not started. Needs fund, fund_usage, promotion_plan, and the nightly actuals job (FR-26.19, FR-26.20, BR-26.15 with PRC-0011 and PRC-0012). |
| D9 | Free goods (inclusive and exclusive), commodity index pricing runs, retroactive rebate accrual with delta settlement | partial: later slice of PSG-26 | `FreeGoods` calculation is built (AC-26.13). Missing: FREE_GOODS condition type and the zero-priced line proposal for w27 (FR-26.22), commodity_index and price_calculation (FR-26.21), retroactive recompute and delta journal (FR-26.23, BR-26.14). |
| D10 | Deploy: Pricing workspace; demo customers and price lists configured | partial: later slice of PSG-26; seed data is handoff | Built: the web module (`apps/web/src/modules/pricing`) wired to the service: workspace tiles (active, drafts, expiring in 30 days), trade agreements grid with filters, create form (with copy), detail page with activate and withdraw, and the simulation / explain screen. Missing: the draft-journal, campaign and accrual tiles (those entities are not built), CSV export, multi-select bulk edit and Ctrl+D duplicate (§5.26.6 interaction rules), and demo customer and price-list seed data (a migration or fixture job; the migration owner must agree the seed path). |

## Acceptance criteria

| AC | Requirement | Status | Reason |
|---|---|---|---|
| AC1 | 500 scenarios including overlapping agreements resolve to the expected price and explanation | partial: later slice of PSG-26 | 19 of 500 hand-worked scenarios are in `testdata/pricing-scenarios.json` (AC-26.1, 26.2, 26.3 two cases, 26.5, access order, variants, search_next chains, inclusive validity, currency and unit filters, tie-breaks, amount-before-percentage, 100% discount, half-even rounding in USD and JPY, total discount, exclusive). Overlapping-activation cases are covered by `domain.Conflict` unit tests and the store tests, not by the fixture file. The other 481 need the remaining engine features in D1 and D2 (multiline, surcharges, exclusion groups, procedures) first. |
| AC2 | Engine is pure and deterministic; same output across 10k random requests (property test) | partial: later slice of PSG-26 | Built: `TestResolve_TenThousandRandomRequests` resolves 10,000 seeded random requests twice with the records in a different order and requires byte-identical explain output and reconciled totals; the rapid property test covers arbitrary record sets. Missing: the AC-26.4 comparison across two running replicas (needs the deployed service and a harness outside unit tests). |
| AC3 | p95 price resolution < 20 ms for a 10-line order | later slice of PSG-26 | No benchmark or load test yet. The in-memory record cache (FR-26.14, `PRICING_CACHE_TTL`) is not built. |
| AC4 | Mass update of 100k prices is atomic and reversible | later slice of PSG-26 | price_journal is not built (D4). |
| AC5 | Every deliverable has code under the owned paths or is listed here | met by this file | Every deliverable is either built or listed above. |
| AC6 | `go vet` / `go test` in the owned Go module; web `vitest` and `tsc`; OpenAPI and AsyncAPI match code; Helm lints | met for slice 1 | Go vet and tests pass (store tests also pass against Postgres with `PASSAGE_TEST_DATABASE_URL`). Web vitest and tsc pass. Helm lints. `internal/api/contract_test.go` checks every router route against `api/openapi.yaml`, and `internal/events/events_test.go` checks every event type and payload field against `events/asyncapi.yaml`. Request and response schemas are not generated from code. |

## Test plan items (PRD §5.26.9)

| Test | Status | Reason |
|---|---|---|
| Resolver scenarios: 500/500 price and trace | partial (19 of 500) | see AC1 |
| Determinism: identical across 10k runs and replicas | partial | see AC2 |
| Overlap rejection: 100% rejected with 409 | partial | Store/API tests cover activation overlap when `PASSAGE_TEST_DATABASE_URL` is set. The 100% target needs the full overlap fixture set. |
| Journal atomicity: failure leaves zero rows changed | later slice of PSG-26 | price_journal not built |
| Rebate accrual = GL 2150 reconciliation | later slice of PSG-26 (reconciliation: handoff to w11 ledger) | accruals not posted; the GL side belongs to w11 |
| Latency: 10-line resolve, 50 callers, p95 < 20 ms, p99 < 60 ms | later slice of PSG-26 | no load test yet |
| Sales ↔ pricing contract (Pact) | handoff | the consumer is w27 sales; a Pact broker and w27 consumer tests are outside this wave's paths |

## Engineering-convention gaps

| Gap | Status | Reason |
|---|---|---|
| `If-Match` on activate and withdraw; `ETag` on reads | later slice of PSG-26 | activate and withdraw carry `version` in the body (checked against the stored version, `conflict_version`). The header form is not implemented. |
| Event type entity segment `conditionrecord` vs PRD `condition-record` | handoff | Kernel naming decision (platform owner). The code uses `conditionrecord`, matching `internal/events`. |
| Events reach Kafka via the outbox router | handoff | Debezium outbox routing and topics belong to the eventing backbone; this service only writes the outbox row. |
| Duty enforcement via OPA (`pricing.view` / `pricing.maintain` in `policies/`) | handoff | The OPA policy bundle lives in `policies/`, outside this wave's paths. Until then the kernel's `authz` duty check runs locally. |
| Separate `PricingResolve` duty for `POST /resolve` (PRD §5.26.5) | later slice of PSG-26 | §5.26.17 defines no PricingResolve duty and gives the Simulation screen to PricingView, so resolve is guarded by `pricing.view` for now. A machine-caller duty comes with the w27 sales integration. |
| `journal_id` in the activated event payload | later slice of PSG-26 | Records are activated one by one; price journals (D4) do not exist yet, so there is no journal to name. |
| Manual price override with reason (BR-26.5, duty PricingOverride, PRC-0001 "unless a manual price exists") | handoff | The override is entered on the w27 sales order line; the engine reports PRC-0001 and the caller decides. |
| Parameters and setup (§5.26.13) | later slice of PSG-26 | Only `max_lines_per_resolve` and `explain_max_bytes` are read (env vars). The other parameters (pricing date basis, total discount basis, smart rounding bands per entity, reference procedure, rebate, commission, free goods, fund and commodity settings) need a parameter table. The caller supplies `pricingDate` (BR-26.2). |
| Inquiries, reports and documents (§5.26.14, §5.26.15) | later slice of PSG-26 | Built: the trade agreements list inquiry, expiring agreements tile and the price explain drill-down (simulation screen). Missing: price list by customer, price journal log, rebate, commission, claim, fund, promotion and commodity inquiries, and every printed document (they depend on entities not built yet and on w5 docgen). |
| Container image build (Dockerfile, CI, cosign) | handoff | Owned by deployment-cicd. The chart references `ghcr.io/benjaminsrussell/passage-pricing` by tag or digest. |
| Atlas migrations generated from metadata | later slice of PSG-26 | `migrations/0001_init.sql` is hand-written. Atlas wiring comes with the deployment pipeline. |
| Unit conversion to the record unit via w6 (PRC-0007) | handoff | Needs the wave 6 conversion table. Records must use the line unit until then. |
| Cross-currency fallback (`cross_currency_fallback`) | later slice of PSG-26 (open question 1, default no) | Default is no fallback, which the engine already enforces. |
| `PRICING_CACHE_TTL` and cache invalidation by `condition-record.activated` | later slice of PSG-26 | Set in the chart, not yet read. |
| Retention and pseudonymization for commission rows | handoff | w48 personal-data policy. |
| Temporal jobs: nightly expiry, rebate recompute, commission reconcile | later slice of PSG-26 | Not started. Expiry is a status transition by (id, version). |
| Pricing CSV import (≤ 100k rows) and procedure designer UI | later slice of PSG-26 | Depends on price_journal and procedures. |
| Migration of AX/SAP price data (§5.26.11) | handoff | Owned by the migration framework (`integration` DB). |
