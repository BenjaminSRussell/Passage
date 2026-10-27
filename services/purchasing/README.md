# purchasing

Passage purchasing service. Wave 17 (PSG-17), slices 1 to 8 of the procurement
context: purchase requisitions with amount/category approval routing,
consolidation of approved requisition lines into purchase orders, purchase orders
with persisted totals, the PO approval lifecycle with versioned change
management, purchase agreements with release tracking, trade agreement
price lookup with explain, the RFQ cycle (send, replies, compare, award to
purchase orders) with sealed bids and weighted scoring, invoicing plans on PO
lines, the subsequent price change (reprice) of confirmed POs, scheduling
agreements with firm/trade-off zones and FRC/JIT releases, and the web
workspace module at `apps/web/src/modules/purchasing`.

Full ticket scope and the list of what is not yet built are in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/purchasing/        entrypoint: dbx.Connect -> dbx.Migrate -> api.Register -> httpx.Serve
internal/domain/       pure rules: approval bands, trade agreement lookup, PO totals, reapproval,
                       PO state machine, version snapshots and RFC 6902 diffs, agreement releases (limits, validity, threshold), consolidation grouping,
                       RFQ state machine, reply ranking and award checks, sealed bids and weighted scoring,
                       invoicing plan generation and reproportioning, reprice planning,
                       scheduling agreements: zones (BR-17.13), schedule changes, FRC/JIT releases with cumulative quantities
internal/store/        Postgres repositories (pgx), every transaction via dbx.WithTenantTx
internal/api/          HTTP handlers under /v1/procurement, duties via authz.RequireDuty
internal/events/       event types and payloads (dev.passage.procurement.*)
migrations/            0001_init.sql (tables, sequences, RLS, outbox, idempotency, total trigger)
                       0002_agreements_consolidation.sql (purchase agreements, PO line and requisition line links)
                       0003_po_lifecycle.sql (PO versions, received/invoiced quantities on PO lines)
                       0004_rfq.sql (RFQs, lines, invitations, replies; PO line link to the awarded reply)
                       0005_rfq_sealed_scoring.sql (sealed flag and opening log, scoring criteria, vendor scores)
                       0006_invoicing_plan.sql (invoicing plans and their rows per PO line)
                       0007_reprice.sql (invoiced-line reprice hand-over mark, trade-priced line index)
                       0008_scheduling_agreement.sql (scheduling agreements, schedule lines, releases and release lines)
api/openapi.yaml       OpenAPI 3.1 for every route
events/asyncapi.yaml   AsyncAPI 3 for every event emitted
charts/purchasing/     Helm chart (probes on /healthz and /readyz, DATABASE_URL from a secret)
testdata/              approval matrix fixture (30 requisitions: 6 amounts across the 4 bands x 5 categories)
Dockerfile             distroless static image (build from the repo root)
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/purchasing
```

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START` (dev only), `HTTP_ADDR`
(default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`). The gateway
supplies `X-Passage-Tenant`, legal-entity scope, user and duties (the kernel tenant
middleware until JWT validation lands).

## Test

```
go vet ./... && go test ./...
```

Domain tests are pure (table-driven plus `rapid` property tests). `internal/api`
tests drive the real router over an in-memory store. `internal/store` tests use
`platform/go/testkit` and skip unless `PASSAGE_TEST_DATABASE_URL` is set. That
URL must name a role without SUPERUSER or BYPASSRLS (as in `services/org`): the
tables FORCE row-level security, and the legal-entity scope assertions (for
example in `TestRFQ_CreateReplayListAndTenantIsolation`) fail under a superuser,
which skips every policy. The
row-level-security test also needs `PASSAGE_TEST_APP_DATABASE_URL` (a non-owner
`passage_app` role) and skips without it.

## Routes (`/v1/procurement`; browser: `/api/v1/procurement` via the BFF)

| Method and path | Duty | Notes |
|---|---|---|
| POST `/requisitions` | RequisitionCreate | Idempotency-Key required; max 500 lines; blank estimates default from the preferred vendor's trade agreement, else 0 with warning PRC-REQ-003 |
| GET `/requisitions` | RequisitionCreate | Newest first; `status`, `mine=true`, `limit` (≤ 200), `cursor` |
| GET `/requisitions/{id}` | RequisitionCreate | ETag = version |
| POST `/requisitions/{id}:submit` | RequisitionCreate | Idempotency-Key and If-Match (version) required; requester only; routes approvers |
| POST `/purchase-orders` | PurchaseOrderMaintain | Idempotency-Key required; missing unit prices come from trade agreements |
| GET `/purchase-orders` | PurchaseOrderInquire | Newest first; `approvalStatus`, `vendorId`, `limit`, `cursor` |
| GET `/purchase-orders/{id}` | PurchaseOrderInquire | ETag = row version |
| PATCH `/purchase-orders/{id}` | PurchaseOrderMaintain | If-Match (row version) required; whole order as on create, `lineNo` names an existing line; see the change rules below |
| POST `/purchase-orders/{id}:submit` / `:cancel` | PurchaseOrderMaintain | Idempotency-Key and If-Match required |
| POST `/purchase-orders/{id}:approve` / `:reject` / `:confirm` | PurchaseOrderConfirm | Idempotency-Key and If-Match required; approve and reject stand in for the wave 4 approval task |
| POST `/purchase-orders/{id}:reprice` | PurchaseOrderInquire (preview) / PurchaseOrderConfirm (apply) | Subsequent price change on one CONFIRMED PO; `{"apply": false}` previews (ETag = row version); apply needs If-Match and Idempotency-Key |
| POST `/purchase-orders:reprice` | PurchaseOrderInquire (preview) / PurchaseOrderConfirm (apply) | The run over a page of CONFIRMED POs (`legalEntityId`, `vendorId`, `tradeAgreementId`, `limit` ≤ 100, `cursor`); apply needs an Idempotency-Key and writes each PO on its own |
| GET `/purchase-orders/{id}/versions` | PurchaseOrderInquire | Every stored version oldest first, a pending change included |
| GET `/purchase-orders/{id}/versions/{n}/diff` | PurchaseOrderInquire | RFC 6902 patch from version n−1 to n |
| GET `/purchase-orders/{id}/invoicing-plans` | PurchaseOrderInquire | Every invoicing plan of the PO in line order |
| GET `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | PurchaseOrderInquire | One line's plan; ETag = PO row version |
| PUT `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | PurchaseOrderMaintain | If-Match (PO row version) required; replaces the plan, bumps the row version (new ETag) |
| DELETE `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | PurchaseOrderMaintain | If-Match required; 204 with the new row version as ETag |
| GET `/requisition-lines` | PurchaseOrderMaintain | Consolidation queue: OPEN lines of APPROVED requisitions; `sourcing` = NEEDS_SOURCING (no preferred vendor) or HAS_VENDOR, `vendorId`, `limit`, `cursor` |
| POST `/requisitions:consolidate` | PurchaseOrderMaintain | Idempotency-Key required; one DRAFT PO per legal entity × vendor × currency × site × delivery window (`windowDays`, default 7, 1–60); `vendorId` per line overrides the preferred vendor |
| POST `/agreements` | AgreementMaintain | Idempotency-Key required; purchase agreement, EFFECTIVE unless `status` = DRAFT; max 500 lines |
| GET `/agreements` | AgreementMaintain | Newest first; `status`, `vendorId`, `limit`, `cursor`; each row carries `maxPctReleased` |
| GET `/agreements/{id}` | AgreementMaintain | Lines with released, remaining and % released, plus the PO lines released against them; ETag = version |
| POST `/agreements/{id}:activate` / `:hold` / `:close` | AgreementMaintain | Idempotency-Key and If-Match (version) required |
| GET `/pricing/lookup` | PurchaseOrderMaintain | Winner plus candidate explain list; `vendorGroup`/`productGroup` add memberships; `tie` = LATEST_FROM_DATE (default) or LOWEST_PRICE |
| POST `/trade-agreements` | TradeAgreementMaintain | PRICE relation only in this slice; PRC-TA-002 on an overlapping row |
| GET `/trade-agreements` | TradeAgreementMaintain | Newest first; `accountId`, `itemId`, `status`, `limit`, `cursor` |
| GET `/trade-agreements/{id}` | TradeAgreementMaintain | |
| POST `/rfqs` | RFQMaintain | Idempotency-Key required; DRAFT RFQ with lines (max 500), a future `replyDeadline`, `rankingRule` LOWEST_PRICE (default), LEAD_TIME or WEIGHTED_SCORE, `sealed`, and scoring `criteria` (name, weight; max 20; required for WEIGHTED_SCORE) |
| GET `/rfqs` | RFQMaintain or RFQAward | Newest first; `status` (comma list), `limit`, `cursor`; rows carry line, vendor and reply counts |
| GET `/rfqs/{id}` | RFQMaintain or RFQAward | Lines, invitations, replies, criteria and scores (replies and scores hidden while sealed); ETag = version |
| POST `/rfqs/{id}:send` / `:close` / `:cancel` | RFQMaintain | Idempotency-Key and If-Match (version) required; `:send` takes `vendorIds` |
| POST `/rfqs/{id}/replies` | RFQMaintain | Idempotency-Key required; one invited vendor's prices for some lines, in one currency; late lines come back with PRC-RFQ-003 warnings |
| GET `/rfqs/{id}/compare` | RFQMaintain or RFQAward | Ranking per line by the RFQ rule or `rule`, with each scored vendor's weighted score; late replies listed as excluded; 403 PRC-RFQ-004 for a sealed RFQ before its deadline |
| PUT `/rfqs/{id}/scores/{vendorId}` | RFQAward | If-Match (version) required; `scores` (criterion, 1–10) of an invited vendor; bumps the version |
| POST `/rfqs/{id}:award` | RFQAward | Idempotency-Key and If-Match required; `awards` (line, reply, override reason); answers the RFQ and the DRAFT POs created |
| POST `/scheduling-agreements` | AgreementMaintain | Idempotency-Key required; vendor × product × site, currency, unit and unit price, target quantity, validity, release `profile` (PRD defaults), schedule lines (max 500) |
| GET `/scheduling-agreements` | AgreementMaintain or PurchaseOrderInquire | Release monitor, newest first; `vendorId`, `productId`, `limit`, `cursor`; scheduled, received and open quantity, last release, next release due |
| GET `/scheduling-agreements/{id}` | AgreementMaintain or PurchaseOrderInquire | Schedule with each line's zone today; ETag = version |
| PUT `/scheduling-agreements/{id}/schedule` | AgreementMaintain | If-Match (version) required; the whole schedule (`lineNo` names a line, qty 0 removes it, no `lineNo` adds one); answers the agreement with trade-off `warnings` |
| POST `/scheduling-agreements/{id}:release` | AgreementMaintain | Idempotency-Key required; `kind` FRC or JIT, `runDate` (default today), `cumulativeReset`; 201 for a new release, 200 for the release of the same kind, date and schedule version |
| GET `/scheduling-agreements/{id}/releases` | AgreementMaintain or PurchaseOrderInquire | Release headers in release order |
| GET `/scheduling-agreements/{id}/releases/{releaseNo}` | AgreementMaintain or PurchaseOrderInquire | Release documentation with every line |

Writes for a legal entity outside the caller's `X-Passage-Legal-Entity` scope are
refused with AUTH-0003 before they reach the database (RLS would refuse them too).

Health: `GET /healthz` (kernel) and `GET /readyz` (pings Postgres, 503 when it is
down). Metrics: `GET :9090/metrics` (Prometheus text counters).

## Business rules implemented

- **Approval routing (FR-17.2).** Default bands: up to 1,000 auto-approve; up to
  10,000 manager; up to 100,000 manager and category owner; above that also CFO.
  Each distinct line category is routed on the requisition total and the most
  senior chain wins. The chain is stored with the policy version `builtin-1`. An
  empty chain approves the requisition on submit and writes `requisition.approved`.
- **Trade agreement lookup (FR-17.6).** The store loads only rows for the vendor,
  its groups or all vendors and the product, its groups or all products, valid on
  the date, in the currency and unit, active and not consignment. The domain drops
  rows outside the quantity break (`from_qty ≤ qty < to_qty`) and ranks the rest:
  specificity = account score (vendor 4, group 2, all 0) plus item score (product
  4, group 2, all 0); then the latest `valid_from`; then the lowest unit price
  (`tie=LOWEST_PRICE` swaps the last two). Every row considered is in `candidates`.
- **Overlap at activation (BR-17.3).** A new ACTIVE row with the same account, item,
  currency, unit, consignment flag and `valid_from` as an ACTIVE row whose quantity
  break overlaps is refused with PRC-TA-002.
- **Totals (FR-17.11, BR-17.5).** Line amounts are rounded half-even once from the
  exact qty × price ÷ price unit. Header totals are sums of rounded lines.
  `total = subtotal − discount_total + charges_total + tax_total` is checked by a
  deferred constraint trigger in the database.
- **Price source (FR-17.7).** Each PO line stores `MANUAL`, `AGREEMENT:<agreement
  line id>`, `TRADE_AGREEMENT:<id>`, `REQUISITION_ESTIMATE` or `RFQ_REPLY:<reply id>`; the price date is the
  order date (BR-17.3 default).
- **Consolidation (FR-17.3).** Approved requisition lines are grouped by legal
  entity, vendor (the buyer's choice, else the preferred vendor), currency, site and
  delivery window: a window opens at the earliest requested date of a stream and
  takes every line requested less than `windowDays` later. A line with no vendor is
  refused with PRC-CONS-002 and stays in the NEEDS_SOURCING queue. Each PO line is
  priced like a direct PO line and falls back to the requisition estimate; it keeps
  `requisitionLineId`, and the requisition line becomes CONSOLIDATED with the PO id
  and line number. A requisition whose lines are all ordered becomes CLOSED. Any PO
  (direct or consolidated) that names a requisition line claims it under a row lock:
  it must be OPEN on an APPROVED requisition of the same legal entity.
- **Purchase agreements (FR-17.5, BR-17.10).** Commitment types QUANTITY (product,
  unit, quantity), VALUE (product, amount), CATEGORY_VALUE (category, amount) and
  TOTAL_VALUE (amount); maximum enforcement HARD (default), SOFT or NONE. A PO line
  with `agreementLineId` releases against the line inside the PO transaction, with
  the line locked: the agreement must be EFFECTIVE (PRC-AGR-007), of the PO's legal
  entity, vendor and currency (PRC-AGR-006), the line's delivery date within
  validity (PRC-AGR-005), and a QUANTITY/VALUE line for the same product (and unit
  for QUANTITY, PRC-AGR-008). The draw adds the quantity and net line amount; the
  commitment type picks the measure. A HARD overrun refuses the whole PO
  (PRC-AGR-003) and a database check enforces `released ≤ committed` for HARD lines.
  Reaching 90 % of the commitment, or any SOFT overrun, writes
  `agreement.threshold.v1` and returns a PRC-AGR-004 warning on the PO. With
  `priceOverride`, the agreement line's unit price and discount price the PO line
  ahead of trade agreements.
- **PO approval lifecycle (FR-17.7, BR-17.2).** `DRAFT|REJECTED →:submit→ IN_REVIEW
  →:approve→ APPROVED →:confirm→ CONFIRMED`, `IN_REVIEW →:reject→ REJECTED`. Confirming
  anything but APPROVED is PRC-PO-008. `:cancel` is allowed from any status but
  FINALIZED and CANCELLED when no line has received or invoiced quantity; it cancels
  every line, gives agreement releases back, re-opens the requisition lines the PO
  ordered (and their CLOSED requisition) and writes `po.cancelled.v1`. Every action
  takes the row lock and checks If-Match (stale: 412 PRC-PO-009, BR-17.8).
- **Versions and change management (FR-17.8).** `:confirm` stores a snapshot of the
  commercial content (lines in line-number order with cancelled lines kept, charges,
  total) as version 1 and writes `po.confirmed.v1`. A PATCH before the first
  confirmation is a plain edit that returns the PO to DRAFT. After it, the edit is
  diffed against the confirmed snapshot as RFC 6902: no difference is a no-op; with
  change management on and a reapproval rule firing (default: quantity or price up
  more than 5 %, or a new line) the change is stored as the next version, unconfirmed,
  and the PO goes to IN_REVIEW; otherwise the version is confirmed at once and
  `po.confirmed.v1` plus `poversion.created.v1` are written. Re-approving and
  confirming a pending change writes the same two events. Editing a pending change
  back to the confirmed content drops it and returns the PO to CONFIRMED.
- **Change limits (BR-17.7, BR-17.9, BR-17.10).** A line left out of the PATCH is
  cancelled (it stays in later versions as CANCELLED) and returns what it held. No
  line may drop below its received quantity, nor below invoiced (PRC-PO-007).
  Vendor, currency and legal entity never change; site and warehouse not after the
  first confirmation. A line's requisition and agreement links cannot change and a
  new line cannot have them (PRC-PO-010); a line drawn on an agreement keeps its
  product, unit, quantity and amount, and a new delivery date must stay within the
  agreement validity (PRC-AGR-005). An existing line that omits `unitPrice` keeps its
  price and price source while its product and unit are unchanged.
- **RFQ cycle (FR-17.4, BR-17.11).** `DRAFT →:send→ SENT →first reply→ REPLIES_OPEN
  →:close→ CLOSED`, `REPLIES_OPEN|CLOSED →:award→ AWARDED`, `:cancel` from anything
  but AWARDED and CANCELLED; a wrong status is 409 PRC-RFQ-STATE and a stale If-Match
  409 PRC-VERSION-CONFLICT. Only invited vendors reply (PRC-RFQ-002), only while SENT
  or REPLIES_OPEN. A reply received after `replyDeadline` is stored with `late=true`
  and a PRC-RFQ-003 warning; replying again to a line replaces the vendor's reply,
  but a late reply cannot replace an on-time one (PRC-RFQ-006). The compare view
  ranks on-time replies per line by price per unit of measure (price ÷ price unit)
  then lead time (LOWEST_PRICE), or lead time then price (LEAD_TIME), with vendor
  and reply id as the last tie-breakers, and lists late replies as excluded. An
  award names one reply per line; the reply must answer that line and still be valid
  on the award day (PRC-RFQ-005), and a late reply needs an override reason, kept on
  the reply (PRC-RFQ-003). The award creates one DRAFT PO per winning vendor and
  currency: each line has the RFQ quantity and unit, the reply price and price unit
  (`RFQ_REPLY:<reply id>`, and `purchase_order_line.rfq_reply_id`), and is due today
  plus the reply lead time, or on the line's required date when that is later. The
  POs, the awarded replies, the AWARDED status and `rfq.awarded.v1` are written in
  one transaction; `:send` writes `rfq.sent.v1`.
- **Sealed bids and scoring (FR-17.19, BR-17.16).** An RFQ created with `sealed`
  hides its replies and scores until `replyDeadline`: reads answer
  `repliesSealed=true` with no replies (the `replyCount` still counts them), and
  compare, scoring and award are 403 PRC-RFQ-004. At the deadline they open; the
  first read or comparison after it stamps `openedAt`/`openedBy` (AC-17.14). A late
  reply to a sealed RFQ is refused (PRC-RFQ-007) instead of flagged. An RFQ may carry
  up to 20 scoring criteria (unique name, positive weight); holders of RFQAward score
  each invited vendor 1–10 per criterion while the RFQ is SENT, REPLIES_OPEN or
  CLOSED. A vendor's weighted score is Σ(weight × score) ÷ Σ weight to 4 decimals,
  an unscored criterion counting 0. WEIGHTED_SCORE ranks by it, highest first, an
  unscored vendor after every scored one, then by price and lead time; the other
  rules show it alongside.
- **Invoicing plans (FR-17.21, BR-17.17).** A PO line that is not cancelled, on a PO
  that is not cancelled or finalized (PRC-IP-003), may carry one plan. PERIODIC:
  `periods` rows of `amountPerPeriod` from `start`'s month, monthly or quarterly, on
  `billingDay` (default the start day; a day past a month's end falls on its last
  day); periods × amount must equal the line amount (PRC-IP-001). PARTIAL:
  milestones with non-decreasing dates, all by percent (Σ = 100; amounts shared to
  the currency's minor unit by largest remainder, so they sum to the line amount
  exactly; a milestone that rounds to nothing is PRC-IP-001) or all by amount (Σ =
  line amount). `settleWithoutReceipt` (default true) tells wave 18 to invoice due
  rows without a receipt. A PO edit that changes a line amount reproportions the
  plan in the same transaction: invoiced rows keep their amount, open rows share the
  rest in proportion to their amounts (rows that come to zero are dropped); a line
  amount below what is invoiced, or an increase with no open row, refuses the edit
  (PRC-IP-001). Cancelling the line or the PO removes its plan, unless a row is
  invoiced (PRC-IP-002), which also blocks replacing or deleting the plan. Plan
  writes take If-Match on the PO row version and bump it.
- **Subsequent price change (FR-17.22, BR-17.18, AC-17.16).** Only a CONFIRMED PO
  is repriced (PRC-PO-STATE otherwise). Each live line priced from a trade agreement
  (`TRADE_AGREEMENT:<id>`) is looked up again with the PO creation rules (FR-17.6) on
  the order date (BR-17.3) for the PO's vendor, the line's product, quantity, unit and
  currency. A line moves when the winning row's price or price unit differs; a
  `MANUAL` line never moves, a line drawn against a purchase agreement keeps its
  release amount (PRC-PO-010), and agreement, RFQ and estimate prices are left alone.
  A line with an invoiced quantity keeps the old price and is handed to payables:
  `invoiced` (and `invoiced_lines` on the event) carries the change on the invoiced
  quantity for the wave 18 subsequent debit/credit, and the line is marked with the
  row handed over so the same change is never sent twice. Lines with receipts but no
  invoice move (their GR/IR expectation follows the new price). Apply goes through
  the PO edit: the moved prices, re-priced amounts and totals become the next version,
  confirmed at once (`po.confirmed`, `poversion.created`) or IN_REVIEW when a
  reapproval rule fires, and invoicing plans follow the new line amounts; then
  `po.repriced.v1` is written in the same transaction. The run over many POs plans
  each PO and applies it in its own transaction at the row version it read; a
  failing PO is reported in its item and the others still apply, and the run's
  Idempotency-Key is claimed per PO so a retried page writes no PO twice.
- **Scheduling agreements (FR-17.15, BR-17.13, AC-17.11).** An agreement is one
  vendor × product × site with a unit price, a target quantity, validity and a
  release profile copied at create time (FRC 90 / JIT 14 / firm 14 / trade-off 28
  days, WEEKLY by default). Schedule lines carry a delivery date (inside the
  validity), a quantity and the cumulative received quantity (written by wave 18).
  A line dated on or before today + firm days is FIRM, on or before today +
  trade-off days TRADEOFF, otherwise FORECAST. Replacing the schedule judges each
  existing line in the zone of its current date: reducing, moving or removing a
  FIRM line needs a reason code (PRC-SA-001, stored on the line), a TRADEOFF
  reduction is accepted with a warning, and no line may drop below its receipts
  (PRC-SA-002). A release run (`:release`) snapshots the whole schedule in date
  order into release `REL-<agreement>-<n>` with per-line running cumulative
  released and received quantities, the zone on the run date and whether the line
  is inside the kind's horizon, and writes `sarelease.created.v1` in the same
  transaction. The run date must be inside the validity. A run with the same
  kind and run date on an unchanged schedule (same agreement version) returns the
  existing release, so a repeated job writes nothing. Cumulative released never
  goes below the previous release (PRC-SA-003) unless the run records a cumulative
  reset (`cumulativeReset`, kept on the release). The monitor's next release due
  is the validity start before any release, else the last run plus one release
  period, and is empty after the validity ends.
- **Idempotency.** Creates and custom methods store the key with a hash of the operation
  and body; a replay returns the original resource, a different body (or endpoint)
  with the same key is `idempotency_key_reuse`.

## Error codes

Problems are RFC 9457 with `code` set to the stable value below. Domain and
store codes are returned as the `code` field.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400/422 | Malformed body or invalid field (`errors` names the field) |
| AUTH-0001 | 401 | Missing tenant or user in the caller context |
| PRC-IDEM-001 | 400 | Idempotency-Key header missing |
| PRC-IFMATCH-001 | 428 | If-Match header missing on a custom method or PATCH |
| PRC-IFMATCH-002 | 400 | If-Match is not a version number |
| AUTH-0003 | 403 | Caller lacks the route's duty, or the legal entity is outside the caller's scope |
| PRC-REQ-002 | 422 | Requisition has more than 500 lines |
| PRC-REQ-003 | warning | Returned in `warnings` on create: no price found, estimate set to 0 |
| PRC-REQ-004 | 403 | Only the requester can submit the requisition |
| PRC-REQ-NOT-FOUND | 404 | Requisition not found |
| PRC-REQ-STATE | 409 | Requisition is not in a state that allows the action |
| PRC-PO-NOT-FOUND | 404 | Purchase order not found |
| PRC-PO-STATE | 422 | The PO's approval status does not allow the edit or action, or the line is cancelled |
| PRC-PO-007 | 422 | A line quantity would drop below received or invoiced |
| PRC-PO-008 | 422 | Only an APPROVED PO (with a pending change, after the first confirmation) can be confirmed |
| PRC-PO-009 | 412 | If-Match is not the PO's current row version |
| PRC-PO-010 | 422 | An edit changes a line's requisition or agreement link, adds a linked line, or changes an agreement line's product, unit, quantity or amount |
| PRC-PO-VERSION-NOT-FOUND | 404 | The PO has no such version |
| PRC-TA-NOT-FOUND | 404 | Trade agreement not found |
| PRC-TA-002 | 409 | Overlapping ACTIVE trade agreement row with equal specificity and valid_from |
| PRC-VERSION-CONFLICT | 409 | Requisition, agreement, scheduling agreement or RFQ is at another version than If-Match |
| PRC-PRICE-001 | 422 | No trade agreement price for the line; enter a unit price |
| PRC-PRICE-002 | 422 | Unknown tie rule |
| PRC-PO-TOTAL | 500 (COM-0500) | Database total identity violated; the trigger refuses the write and the client sees COM-0500. Should not occur because totals come from `domain.Price` |
| PRC-ACTION-001 | 404 | Unknown custom action on a requisition, purchase order, agreement, scheduling agreement or RFQ |
| PRC-REQ-005 | 422 | A PO line names a requisition line of another legal entity |
| PRC-REQ-LINE-NOT-FOUND | 404 | Requisition line not found |
| PRC-CONS-001 | 422 | Consolidation window outside 1–60 days |
| PRC-CONS-002 | 422 | A consolidation line has no vendor (needs sourcing) |
| PRC-AGR-NOT-FOUND | 404 | Purchase agreement or agreement line not found |
| PRC-AGR-STATE | 409 | Agreement status does not allow the action |
| PRC-AGR-002 | 422 | Agreement has more than 500 lines |
| PRC-AGR-003 | 422 | A release would exceed a HARD agreement line |
| PRC-AGR-004 | warning | Returned in `warnings` on PO create: agreement line at the threshold or over a SOFT limit |
| PRC-AGR-005 | 422 | PO delivery date outside agreement validity |
| PRC-AGR-006 | 422 | Agreement is for another legal entity, vendor or currency |
| PRC-AGR-007 | 422 | Agreement is not EFFECTIVE |
| PRC-AGR-008 | 422 | PO line product or unit does not match the agreement line |
| PRC-RFQ-NOT-FOUND | 404 | RFQ not found |
| PRC-RFQ-STATE | 409 | The RFQ's status does not allow the action or a reply |
| PRC-RFQ-002 | 422 | The replying vendor was not invited |
| PRC-RFQ-003 | warning / 422 | Warning on a reply after the deadline; 422 when such a reply is awarded without an override reason |
| PRC-RFQ-005 | 422 | Award rule: line not on the RFQ, line awarded twice, reply of another line, reply expired, or no awards |
| PRC-RFQ-006 | 422 | A late reply cannot replace the vendor's on-time reply to the line |
| PRC-RFQ-004 | 403 | A sealed RFQ's replies cannot be compared, scored or awarded before its reply deadline |
| PRC-RFQ-007 | 422 | A reply after the deadline to a sealed RFQ is rejected |
| PRC-IP-001 | 422 | Invoicing plan total ≠ line amount, percents not 100, a milestone rounding to nothing, or a line amount change the plan cannot absorb |
| PRC-IP-002 | 422 | The plan has invoiced rows: it cannot be replaced or removed, nor its line cancelled |
| PRC-IP-003 | 422 | The PO is cancelled or finalized, or the line is cancelled |
| PRC-IP-NOT-FOUND | 404 | The PO line has no invoicing plan |
| PRC-PO-LINE-NOT-FOUND | 404 | The PO has no line with that number |
| PRC-SA-001 | 422 | A firm-zone schedule line is reduced, moved or removed without a reason code |
| PRC-SA-002 | 422 | A schedule line would drop below the quantity already received against it |
| PRC-SA-003 | 422 | A release would lower cumulative released below the previous release without a cumulative reset |
| PRC-SA-NOT-FOUND | 404 | Scheduling agreement not found (or outside the caller's legal entity scope) |
| PRC-SA-REL-NOT-FOUND | 404 | The scheduling agreement has no release with that number |
| idempotency_key_reuse | 422 | Idempotency key reused with a different body or on another endpoint |

## Dependencies

Allowed set only: platform kernel, chi (through the kernel), pgx/v5,
shopspring/decimal, and `pgregory.net/rapid` for property tests (already in the
platform go.mod).

## Web module

`apps/web/src/modules/purchasing` gives the procurement workspace: requisition,
RFQ, purchase order, purchase agreement and trade agreement lists with create forms
and detail pages, the consolidation board, the price lookup with explain, sealed-bid
and scoring screens on the RFQ, the invoicing plan page per PO line, the price
change pages (one PO, and the run over many), and the scheduling agreement
monitor, create form, detail page (schedule editor, release run, releases) and
release documentation. See its README.
