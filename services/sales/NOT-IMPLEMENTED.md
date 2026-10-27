# NOT-IMPLEMENTED — sales (PSG-27, wave 27)

Slice 1 builds the order core: create (UI and channel intake), the order list
(with the credit release queue as `hold=CREDIT`), confirm with the credit check,
CREDIT holds and the release that confirms the order, the stateless cumulative
ATP check, the ATP domain for delivery-date control, credit profiles (list,
read, create, replace), and the web screens for all of these. Everything below
from the ticket's deliverables (1–9) and PRD §5.27 acceptance criteria is not
in this slice. Status is either a later slice of PSG-27 or a handoff to another
wave or team.

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Quotation → order conversion, quotation lifecycle (`:send`, `:convert`, expiry, probability) | later slice of PSG-27 | Quotations are a separate aggregate (FR-27.1); not started. `dev.passage.sales.quotation.converted.v1` not emitted. |
| 1 | Sales agreements, release orders, commitment decrement (FR-27.10, BR-27.9, AC-27.9 `AGREEMENT_EXHAUSTED`) | later slice of PSG-27 | Needs the agreement aggregate and atomic decrement; not started. |
| 1 | Order types JOURNAL / SUBSCRIPTION / RETURNED behaviour | later slice of PSG-27 | Types are accepted on create and stored, but JOURNAL no-reserve/no-ATP (BR-27.7) and the subscription/returned flows are not implemented. RETURNED order flow is wave 30. |
| 2 | Cumulative ATP on stored supply and demand (`atp_bucket` projection, lag < 30 s, planning batch-end marker) | later slice of PSG-27 | Only the pure cumulative/promise computation and a stateless `POST /v1/sales/atp:check` exist. The projection needs supply/demand events from w16/w17/w21/w22. |
| 2 | Delivery date control per line (NONE / SALES_LEAD_TIME / ATP / CTP; transport days; receiving calendar; past-date warning; fence-end TIME_FENCE) (FR-27.3, BR-27.2) | later slice of PSG-27 | Domain promise logic exists; per-line control, calendar and transport days are not wired into confirm. Calendar comes from w6/w48. |
| 2 | Schedule lines persisted per line (`order_schedule_line`, partial delivery split, AC-27.8 backorder list) | later slice of PSG-27 | Promise returns schedule lines; they are not stored and there is no backorder list endpoint. |
| 2 | Backorder handling and backorder list by promised date (FR-27.6) | later slice of PSG-27 | Depends on schedule lines above. |
| 2 | Alternative items (FR-27.13) | later slice of PSG-27 | Needs product configuration (w8) and an ATP fallback. |
| 2 | CTP via scheduling (FR-27.5, planned order link) | handoff | Needs the w23 finite-capacity CTP API. Not built here. |
| 3 | Credit limits, CR-1..CR-6, holds, release workflow | implemented in part | CR-1..CR-6 are implemented in the domain and run at confirm. Gaps are listed below. |
| 3 | CR-1 amounts converted to the profile currency at the wave 12 spot rate (BR-27.3) | handoff (w12 FX rates) | No rate source yet. An order in another currency than its profile fails CR-1 with a note, so a credit manager reviews it instead of the check comparing across currencies. CR-6 uses the order currency's threshold. |
| 3 | BR-27.3 "open AR + unapplied" | implemented as open AR − unapplied | Exposure amounts are stored non-negative, so unapplied cash (paid, not yet applied) is subtracted as a credit. Confirm the sign with the receivables owner when the w29/w30 projection replaces the PUT body. |
| 3 | Credit exposure from open AR, open items and unapplied cash (BR-27.3 inputs) | handoff | Open AR lives in `receivables_db.open_item` (w29/w30). Exposure is supplied in the credit-profile PUT body until that projection exists. |
| 3 | Credit check at entry, invoicing and release points; `credit_check_points` setting (FR-27.7) | later slice of PSG-27 | Only the confirm point runs. Entry, release-recheck and invoice points are not wired. |
| 3 | `credit-check.evaluated` event and stored evaluations for every check | implemented for the confirm point | Every confirm emits `creditcheck.evaluated` in the same transaction and stores the evaluation on the order (`credit_check_result`). Checks at other points are not built (next row). |
| 3 | Credit exposure recompute and nightly reconciliation (`exposure.open_orders`) | later slice of PSG-27 | Needs the order-amount projection. |
| 3 | Credit review warnings daily job (06:00), review_date handling | later slice of PSG-27 | Scheduled job not built; CR-4 review-date rule runs at confirm. |
| 4 | Holds: FRAUD / MANUAL / PRICE_REVIEW apply endpoint (`POST /orders/{id}/holds`) | later slice of PSG-27 | Only CREDIT holds are applied (by confirm). The apply endpoint and its duty (`sales.order.hold`) are not built. |
| 4 | Temporal release workflow with approvers by duty and 2-business-day SLA timer (FR-27.8, AC E2E "SLA timer fires") | later slice of PSG-27; handoff for the Temporal namespace `sales` (deployment-cicd) | Release is a synchronous API call. No workflow, no SLA timer, no escalation. |
| 4 | Release validity re-trigger on amount increase > 5% (BR-27.4) | implemented at confirm; re-trigger on change is later | A CREDIT release covers its own order only while it is valid (7 days) and the amount is within the released amount plus 5% (`domain.ReleaseCovers`). A change to the amount needs change management (next row). |
| 4 | Hold apply endpoint for MANUAL / PRICE_REVIEW / FRAUD, and MANUAL / PRICE_REVIEW blocking warehouse release | later slice of PSG-27; release blocking belongs with w28 | Release of an existing hold of these types is routed to `sales.order.hold`, but nothing places them yet. |
| 4 | Change management: PATCH with If-Match, version per change, field-level diff, selective re-pricing/ATP/credit, `order.changed` and `order.cancelled` events (FR-27.9, AC-27.7) | later slice of PSG-27 | PATCH, `:cancel`, `:cancel-remainder`, change requests and approval (`change_approval_required`) are not built. The order `version` column exists and is enforced by `If-Match` on confirm and release. |
| 4 | Line-level status machine beyond confirm (RESERVED / PICKED / DELIVERED / INVOICED) and order status derivation (BR-27.5) | later slice of PSG-27 | Confirm sets lines to OPEN with confirmed dates. Reservation and later states belong to w16/w28/w29. |
| 4 | Order status CANCELLED path | later slice of PSG-27 | Depends on the cancel endpoint above. |
| 5 | Customer portal order entry and status | handoff | Wave 30 (portal UI). The intake API it will use exists. |
| 5 | Multi-channel intake: `(channel, externalRef)` dedup (BR-27.10) | implemented in part | Same-key replay (200) and different payload (409 SLS-0007) are implemented and tested. The 30-day dedup window (`intake_dedup_window_days`) is not enforced; dedup currently has no expiry. |
| 5 | Intake rate limit per key (`INTAKE_RATE_LIMIT_PER_KEY=1000/min`, 429 with Retry-After) | later slice of PSG-27; handoff for shared rate-limit store (Valkey addendum, open question) | Needs the kernel rate-limit middleware; no per-pod limiter here. |
| 5 | ChannelIntake client credentials (service accounts) | handoff | Keycloak `_internal`/client registration belongs to edge-identity and w35 integration. The duty `sales.channel.intake` is enforced in code. |
| 6 | Supplementary items (FR-27.14, BR-27.14, AC-27.10) | later slice of PSG-27; handoff for w26 free-goods proposals | Needs the supplementary rule entity and the w26 FREE_GOODS proposal API. |
| 6 | Product listing / exclusion (FR-27.15, BR-27.13, AC-27.11 `PRODUCT_NOT_LISTED`) | later slice of PSG-27 | Needs the `product_listing` entity; not started. |
| 6 | Cross-selling and product proposals (FR-27.21) | later slice of PSG-27; handoff for w8 related products | Needs the proposal rule entity and w8 relations. |
| 7 | Product allocation quotas (FR-27.16, BR-27.15, AC for SLS-0013) | later slice of PSG-27 | Not started; `allocation_enabled` default false. |
| 7 | Backorder processing runs WIN / GAIN / REDISTRIBUTE / FILL / LOSE with preview (FR-27.17, BR-27.11, AC-27.12) | later slice of PSG-27 | Not started. The ordering and cumulative check it needs are in the domain. |
| 8 | Scheduling-agreement forecast/JIT releases and cumulative reconciliation (FR-27.20, BR-27.12, AC-27.13, `CUM_QTY_MISMATCH`) | later slice of PSG-27; handoff for DELFOR/DELJIT mapping (w35) | Not started. |
| 9 | Customer consignment fill-up / issue / pick-up / return (FR-27.18) | later slice of PSG-27; handoff for the w16 owner dimension (CUSTOMER_LOCATION) | Depends on w16 ownership dimension. |
| 9 | Sales-order special stock segregation (FR-27.19) | later slice of PSG-27; handoff for w16 reservation segment and w21 production flag | Depends on w16 reservations and w21 production orders. |
| 9 | One-time customer orders (FR-27.22, AC-27.14, `one_time_order_limit`) | later slice of PSG-27; handoff for the w7 ONE_TIME party role | Needs the party role and the document-level address on the order header. |
| 9 | Sales-order cost estimate hook `POST /orders/{id}/lines/{l}:estimate-cost` (FR-27.23) | handoff | Calls w20 cost snapshot and w39 simulation; neither API exists yet. Full costing is w39. |

## Functional requirements not covered above

| Requirement | Status | Reason |
|---|---|---|
| FR-27.11 configured products and make-to-order production link (AC-27.4) | handoff (w8 configuration_id, w21 production order) | Needs the w21 production order API to create the linked order on confirm. |
| FR-27.13 alternative items | later slice of PSG-27 | See deliverable 2. |
| FR-27.9 change approval per customer (`change_approval_required`) | later slice of PSG-27 | See deliverable 4. |
| BR-27.6 advisory lock per (product, dims, site, warehouse) around ATP consumption | later slice of PSG-27 | ATP is not yet consumed by confirm; the lock belongs with the projection. |
| BR-27.7 reservation on confirm (`reserve_on_confirm`) | handoff (w16) | Reservation API belongs to inventory. |
| BR-27.8 quantity rules (`whole_units`, unit conversion via w6, half-even) | later slice of PSG-27 | Decimal quantities are accepted at 6 places; whole-unit product flags and unit conversion (w6) are not applied. |
| Parameters from PRD §5.27.13 (default_order_type, quotation expiry, partial delivery, overdelivery, tolerance, release validity, SLA days) | partly built | `credit_release_validity_days` (7) and credit tolerance are in code as constants and profile fields. The legal-entity parameter table is not built. |

## Events (PRD §5.27.5)

| Event | Status | Reason |
|---|---|---|
| `dev.passage.sales.order.confirmed.v1` | implemented | Written to the outbox in the confirm transaction, or in the release transaction when the CREDIT release confirms the order. |
| `dev.passage.sales.orderhold.applied.v1` / `.released.v1` | implemented for CREDIT holds | Applied is emitted when confirm places a CREDIT hold; released is emitted on release. Other hold types wait for the apply endpoint. Type names follow the convention `dev.passage.<ctx>.<entity>.<verb>.v<n>` (`orderhold`), not the PRD's `passage.sales.order-hold.*`; see note below. |
| `dev.passage.sales.creditcheck.evaluated.v1` | implemented | Emitted on every confirm credit check with each rule's result, value and threshold. |
| `dev.passage.sales.order.changed.v1` / `cancelled.v1` | later slice of PSG-27 | Change management not built. |
| `dev.passage.sales.atp.promised.v1` | later slice of PSG-27 | Needs the projection and re-promise. |
| `dev.passage.sales.quotation.converted.v1` | later slice of PSG-27 | Quotations not built. |

Note: the PRD names the hold events `passage.sales.order-hold.*`. The
architecture convention (`dev.passage.<context>.<entity>.<verb>.v<n>`,
lowercase) is followed here. Confirm the name with the events owner before
consumers depend on it.

## Acceptance criteria (PRD §5.27.8 and ticket)

| Criterion | Status | Reason |
|---|---|---|
| AC-27.1 BR-27.1 fixture: 45 on 03-01 → 03-04, bucket 1 referenced | partly met | The fixture promise (domain test) is met. The promised event does not yet reference an `atp_bucket` id (no projection). |
| AC-27.2 `atp-100` fixtures: 100 scenarios match expected dates; never over-promises (property) | met for the domain | `testdata/atp-100.json` holds 100 cases (the first nine are the BR-27.1, AC-27.1 and AC-27.8 worked examples and bucket-boundary edge cases) with promise date, schedule and backorder computed by a separate oracle; `TestATPScenarios_100` checks every one, and a rapid property test checks that no promise exceeds ATP or precedes the requested date. The "all orders confirm" variant needs the projection. |
| AC-27.3 credit 10,000 / 8,500 / 2,000 WARN, 3,000 CREDIT hold, release confirms and emits `order-hold.released` | met | The WARN/FAIL outcomes are domain tests (and `testdata/credit-40.json`). The store test places the hold, refuses a release by the user who applied it, releases as a credit manager, sees the order CONFIRMED, and asserts the outbox holds `creditcheck.evaluated`, `orderhold.applied`, `orderhold.released` and `order.confirmed` (the last with the confirmed ship date). |
| AC-27.4 configured make-to-order with production linkage | handoff | See FR-27.11. |
| AC-27.5 intake 500 orders/min for 10 min, p99 < 500 ms, 5,000 exactly once | not run | The intake path and idempotency are implemented. The load test (k6, `orders-5k-intake` fixture) and the p99 measurement need a deployed database and are not part of this slice. |
| AC-27.6 quotation convert (lines 1 and 3) | later slice of PSG-27 | Quotations not built. |
| AC-27.7 change line 10 from 5 to 8 (version, diff, only line 10 re-promised) | later slice of PSG-27 | Change management not built. |
| AC-27.8 partial delivery 40/45 → two schedule lines, backorder 5 on 03-04 | partly met | The domain promise returns the two schedule lines and the backorder (tested). Persistence and the backorder list are not built. |
| AC-27.9 agreement exhausted → 422 `AGREEMENT_EXHAUSTED` | later slice of PSG-27 | Agreements not built. |
| AC-27.10 supplementary items | later slice of PSG-27 | See deliverable 6. |
| AC-27.11 listing denial `PRODUCT_NOT_LISTED` | later slice of PSG-27 | See deliverable 6. |
| AC-27.12 backorder run REDISTRIBUTE | later slice of PSG-27 | See deliverable 7. |
| AC-27.13 cumulative reconciliation `CUM_QTY_MISMATCH` | later slice of PSG-27 | See deliverable 8. |
| AC-27.14 one-time order and invoice address | later slice of PSG-27 / handoff (w29 invoice) | See deliverable 9. |
| PRD §5.27.9 "credit-40" fixture (40/40 outcomes incl. tolerance edges) | met | `testdata/credit-40.json`, checked by `TestEvaluateCredit_Credit40Fixtures`. |
| PRD §5.27.9 Playwright E2E + axe a11y; Pact contract tests with pricing/inventory/production | later slice of PSG-27 | The web pages have vitest tests that also run the in-repo WCAG checks (`checkAxeAA`); there is no Playwright in apps/web. Pact contracts need the w26/w16/w21 providers. |
| PRD §5.27.9 reconciliation: Σ open order amounts = `exposure.open_orders`; reservations = Σ RESERVED lines; ATP rebuild diff = 0 | later slice of PSG-27 | Needs the exposure recompute, reservations and projection. |
| Ticket AC6: `go vet ./... && go test ./...`; web vitest + tsc; OpenAPI/AsyncAPI match code; Helm lints | met | `TestOpenAPIMatchesRoutes` and `TestAsyncAPIMatchesEventTypes` fail if a route or event type is added without its contract entry, or the reverse. |

## Operations and deployment (PRD §5.27.10)

| Item | Status | Reason |
|---|---|---|
| HPA on RPS (`passage-sales`, 3 replicas) | later slice of PSG-27 | The chart has a fixed replica count. An HPA template needs a request-rate metric from the kernel metrics. |
| `sales-db` (CloudNativePG cluster) and secret provisioning | handoff (deployment-cicd / deploy/) | Outside the owned paths. The chart reads `DATABASE_URL` from secret `passage-sales-db`. |
| Temporal namespace `sales` | handoff (deployment-cicd, w4) | Not needed until the release workflow exists. |
| Config flags (`sales.ctp`, `sales.alternative_items`, `sales.change_approval`) and `ATP_*`, `CREDIT_CHECK_POINTS` | later slice of PSG-27 | Not read by the service yet. |
| Jobs: ATP rebuild 01:00, quotation expiry hourly, credit review daily, order auto-close, intake DLQ replay | later slice of PSG-27 | No jobs built. |
| SLO alerts (lag > 120 s, hold queue age, intake 5xx) and runbooks | handoff (deployment-cicd) | Alert rules live in deploy/ (outside owned paths). |

## Migration (PRD §5.27.11)

| Item | Status | Reason |
|---|---|---|
| AX / SAP extraction into sales_order, quotation, agreement, credit_profile | later slice of PSG-27 / handoff (migration-framework) | Migration packs belong to the migration framework; the target tables exist for the order and credit profile. |

## Web (`apps/web/src/modules/sales`)

| Item | Status | Reason |
|---|---|---|
| Sales orders list, order detail (confirm, release, credit rules), new order form, credit release queue, credit profiles list and form, ATP check | implemented | Routes under `/sales/...` registered in `module.ts`; pages use the shell design system with loading, empty and error states, and vitest tests in `pages.test.tsx`. |
| Sales workspace tiles (on hold, backorders, quotes expiring) | later slice of PSG-27 | Needs count endpoints, backorders and quotations. |
| Order entry grid with inline price and ATP (300 ms) | later slice of PSG-27 | Needs the pricing API (w26) and the projection. The new order form takes ids and prices as entered; customer, product and site pickers need the w7/w8/w6 lookups. |
| Credit exposure by customer report (PRD §5.27.14) | later slice of PSG-27 | The credit profiles list shows each profile's entered open AR; the computed exposure needs the projection. |
| Backorders / change requests screens | later slice of PSG-27 | See deliverables 2 and 4. |
| Customer portal order entry and status | handoff (wave 30) | Portal UI is wave 30. |
