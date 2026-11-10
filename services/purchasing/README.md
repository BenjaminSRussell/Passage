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
                       0018_delivery_schedule.sql (delivery schedule rows per PO line)
                       0023_inbound_changes.sql (stable id on PO lines, consumer inbox, log of changes other services asked of POs)
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
| POST `/requisitions` | procurement.requisition.create | Idempotency-Key required; max 500 lines; blank estimates default from the preferred vendor's trade agreement, else from the last approved order of the product in the legal entity, currency and unit (warning PRC-REQ-012), else 0 with warning PRC-REQ-003 |
| GET `/requisitions` | procurement.requisition.create | Newest first; `status`, `mine=true`, `limit` (≤ 200), `cursor` |
| GET `/requisitions/{id}` | procurement.requisition.create or procurement.requisition.approve | ETag = version; legal entity scope applies (row-level security); `approvedSteps`, `nextApprover`, `history` |
| POST `/requisitions/{id}:submit` | procurement.requisition.create | Idempotency-Key and If-Match (version) required; requester only; routes approvers |
| POST `/requisitions/{id}:approve` | procurement.requisition.approve | Idempotency-Key and If-Match required; optional `{"comment"}`; approves the current level of the chain, APPROVED after the last (`requisition.approved.v1`); not the requester (PRC-REQ-009), not twice in one round (PRC-REQ-010), held to the approver's REQUISITION approval limit (BR-17.15, PRC-SL-001/002) and, at the CATEGORY_OWNER level, to the category managers once assigned (PRC-REQ-011) |
| POST `/requisitions/{id}:reject` | procurement.requisition.approve | As `:approve` with a required `{"comment"}`; IN_REVIEW to REJECTED; `requisition.rejected.v1`; not held to the approval limit |
| POST `/requisitions/{id}:reopen` | procurement.requisition.create | Requester only; REJECTED back to DRAFT with the chain cleared |
| POST `/requisitions/{id}:cancel` | procurement.requisition.create | Requester only; DRAFT, REJECTED or APPROVED (no line ordered and no running RFQ, else PRC-REQ-STATE / PRC-REQ-006) to CANCELLED, OPEN lines CANCELLED; `requisition.cancelled.v1` |
| PATCH `/requisitions/{id}` | procurement.requisition.create | Idempotency-Key and If-Match required; requester only; DRAFT only (PRC-REQ-STATE otherwise); `operatingUnitId`, `currency` (only with lines) and `lines` (replace all, priced as on create); EDITED in the history |
| POST `/requisitions/{id}:withdraw` | procurement.requisition.create | Requester only; IN_REVIEW back to DRAFT while no level has approved; clears the chain; `requisition.withdrawn.v1` |
| POST `/requisitions/{id}:cancel-line` | procurement.requisition.create | Requester only; body `{"lineId", "comment"}`; one OPEN line of an APPROVED requisition, not held by an RFQ (PRC-REQ-006), never the last live line; the total drops to the lines left; CLOSED when every line left is consolidated; `requisitionline.cancelled.v1` |
| GET `/requisition-approvals` | procurement.requisition.approve or procurement.purchase-order.inquire | The approver's inbox: IN_REVIEW requisitions with the role that decides next; `role` and `legalEntityId` filters; the caller's own requisitions are left out |
| POST `/purchase-orders` | procurement.purchase-order.maintain | Idempotency-Key required; missing unit prices come from trade agreements |
| GET `/purchase-orders` | procurement.purchase-order.inquire | Newest first; `approvalStatus`, `vendorId`, `limit`, `cursor` |
| GET `/purchase-orders/{id}` | procurement.purchase-order.inquire, or the service-only `procurement.purchase-order.lookup` (receiving's signed token, #305) | ETag = row version |
| PATCH `/purchase-orders/{id}` | procurement.purchase-order.maintain | If-Match (row version) required; whole order as on create, `lineNo` names an existing line; see the change rules below |
| GET `/purchase-order-lines` | procurement.purchase-order.inquire | Open supply: lines of CONFIRMED POs with quantity left to receive, by delivery date; `warehouseId`, `siteId`, `productId`, `legalEntityId`, `dueFrom`, `dueTo`, `limit`, `cursor` (opaque `<date>|<po id>|<line no>`). Master planning (wave 22) reads it per warehouse |
| GET `/inquiries/open-orders-by-vendor`, `/inquiries/overdue-lines`, `/inquiries/backordered-lines`, `/inquiries/expiring-agreements`, `/inquiries/near-limit-agreements`, `/inquiries/change-orders`, `/inquiries/price-history` | procurement.purchase-order.inquire | PRD 3.17.14 inquiries: open orders by vendor and currency; overdue lines (due before `asOf`); backordered lines (OPEN lines of CONFIRMED orders received in part, earliest due first, days late 0 before due); expiring agreements (EFFECTIVE or ON_HOLD agreements ending within `withinDays` of `asOf`, default 30, at most 366; lapsed ones have a negative `daysLeft`); near-limit agreements (lines of EFFECTIVE agreements released to `thresholdPct`, default 90, 50 to 100, of their commitment on their own measure, most released first, `overrun` past 100 %); change orders (IN_REVIEW POs with a confirmed version and an unconfirmed next version, longest waiting first, confirmed and proposed totals); order price history (`productId` required, `from`, `to`). Filters `legalEntityId` (in scope, else 403), `vendorId`, `productId`, `asOf`; `limit`, `cursor` |
| GET `/inbound-changes` | procurement.purchase-order.inquire | Changes other services asked of purchase orders (slice 18), newest first; `source` (PLANNING_MESSAGE, DROPSHIP_CLOSED), `status` (APPLIED, REFUSED, IGNORED), `purchaseOrderId`, `legalEntityId`, `limit`, `cursor` |
| GET `/inbound-changes/{id}` | procurement.purchase-order.inquire | One change |
| POST `/purchase-orders/{id}:submit` / `:cancel` | procurement.purchase-order.maintain | Idempotency-Key and If-Match required |
| POST `/purchase-orders/{id}:approve` / `:reject` / `:confirm` | procurement.purchase-order.confirm | Idempotency-Key and If-Match required; approve and reject stand in for the wave 4 approval task |
| POST `/purchase-orders/{id}:reprice` | procurement.purchase-order.inquire (preview) / procurement.purchase-order.confirm (apply) | Subsequent price change on one CONFIRMED PO; `{"apply": false}` previews (ETag = row version); apply needs If-Match and Idempotency-Key |
| POST `/purchase-orders:reprice` | procurement.purchase-order.inquire (preview) / procurement.purchase-order.confirm (apply) | The run over a page of CONFIRMED POs (`legalEntityId`, `vendorId`, `tradeAgreementId`, `limit` ≤ 100, `cursor`); apply needs an Idempotency-Key and writes each PO on its own |
| GET `/purchase-orders/{id}/versions` | procurement.purchase-order.inquire | Every stored version oldest first, a pending change included |
| GET `/purchase-orders/{id}/versions/{n}/diff` | procurement.purchase-order.inquire | RFC 6902 patch from version n−1 to n |
| GET `/purchase-orders/{id}/invoicing-plans` | procurement.purchase-order.inquire, or the service-only `procurement.purchase-order.lookup` | Every invoicing plan of the PO in line order |
| GET `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | procurement.purchase-order.inquire | One line's plan; ETag = PO row version |
| PUT `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | procurement.purchase-order.maintain | If-Match (PO row version) required; replaces the plan, bumps the row version (new ETag) |
| DELETE `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan` | procurement.purchase-order.maintain | If-Match required; 204 with the new row version as ETag |
| GET `/purchase-orders/{id}/delivery-schedules` | procurement.purchase-order.inquire | Every delivery schedule of the PO in line order |
| GET `/purchase-orders/{id}/lines/{lineNo}/delivery-schedule` | procurement.purchase-order.inquire | One line's schedule; ETag = PO row version |
| PUT `/purchase-orders/{id}/lines/{lineNo}/delivery-schedule` | procurement.purchase-order.maintain | If-Match (PO row version) required; replaces the rows on a DRAFT or REJECTED PO; Σ qty = line qty (PRC-DS-001) |
| DELETE `/purchase-orders/{id}/lines/{lineNo}/delivery-schedule` | procurement.purchase-order.maintain | If-Match required; 204 with the new row version as ETag |
| PUT `/purchase-orders/{id}/lines/{lineNo}/delivery-schedule/rows/{seq}/confirmation` | procurement.purchase-order.maintain | If-Match required; the vendor's confirmed date on a CONFIRMED PO; writes `deliveryschedule.confirmed.v1`; the same date again is a no-op |
| GET `/purchase-orders/{id}/service-lines` | procurement.purchase-order.inquire or the lookup duty | The service lines of the PO with outlines and limits, in line order |
| GET `/purchase-orders/{id}/lines/{lineNo}/service` | procurement.purchase-order.inquire or the lookup duty | One line's service specification; ETag = PO row version; 404 PRC-SRV-NOT-FOUND for a plain line |
| PUT `/purchase-orders/{id}/lines/{lineNo}/service` | procurement.purchase-order.maintain | If-Match (PO row version) required; saves the specification on a DRAFT or REJECTED PO (PRC-SRV-001): value limit, expected value, optional contract agreement line, and an outline tree or a `modelId` to copy; writes `servicespec.changed.v1` |
| DELETE `/purchase-orders/{id}/lines/{lineNo}/service` | procurement.purchase-order.maintain | If-Match required; 204 with the new row version as ETag; writes `servicespec.changed.v1` with `removed` |
| GET `/purchase-orders/{id}/lines/{lineNo}/service/limit-check?unplanned=&entry=` | procurement.purchase-order.inquire or the lookup duty | Whether an entry of unplanned services fits the value limit and the contract limit (BR-17.19); a refusal is 200 with `allowed` false and `code` PRC-SRV-003, with `remaining` (what can still be entered) either way; changes nothing (wave 18 asks for a service entry sheet) |
| POST `/model-service-specs`, GET `/model-service-specs`, GET `/model-service-specs/{id}`, PUT `/model-service-specs/{id}` | procurement.policy.maintain (reads also procurement.purchase-order.inquire) | Model service specifications (FR-17.23, SAP ML10-ML12): a reusable outline in one currency per legal entity; POST needs an Idempotency-Key (409 PRC-SRV-006 for a duplicate code), PUT needs If-Match (412 PRC-SRV-007); writes `modelservicespec.created.v1` and `modelservicespec.changed.v1` |
| PUT `/purchase-orders/{id}/lines/{lineNo}/invoicing-plan/rows/{seq}/settlement` | `procurement.invoicing-plan.settle` | Receiving's service token only (#289; 403 for a person, and identity refuses to assign the service-only duty). Body `settlementRef` and `amount`; flips an OPEN row to INVOICED at the settled amount (a reprice in between is absorbed by the other OPEN rows), idempotent per row and reference, records who settled it in `purchase_invoicing_plan_row_settlement`; bumps the PO row version |
| GET `/requisition-lines` | procurement.purchase-order.maintain | Consolidation queue: OPEN lines of APPROVED requisitions; `sourcing` = NEEDS_SOURCING (no preferred vendor) or HAS_VENDOR, `vendorId`, `legalEntityId`, `limit`, `cursor` |
| POST `/requisition-lines/{id}:split` | procurement.purchase-order.maintain | Idempotency-Key and If-Match (the requisition version) required; body `{"quantities": [...]}` cuts one new OPEN line per quantity out of an OPEN line of an APPROVED requisition, the original keeps the rest (FR-17.3); `requisition.split.v1` |
| POST `/requisitions:consolidate` | procurement.purchase-order.maintain | Idempotency-Key required; one DRAFT PO per legal entity × vendor × currency × site × delivery window (`windowDays`, default 7, 1–60); `vendorId` per line overrides the preferred vendor |
| POST `/requisition-releases` | procurement.purchase-order.maintain | Idempotency-Key required; body `{legalEntityId, windowDays (default 7, 1-60), maxLines (default 200, 1-500)}`. Release run (PRD 3.17.16): consolidates the OPEN lines of APPROVED requisitions of the legal entity that have a vendor, oldest first up to `maxLines`, one vendor at a time (lines with no vendor are listed up to the same number and do not count against it); lines with no vendor and vendors whose POs cannot be made stay in the queue and are listed as skipped with their code; records the run (EMPTY, COMPLETED, PARTIAL, FAILED); `requisitionrelease.completed.v1` |
| GET `/requisition-releases`, GET `/requisition-releases/{id}` | procurement.purchase-order.inquire | Release runs newest first (`legalEntityId`, `status`, `limit`, `cursor`) and one run with its orders, skipped lines and warnings |
| POST `/agreements` | procurement.agreement.maintain | Idempotency-Key required; purchase agreement, EFFECTIVE unless `status` = DRAFT; max 500 lines |
| GET `/agreements` | procurement.agreement.maintain | Newest first; `status`, `vendorId`, `limit`, `cursor`; each row carries `maxPctReleased` |
| GET `/agreements/{id}` | procurement.agreement.maintain | Lines with released, remaining and % released, plus the PO lines released against them; ETag = version |
| POST `/agreements/{id}:activate` / `:hold` / `:close` | procurement.agreement.maintain | Idempotency-Key and If-Match (version) required |
| GET `/pricing/lookup` | procurement.purchase-order.maintain | Winner plus candidate explain list; `vendorGroup`/`productGroup` add memberships; `tie` = LATEST_FROM_DATE (default) or LOWEST_PRICE |
| POST `/trade-agreements` | procurement.trade-agreement.maintain | PRICE relation only in this slice; PRC-TA-002 on an overlapping row |
| GET `/trade-agreements` | procurement.trade-agreement.maintain | Newest first; `accountId`, `itemId`, `status`, `limit`, `cursor` |
| GET `/trade-agreements/{id}` | procurement.trade-agreement.maintain | |
| POST `/rfqs` | procurement.rfq.maintain | Idempotency-Key required; DRAFT RFQ with lines (max 500), a future `replyDeadline`, `rankingRule` LOWEST_PRICE (default), LEAD_TIME or WEIGHTED_SCORE, `sealed`, and scoring `criteria` (name, weight; max 20; required for WEIGHTED_SCORE) |
| GET `/rfqs` | procurement.rfq.maintain or procurement.rfq.award | Newest first; `status` (comma list), `limit`, `cursor`; rows carry line, vendor and reply counts |
| GET `/rfqs/{id}` | procurement.rfq.maintain or procurement.rfq.award | Lines, invitations, replies, criteria and scores (replies and scores hidden while sealed); ETag = version |
| POST `/rfqs/{id}:send` / `:close` / `:cancel` | procurement.rfq.maintain | Idempotency-Key and If-Match (version) required; `:send` takes `vendorIds` |
| POST `/rfqs/{id}/replies` | procurement.rfq.maintain | Idempotency-Key required; one invited vendor's prices for some lines, in one currency; late lines come back with PRC-RFQ-003 warnings |
| GET `/rfqs/{id}/compare` | procurement.rfq.maintain or procurement.rfq.award | Ranking per line by the RFQ rule or `rule`, with each scored vendor's weighted score; late replies listed as excluded; 403 PRC-RFQ-004 for a sealed RFQ before its deadline |
| PUT `/rfqs/{id}/scores/{vendorId}` | procurement.rfq.award | If-Match (version) required; `scores` (criterion, 1–10) of an invited vendor; bumps the version |
| POST `/rfqs/{id}:award` | procurement.rfq.award | Idempotency-Key and If-Match required; `awards` (line, reply, override reason); answers the RFQ and the DRAFT POs created |
| POST `/scheduling-agreements` | procurement.agreement.maintain | Idempotency-Key required; vendor × product × site, currency, unit and unit price, target quantity, validity, release `profile` (PRD defaults), schedule lines (max 500) |
| GET `/scheduling-agreements` | procurement.agreement.maintain or procurement.purchase-order.inquire | Release monitor, newest first; `vendorId`, `productId`, `limit`, `cursor`; scheduled, received and open quantity, last release, next release due |
| GET `/scheduling-agreements/{id}` | procurement.agreement.maintain or procurement.purchase-order.inquire | Schedule with each line's zone today; ETag = version |
| PUT `/scheduling-agreements/{id}/schedule` | procurement.agreement.maintain | If-Match (version) required; the whole schedule (`lineNo` names a line, qty 0 removes it, no `lineNo` adds one); answers the agreement with trade-off `warnings` |
| POST `/scheduling-agreements/{id}:release` | procurement.agreement.maintain | Idempotency-Key required; `kind` FRC or JIT, `runDate` (default today), `cumulativeReset`; 201 for a new release, 200 for the release of the same kind, date and schedule version |
| GET `/scheduling-agreements/{id}/releases` | procurement.agreement.maintain or procurement.purchase-order.inquire | Release headers in release order |
| GET `/scheduling-agreements/{id}/releases/{releaseNo}` | procurement.agreement.maintain or procurement.purchase-order.inquire | Release documentation with every line |
| GET `/purchasing-policies` | procurement.policy.maintain or procurement.purchase-order.inquire | Saved purchasing policies of the caller's legal entities |
| GET `/purchasing-policies/{legalEntityId}` | procurement.policy.maintain or procurement.purchase-order.inquire | The policy, or the defaults at version 0; ETag is the version |
| PUT `/purchasing-policies/{legalEntityId}` | procurement.policy.maintain | If-Match required (0 creates); body has all three flags; writes `policy.changed.v1`; no Idempotency-Key (PUT is idempotent) |
| POST `/catalogs` | procurement.catalog.maintain | Idempotency-Key required; creates a DRAFT catalog with optional entries; ETag is the version |
| GET `/catalogs`, `/catalogs/{id}` | procurement.catalog.maintain or procurement.purchase-order.inquire | List (`status`, `legalEntityId`) and read with the entry count and the first 200 entries (`entriesNextCursor` when there are more) |
| GET `/catalogs/{id}/entries` | procurement.catalog.maintain or procurement.purchase-order.inquire | The entries in entry order, a page at a time (`cursor` is the last entry number, `limit` up to 200) |
| POST `/catalogs/{id}:publish`, `:retire` | procurement.catalog.maintain | Idempotency-Key and If-Match required; publish needs at least one entry (PRC-CAT-001); writes `catalog.published.v1` or `catalog.retired.v1` |
| POST `/catalogs/{id}/entries`, DELETE `/catalogs/{id}/entries/{entryId}` | procurement.catalog.maintain | If-Match required; DRAFT catalogs only (PRC-CAT-STATE); at most 50,000 entries |
| POST `/catalogs/{id}/vendor-import` | procurement.catalog.maintain | Idempotency-Key required; stages a vendor file (CSV text or JSON rows, up to 50,000) against a DRAFT or PUBLISHED catalog: each row ACCEPTABLE or ERROR with a reason (PRC-CAT-005) |
| GET `/catalog-imports`, `/catalog-imports/{id}`, `/catalog-imports/{id}/rows` | procurement.catalog.maintain or procurement.purchase-order.inquire | Imports (`catalogId`, `status`), one import with row counts, and its rows (`status`, row-number cursor) |
| POST `/catalog-imports/{id}/decisions` | procurement.catalog.maintain | Idempotency-Key and If-Match required; `acceptAll`, `accept` and `reject` by row number while STAGED |
| POST `/catalog-imports/{id}:commit`, `:cancel` | procurement.catalog.maintain | Idempotency-Key and If-Match required; commit adds the ACCEPTED rows to the catalog and writes `catalogimport.committed.v1` |
| GET `/catalog-entries` | procurement.requisition.create or procurement.catalog.maintain | The requisition picker: `legalEntityId` (required), `operatingUnitId`, `q`, `category`, `vendorId`, `date` (default today) |
| POST `/category-vendors`, GET `/category-vendors`, GET `/category-vendors/{id}`, PUT `/category-vendors/{id}` | procurement.category.maintain (reads also procurement.purchase-order.inquire) | Vendor assignments to a procurement category (FR-17.20): status APPROVED, PROVISIONAL or BLOCKED and a validity window; POST needs an Idempotency-Key, PUT needs If-Match (412 PRC-CAT-013) and keeps the stored `validFrom` when left out; one assignment per vendor and category (409 PRC-CAT-011); writes `categoryvendor.assigned.v1` and `categoryvendor.changed.v1` |
| POST `/category-managers`, GET `/category-managers`, DELETE `/category-managers/{id}` | procurement.category.maintain (reads also procurement.purchase-order.inquire) | Workers who manage a category (FR-17.20); POST needs an Idempotency-Key (409 PRC-CAT-012 for the same worker twice); writes `categorymanager.assigned.v1` and `categorymanager.removed.v1` |
| POST `/charge-codes`, GET `/charge-codes`, GET `/charge-codes/{id}`, PUT `/charge-codes/{id}`, POST `/auto-charges`, GET `/auto-charges`, GET `/auto-charges/{id}`, PUT `/auto-charges/{id}` | procurement.policy.maintain (reads also procurement.purchase-order.inquire) | Charge codes (FR-17.9: calculation, default value, debit ITEM/LEDGER/VENDOR and credit LEDGER/VENDOR sides, allocation rule QTY/AMOUNT/LINES) and auto charges by vendor, vendor group or every vendor with a value, minimum order amount, currency and window; POST needs an Idempotency-Key, PUT needs If-Match (412 PRC-CHG-002 and PRC-CHG-003); a duplicate code is 409 PRC-CHG-001; writes `chargecode.created.v1`, `chargecode.changed.v1`, `autocharge.created.v1` and `autocharge.changed.v1`; a calc change on a code that auto charges use is 409 PRC-CHG-006. A PO charge may carry `chargeCodeId` and the PO body `applyAutoCharges`, which adds each code once per PO (`autoChargeCodes`) |
| POST `/signing-limit-requests`, GET `/signing-limit-requests`, GET `/signing-limit-requests/{id}` | procurement.requisition.create to request; reads need procurement.signing-limit.approve or procurement.purchase-order.inquire | Requests for a worker's signing limit (FR-17.17) per document type and currency with a spending and an approval limit; POST needs an Idempotency-Key |
| POST `/signing-limit-requests/{id}:approve`, `:reject` | procurement.signing-limit.approve | Idempotency-Key and If-Match required (412 PRC-SL-005); approving writes or changes the worker's limit and `signinglimit.changed.v1` in one transaction; a decided request is 409 PRC-SL-004; the requester and the worker cannot decide (403 PRC-SL-003) |
| GET `/signing-limits`, GET `/signing-limits/{id}`, GET `/signing-limits/evaluation` | procurement.signing-limit.approve or procurement.purchase-order.inquire | Limits (filter by worker, document type, `expired=true`); the evaluation shows what a limit does to an amount as requester and as approver |
| POST `/signing-limits/{id}:recertify` | procurement.signing-limit.approve | Idempotency-Key and If-Match required; moves `recertifyBy` 12 months past today; the worker cannot recertify their own limit (403 PRC-SL-003); writes `signinglimit.changed.v1` |
| GET `/acknowledgements/awaiting`, GET `/acknowledgements`, GET `/purchase-orders/{id}/acknowledgements` | procurement.purchase-order.inquire | Buyer side (#511): confirmed POs whose current version no vendor has answered; vendor answers (`purchaseOrderId`, `outcome`, `pending=true` for undecided proposals); the answers to one PO |
| POST `/acknowledgements/{id}:approve`, `:decline` | procurement.purchase-order.confirm | Idempotency-Key required; approve writes the proposal as the PO's next version through the edit path (a quantity or price change past the change rules waits for re-approval); decline leaves the order as it is |
| GET `/vendor/purchase-orders`, GET `/vendor/purchase-orders/{id}` | procurement.vendor-portal.read (service-only) | The calling vendor's confirmed POs and its commercial view of one; the vendor comes from the verified `VendorPrincipal`, never from a header, query parameter or body; another vendor's order is 404 |
| POST `/vendor/purchase-orders/{id}/acknowledgements` | procurement.vendor-portal.acknowledge (service-only) | Idempotency-Key required (namespaced by vendor); accept, reject with a reason, or propose per-line `qty`, `unitPrice`, `deliveryDate`; 201 |

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
  Vendor, currency and legal entity never change; the header site and warehouse not
  after the first confirmation. A line has its own `siteId` and `warehouseId`
  (slice 16): omitted, a new line goes where the header does and an existing line
  keeps its destination (and keeps following the header if it did). A line stores a
  destination only when it differs from the header, so a version taken before lines
  had one still compares equal; a move of one line is a new version whose patch names
  `/lines/<i>/warehouseId`, confirmed at once unless a reapproval rule fires, and is
  refused once the line has receipts. A line's requisition and agreement links cannot change and a
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
  up to 20 scoring criteria (unique name, positive weight); holders of procurement.rfq.award score
  each invited vendor 1–10 per criterion while the RFQ is SENT, REPLIES_OPEN or
  CLOSED. A vendor's weighted score is Σ(weight × score) ÷ Σ weight to 4 decimals,
  an unscored criterion counting 0. WEIGHTED_SCORE ranks by it, highest first, an
  unscored vendor after every scored one, then by price and lead time; the other
  rules show it alongside.
- **RFQ types, draft edits and amendments (FR-17.19).** `rfq_type` is a per-legal-entity
  setup (code, name, sealed, amendments allowed, ranking rule, status, default scoring
  criteria) under procurement.policy.maintain, readable with an RFQ duty. An RFQ created
  with `rfqTypeId` (also on `:from-requisitions`) takes the type's ranking rule and criteria
  where the request leaves them out, is sealed when the type is, and copies the amendment
  flag and the sealed requirement, so changing a type never moves an RFQ already out. A DRAFT
  RFQ's deadline, ranking rule, sealed flag and criteria are edited with `PUT /rfqs/{id}`
  (If-Match); a sent RFQ changes only by `POST /rfqs/{id}/amendments`: a summary, optionally
  a later deadline (replies that were late only against the old deadline stop being late; the
  amendment lists the vendors it cleared in `lateClearedVendorIds` and `rfq.amended.v1`) and a
  replacement scoring group (only while nobody is scored and, on an RFQ that is not sealed,
  before any reply is recorded: once the buyer can read prices, re-weighting could steer the
  award; a sealed RFQ before its deadline may still replace it). It numbers the addendum
  RFQ-n-A<k>, owes an acknowledgement from every invited vendor
  (`PUT /rfqs/{id}/amendments/{no}/acknowledgements/{vendorId}`, idempotent) and writes
  `rfq.amended.v1`. A sealed RFQ past its deadline cannot be amended.
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
- **Delivery schedules (FR-17.10).** A PO line may carry 1 to 100 schedule rows (quantity,
  delivery date, the date the vendor confirmed) whose quantities add up to the line quantity
  exactly (PRC-DS-001), and all lines of one PO hold at most 1,000 rows together (PRC-DS-003;
  `po.confirmed.v1` carries every row and the broker takes 512 KiB messages). Rows are
  numbered in delivery date order. The rows are replaced or removed only while the PO is DRAFT
  or REJECTED (PRC-DS-002); a replacement row that repeats an existing one (same date and
  quantity) keeps the vendor's confirmed date. After that quantities change through the PO
  edit (change management). A PO edit that changes the line quantity adds the difference to
  the last row or takes it from the last rows backwards (a row that reaches zero is dropped; a
  reduction larger than the last row continues into the rows before it), and a row whose
  quantity changes loses the vendor's confirmed date. Cancelling the line or the PO removes the
  schedule. `:confirm`
  refuses a schedule that does not add up (PRC-DS-001) and `po.confirmed.v1` carries each
  line's rows, one per ORDERED inventory transaction the wave 16 contract will create. The
  vendor's confirmed date of a row is set on a CONFIRMED PO and writes
  `deliveryschedule.confirmed.v1` once per change; the same date again is answered 200 even
  with an older If-Match. Writes take If-Match on the PO row version and bump it.
- **Service lines (FR-17.23, BR-17.19, AC-17.17).** A PO line becomes a SERVICE line when it
  has a specification (a plain line has none). The specification holds the value limit for
  unplanned services (0 allows none), the expected value (never above the limit), an optional
  contract limit (an agreement line of an EFFECTIVE agreement of the PO's legal entity, vendor
  and currency whose remaining value, committed minus released, or remaining quantity at the
  net unit price, also bounds unplanned services) and an optional outline: levels and service
  items nested up to 5 deep and 200 nodes. An item's amount is quantity × unit price rounded
  half-even to the currency's minor units, a level's amount is the sum of its children (a
  stated `total` that differs is 422 PRC-SRV-004), and the planned total (the top-level sum)
  may not exceed the line amount (PRC-SRV-002, checked again at `:submit` and `:confirm`, and
  on an edit of a confirmed PO, since the line amount can fall in a later edit). The specification is saved or removed only while the PO is
  DRAFT or REJECTED (PRC-SRV-001) and under If-Match on the PO row version, which it bumps;
  after that it changes through change management (not yet versioned, see NOT-IMPLEMENTED).
  A model service specification is copied into the line (`modelId`), so a later change to the
  model does not move the line; the model must be ACTIVE, of the PO's legal entity and priced
  in the PO's currency (PRC-SRV-005). The limit check adds the unplanned amount already
  entered to the entry and refuses when the total is above the value limit or the contract
  line's remaining value: 200 with `allowed` false, `code` PRC-SRV-003 and the PRD message in
  `detail`, so the caller keeps the figures; `remaining` is what may still be entered under both
  limits. A cancelled line or PO takes no entry (PRC-SRV-001). Wave 18 owns the entered amounts
  and the entry sheets: it reads the limits and outline `seq`s of every service line of a PO in
  one call (`GET .../service-lines`, which also tells a plain line from a service line) and
  enforces `remaining` under its own per-PO lock, since this check holds none.
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
- **Purchasing policy (FR-17.13).** One row per legal entity with three flags;
  a legal entity with no row has the defaults (change management on, the other
  two off) at version 0. `changeManagementDefault` is the `changeManagement` of a
  new PO that does not say. `allowInvoiceBeforeReceipt` lowers the PO edit floor
  of BR-17.7 from invoiced to received (never below received, PRC-PO-007).
  `poFourEyes` keeps the user who created (`created_by`), submitted
  (`submitted_by`) or last changed the content of (`changed_by`, an edit or a
  reprice apply) a PO from `:approve` and `:confirm` (403 PRC-POL-002), so the
  editor of a change order cannot approve it either. A caller with no user id is
  refused while the policy is on; an actor not recorded on an older PO blocks no
  one. The routes live under `/purchasing-policies` so `/policies` stays free for
  the FR-17.16 policy framework.
- **Category access (FR-17.20).** A procurement category is the free-text
  `category` of requisition lines, PO lines and catalog entries (trimmed, matched
  exactly). Assignments belong to a legal entity and the rule reads those of the
  document's legal entity: a vendor approved in one entity is not approved in another,
  and a category restricted in one entity can be open in another. A category with at
  least one APPROVED or PROVISIONAL assignment (whatever its window) is restricted; a
  category with none, or with only BLOCKED rows, is open to every vendor. In a
  restricted category a vendor needs an APPROVED assignment whose window holds the day
  (UTC today); PROVISIONAL passes with warning PRC-CAT-010, and expired, not yet valid
  or missing is a violation. A BLOCKED assignment whose window holds the day is a
  violation in any category; it blocks that vendor only. Before or after its window a
  BLOCKED row is not in force and the vendor counts as unassigned (passes in an open
  category, a violation in a restricted one). The policy's `categoryAccessMode`
  (default SOFT, PRD 3.17.10) decides what a violation does: HARD refuses with 422
  PRC-CAT-002 naming the first line and how many more, SOFT returns it as a
  PRC-CAT-002 warning, OFF checks nothing. The rule runs at
  `requisitions/{id}:submit` (each line's preferred vendor against its category; the
  warnings come back on the requisition), at `requisitions:consolidate` (the vendor
  each line is bought from, including a buyer override; warnings come back beside the
  POs), at `POST /purchase-orders` (the PO vendor against every line's category) and at
  `PATCH /purchase-orders/{id}` for the lines the edit adds (the vendor of a PO cannot
  change and an existing line keeps its category); the PO answer carries the warnings.
  A PO line that orders a requisition line takes that line's category (another one is
  refused), a direct line takes the category it is given, and a catalog requisition
  line keeps its entry's category. Under HARD a PO line with neither a product nor a
  category is refused (COM-0001 on `lines[i].category`), so a description-only line
  cannot step around the rule. A line with a product and no category is not checked,
  because a product's procurement category lives in the wave 8 product master. The check reads the assignments in its own transaction before the document
  is written, so a change made in between is not seen; a replayed submit of a
  requisition that is no longer a DRAFT is not checked again.
- **Procurement catalogs (FR-17.18).** A catalog belongs to a legal entity, has a
  validity window and the operating units it is published to (none means every
  unit), and moves DRAFT → PUBLISHED → RETIRED. Entries (vendor, optional product,
  category, unit, price with up to six decimals, currency, lead time, preferred,
  image reference) are added and removed only while DRAFT; a published catalog is
  frozen, so a change is a new catalog. The one exception is the reviewed vendor
  import below: its commit adds entries to a PUBLISHED catalog, because AC-17.13
  has the accepted rows of a published catalog show in the picker; nothing changes
  or removes an entry of a published catalog. A catalog holds up to 50,000 entries
  (the PRD caps a vendor file at 50,000 rows and sets no catalog cap); reads carry
  the count and the first 200 entries and page the rest. The picker returns entries of PUBLISHED
  catalogs valid on the date and published to the unit, newest first. A
  requisition line that names `catalogEntryId` is checked against the same rules
  on the requisition's legal entity, unit and currency (PRC-CAT-004,
  PRC-CAT-006), takes the entry's product, name, category, unit, price and vendor
  where the request left them blank, and keeps the link on the line; a different
  unit or product is refused, and an entry the caller cannot see is a field error
  on the line (COM-0001). Entries are not re-priced after a requisition is saved.
- **Vendor catalog import (FR-17.18, AC-17.13).** A vendor file is staged, not applied.
  The CSV header is matched by name (`Vendor part number`, `Name`, `Category`, `UOM`,
  `Price`, `Currency`, `Lead time`, `Image URL`, `Product id`; name, category, unit,
  price and currency must be present), and every row is validated like a catalog
  entry: a row that fails is ERROR with its reason, as is a vendor part number
  repeated in the file or already held by the vendor in the catalog. The rest are
  ACCEPTABLE. The buyer accepts or rejects rows (an ERROR row cannot be accepted),
  and the commit adds the ACCEPTED rows to the catalog as entries, in one transaction
  with the version bump and the event. Rows left undecided stay out. The one place
  a PUBLISHED catalog takes entries is this reviewed commit, so the new items show
  in the picker for the catalog's units at once; a RETIRED catalog takes none. The
  same file (vendor and rows) is staged once per catalog unless its import was
  cancelled (PRC-CAT-IMPORT-DUPLICATE; PRD 3.17.16 keys the import on the file
  hash), and a part number that another import committed after this one was staged
  stops the commit (PRC-CAT-005) until the buyer rejects that row. The request body
  is capped at 32 MiB (a 50,000-row file is about 8 MB as CSV, 20 MB as JSON rows);
  the commit adds all accepted rows in one statement (50,000 rows stage in about a
  second and commit in under one on a laptop Postgres).
- **Signing limits (FR-17.17, BR-17.15, AC-17.12 limit half).** A signing limit is a
  worker's spending limit and approval limit for one document type (REQUISITION,
  PURCHASE_ORDER, INVOICE, EXPENSE) in one currency of a legal entity. It is written
  only by approving a `signing_limit_request`; approving a second request for the same
  worker, type and currency changes the same limit. It must be recertified within 12
  months (`recertifyBy`; the 12 is fixed until the `signing_limit_recertification_months`
  setting exists). Two rules read it. At requisition submit, a requester whose spending
  limit in the requisition currency is below the total, or whose limit is past
  `recertifyBy` (warning PRC-SL-001), gets one more approval level, `NEXT_APPROVER`,
  appended to the chain. At PO `:approve`, an approver whose approval limit in the PO
  currency is below the PO total (403 PRC-SL-002), or whose limit is past `recertifyBy`
  (403 PRC-SL-001), cannot approve and the next approver must. The approver rule fails
  closed: an approver with no PURCHASE_ORDER limit in the PO currency (none at all, or
  only limits in other currencies, since amounts are not converted until wave 12 rates
  are wired) has an approval limit of zero, as does a caller that is not a named user,
  so only a zero total can be approved without a limit. The total checked is the one
  at the If-Match version, which the store re-checks under the row lock, so an edit or
  reprice after the check is a 412 and a changed PO that needs re-approval is checked
  again at its new total. A requester whose limits are all in other currencies gets the
  extra level; a requester with no REQUISITION limit at all gets none until default
  signing limits by job exist (waves 7 and 42). The requester of a request and the
  worker it is for cannot approve or reject it, and a worker cannot recertify their own
  limit (403 PRC-SL-003). `workerId` is the worker's user id until worker records exist.
- **Idempotency.** Creates and custom methods store the key with a hash of the operation
  and body; a replay returns the original resource, a different body (or endpoint)
  with the same key is `idempotency_key_reuse`.

## Vendor acknowledgement (#511, subtask 1)

A vendor answers the latest version of a CONFIRMED PO once: accepts it, rejects it with a reason, or proposes changes to
lines (quantity, delivery date, unit price). A proposal is stored PENDING; a buyer with `procurement.purchase-order.confirm`
approves it (the PO moves to its next version through the normal edit path, including re-approval) or declines it (the order is
unchanged and the version stays unanswered). One proposal may be open per PO. An answer to a version that is not the latest is
refused with a plain message (PRC-ACK-001). Every answer and decision is a row in `purchase_order_ack` (FORCEd RLS, tenant and
legal entity) and writes `poack.accepted.v1`, `poack.rejected.v1`, `poack.proposed.v1` or `poack.decided.v1`.

A new confirmed version of the order marks every proposal still open on an older version SUPERSEDED (BR-20.9; `poack.decided.v1`
with decision SUPERSEDED) so the vendor can answer the new version. `resultingVersionNo` is set when an approved proposal is
confirmed at once; when the change waits for re-approval it is absent and the order is IN_REVIEW. `GET /purchase-orders/{id}`
carries `pendingAcknowledgement` and `pendingAcknowledgementId` while a proposal waits for the buyer (AC-20.7).

A vendor sees only confirmed content: while the buyer's change to a confirmed order is unconfirmed (IN_REVIEW), the vendor's
read and list show the confirmed version's snapshot, and a cancelled order is gone from the vendor's reads. Vendor responses
carry no legal entity, no buyer users and no decision note; the order detail lists up to 200 answers and sets
`acknowledgementsMore` when there are more.

The vendor routes take the vendor only from `VendorPrincipal` (`API.WithVendorPrincipal`). No credential in the platform carries a
vendor yet, so the default `NoVendorPrincipal` refuses every caller (403 AUTH-0003) until the supplier portal supplies one. Their
duties, `procurement.vendor-portal.read` and `procurement.vendor-portal.acknowledge`, are `service_only`: never assignable to a person.

## Inbound changes from planning and fulfillment (slice 18)

`purchasing consume` (a second process from the same image; Helm `consumer.enabled`) follows two topics with the
platform at-least-once consumer (inbox `consumer_inbox`, retries, one dead-letter topic `passage.procurement.inbound.dlq` for both source topics)
and runs as the service principal `service:purchasing-inbound` with the duty `purchasing.inbound.apply`. It does not
start until that principal is seeded in `platform/go/tenant` and `policies/authz/data.json` (see NOT-IMPLEMENTED.md).

| Event | Effect on the purchase order |
|---|---|
| fulfillment `dropship.closed.v1` (topic `passage.fulfillment.dropship`) | The PO line named by `purchase_order_line_id` (the `id` of a line of `GET /purchase-orders/{id}`) is lowered by what the closing adds: `qty_closed` is the drop-ship's running closed total (a drop-ship can be closed more than once, for example when sales cuts the order back and the buyer later closes the rest), so the line goes down by `qty_closed` less the closings of that drop-ship already recorded. A line left with nothing and no receipts is cancelled, and the PO with it when it was the last open line. The drop-ship id with the total is the idempotency key. |
| fulfillment `dropship.cancelled.v1` (same topic, slice 19) | The same line is lowered by the quantity the vendor will never ship: `qty` (the drop-ship's ordered quantity) less `qty_confirmed` and less what earlier closings of that drop-ship took off. A line left with nothing and no receipts is cancelled, the PO with it when it was the last open line. The drop-ship id is the idempotency key. |
| planning `message.applied.v1` (topic `passage.planning.plan`, planning's plan channel, beside its other plan and message events, which are skipped) | `supply_ref` names the PO (by number) and `item_id` the line. `postpone` and `advance` move the delivery date to `suggested_date` (a postpone must be later, an advance earlier); `decrease` and `increase` take or add `qty` (the message quantity is the delta); `cancel` cancels the line. With several open lines of the item a date move is narrowed to the line whose open quantity equals `qty`; otherwise the message is refused as ambiguous. A `warehouse_id` narrows the lines to that warehouse. The message id is the idempotency key. Any other action, a `supply_ref` that is no PO of the legal entity (a planned order, production or transfer) and a plan with no legal entity record nothing. |

Every change goes through the PO change management path (FR-17.8): the next version, confirmed at once or waiting for
re-approval when a rule fires (a quantity increase over 5 %); totals, percentage charges and tax follow the new
quantity. Only a CONFIRMED order takes a change; one in review or cancelled is recorded as refused. A purchasing rule
that stops a change (the received/invoiced floor PRC-PO-007, a line drawn on an agreement PRC-PO-010, a service
specification above the line, an ambiguous line) never retries and never dead-letters: it is recorded as `REFUSED`
with its code for a buyer to settle: `POST /inbound-changes/{id}:retry` runs the stored request again against the order as
it is now (the same planning and change path, as the caller, who needs `procurement.purchase-order.maintain`; the row becomes APPLIED or
IGNORED and `RETRIED`, or stays REFUSED with the new reason and one more `attempts`), and `:acknowledge` marks it dealt
with by hand (`ACKNOWLEDGED`, order untouched). Either needs the change to be REFUSED and unsettled (409 PRC-INB-008
otherwise), writes `inboundchange.resolved.v1` and takes an `Idempotency-Key`; `GET /inbound-changes?status=REFUSED&resolution=NONE`
is the buyer's queue. A refused change also writes `inboundchange.refused.v1` with `buyer_id`, the user who made the order,
for the notification service (wave 4) to route; purchasing sends no notification itself. The consumer runs with the legal-entity scope of the event, because its
principal carries none.

`purchase_inbound_change` keeps one row per request (`status` APPLIED, REFUSED or IGNORED; the quantity and date before
and after; the PO version it went into and whether it waits for re-approval; who applied the message) and
`inboundchange.recorded.v1` is written with it (a retry rewrites the row in place, so one request keeps one row and one key). Codes recorded on a row besides those of the PO edit: PRC-INB-001
action is not for a PO, -002 no open line (of the item in that warehouse), -003 more than one open line, -004 quantity
(nothing to take off or add, more than the line holds, a line with receipts to zero), -005 date (none, the same, the wrong
direction), -006 the order is not CONFIRMED, -007 no change (the line already had that quantity and date), -008 (the 409 of settling a change that is not an unsettled REFUSED one).

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
| PRC-REQ-012 | warning | Returned in `warnings` on create: the estimate was taken from the last approved order of the product (names the order and its date) |
| PRC-REQ-004 | 403 | Only the requester can submit, reopen or cancel the requisition |
| PRC-REQ-009 | 403 | The requester cannot approve or reject their own requisition |
| PRC-REQ-010 | 403 | The approver already approved an earlier level of this round |
| PRC-REQ-011 | 403 | The CATEGORY_OWNER level needs a manager of one of the requisition's categories |
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
| PRC-REQ-006 | 409 | A requisition line is held by a running RFQ (DRAFT, SENT, REPLIES_OPEN, CLOSED): it cannot be ordered, put on a second RFQ or split |
| PRC-REQ-007 | 422 | A split asks for quantities the line cannot give up: they must leave a quantity above zero on the original line |
| PRC-REQ-008 | 422 | A PO line names a requisition line but orders another quantity or unit than the line's (order it whole in its unit, or split the requisition line first), at create or on a later PO edit; only inbound planning may move it |
| PRC-CONS-001 | 422 | Consolidation window outside 1–60 days |
| PRC-CONS-002 | 422 | A consolidation line has no vendor (needs sourcing); a release run lists such a line as skipped instead |
| PRC-REL-NOT-FOUND | 404 | Release run not found |
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
| PRC-RFQ-008 | 422 | An amendment breaks a rule: the RFQ's type does not allow amendments, no summary, the new deadline is not after now and after the current one, the deadline has passed and no later one is given, a sealed RFQ is past its deadline, or the scoring criteria are replaced after vendors were scored or once an RFQ that is not sealed has replies |
| PRC-RFQ-009 | 422 | A draft edit clears the sealed flag of an RFQ whose type requires sealed bids |
| PRC-RFQ-TYPE-001 | 422 | The RFQ type named on a new RFQ is inactive or belongs to another legal entity |
| PRC-RFQ-TYPE-002 | 409 | The legal entity already has an RFQ type with this code |
| PRC-RFQ-TYPE-003 | 412 | If-Match is not the RFQ type's version |
| PRC-RFQ-TYPE-NOT-FOUND | 404 | No such RFQ type |
| PRC-RFQ-AMENDMENT-NOT-FOUND | 404 | The RFQ has no such amendment, or the vendor owes no acknowledgement of it |
| PRC-IP-001 | 422 | Invoicing plan total ≠ line amount, percents not 100, a milestone rounding to nothing, or a line amount change the plan cannot absorb |
| PRC-IP-002 | 422 | The plan has invoiced rows: it cannot be replaced or removed, nor its line cancelled |
| PRC-IP-003 | 422 | The PO is cancelled or finalized, or the line is cancelled |
| PRC-IP-NOT-FOUND | 404 | The PO line has no invoicing plan (or no such row on a settlement report) |
| PRC-IP-004 | 409 | The plan row is settled under another reference or amount, or the line cannot absorb the settled amount |
| PRC-PO-LINE-NOT-FOUND | 404 | The PO has no line with that number |
| PRC-ACK-001 | 409 | The answer or proposal is for a version that is not the PO's latest (the PRD's SUP-ACK-001, "Only the latest PO version {version} can be acknowledged"; the message names the latest version in plain words) |
| PRC-ACK-002 | 409 | The PO is not CONFIRMED (being changed or closed), or the answer is not a proposal |
| PRC-ACK-003 | 409 | A proposal is already open on the PO |
| PRC-ACK-004 | 409 | The version has already been accepted or rejected |
| PRC-ACK-005 | 409 | The proposal has already been approved or declined |
| PRC-ACK-NOT-FOUND | 404 | No such vendor answer |
| PRC-POL-002 | 403 | Four-eyes policy: a user who created, submitted or last changed the PO (or a caller with no user id) cannot approve or confirm it |
| PRC-POL-003 | 412 | If-Match is not the saved purchasing policy version (0 when none is saved) |
| PRC-CAT-002 | 422 | Category access is HARD and a line's vendor has no valid APPROVED or PROVISIONAL assignment in a restricted category, or a BLOCKED assignment in force (SOFT returns it as a warning) |
| PRC-CAT-010 | warning | The vendor's assignment to the category is PROVISIONAL |
| PRC-CAT-011 | 409 | The vendor already has an assignment for the category (change that one) |
| PRC-CAT-012 | 409 | The worker already manages the category |
| PRC-CAT-013 | 412 | If-Match is not the category vendor assignment's version |
| PRC-CHG-001 | 409 | The legal entity already has a charge code with this code |
| PRC-CHG-002 | 412 | If-Match is not the charge code's version |
| PRC-CHG-003 | 412 | If-Match is not the auto charge's version |
| PRC-CHG-004 | 422 | The charge code is inactive or belongs to another legal entity |
| PRC-CHG-005 | 422 | A PO charge's calculation differs from its charge code's |
| PRC-CHG-006 | 409 | The calculation of a charge code that auto charges use cannot change |
| PRC-DS-001 | 422 | The delivery schedule rows do not add up to the line quantity |
| PRC-DS-002 | 422 | The PO is not DRAFT or REJECTED (replace, delete) or not CONFIRMED (confirmed date), or the line is cancelled |
| PRC-DS-003 | 422 | The PO's delivery schedules would hold more than 1,000 rows together |
| PRC-DS-NOT-FOUND | 404 | The line has no delivery schedule, or the schedule has no such row |
| PRC-SRV-001 | 422 | The PO is not DRAFT or REJECTED (save, remove), or the line or PO is cancelled (save, remove, limit check) |
| PRC-SRV-002 | 422 | The planned outline is worth more than the line, the expected value is above the limit, or the contract agreement line is not of an EFFECTIVE agreement of the PO's legal entity, vendor and currency |
| PRC-SRV-003 | 200 answer | Unplanned services exceed the value limit or the contract line's remaining value (BR-17.19): `allowed` false on the limit check; wave 18 raises it on the entry sheet |
| PRC-SRV-004 | 422 | The items of an outline level do not sum to the total stated for the level |
| PRC-SRV-005 | 422 | The model is inactive, of another legal entity or priced in another currency than the PO |
| PRC-SRV-006 | 409 | The legal entity already has a model service specification with this code |
| PRC-SRV-007 | 412 | If-Match is not the model service specification's version |
| PRC-SRV-NOT-FOUND | 404 | The line has no service specification |
| PRC-SRV-MODEL-NOT-FOUND | 404 | Model service specification not found (or outside the caller's legal entity scope) |
| PRC-SL-001 | 403 / warning | The signing limit is past its recertification date: an approver cannot approve; a requester's document gets one more approval level (warning on submit) |
| PRC-SL-002 | 403 | The amount is above the approver's approval limit, or the approver has no limit in the document currency |
| PRC-SL-003 | 403 | A worker cannot decide a signing limit request they made or that is for them, or recertify their own limit |
| PRC-SL-004 | 409 | The signing limit request was already approved or rejected |
| PRC-SL-005 | 412 | If-Match is not the signing limit's or request's version |
| PRC-CAT-VENDOR-NOT-FOUND | 404 | Category vendor assignment not found (or outside the caller's legal entity scope) |
| PRC-CAT-MANAGER-NOT-FOUND | 404 | Category manager not found (or outside the caller's legal entity scope) |
| PRC-CAT-001 | 422 | A catalog with no entries cannot be published |
| PRC-CAT-004 | 422 | The requisition line's catalog is not published to the legal entity and unit on the date |
| PRC-CAT-006 | 422 | The catalog entry's currency differs from the requisition's |
| PRC-CAT-005 | 422 | A vendor file has no header, lacks a required column or has no rows; a row is ERROR (the reason is on the row) and is accepted; or an accepted row's vendor part number reached the catalog after staging (commit) |
| PRC-CAT-007 | 422 | A catalog would hold more than 50,000 entries |
| PRC-CAT-008 | 422 | A vendor file holds more than 50,000 rows |
| PRC-CAT-009 | 422 | A vendor import is committed with no accepted row |
| PRC-CAT-IMPORT-DUPLICATE | 409 | The same vendor file is already staged or committed for the catalog (cancel that import to stage it again) |
| PRC-CAT-IMPORT-STATE | 409 | The import is not STAGED (decide, commit and cancel need STAGED) |
| PRC-CAT-IMPORT-NOT-FOUND | 404 | Import not found (or outside the caller's legal entity scope) |
| PRC-CAT-IMPORT-ROW-NOT-FOUND | 404 | A decision names a row number the import does not have |
| PRC-CAT-STATE | 409 | The catalog status does not allow the action (entries change only in DRAFT, apart from a vendor import commit into a PUBLISHED catalog; publish needs DRAFT, retire needs PUBLISHED; a RETIRED catalog takes no import) |
| PRC-CAT-NOT-FOUND | 404 | Catalog not found (or outside the caller's legal entity scope) |
| PRC-CAT-ENTRY-NOT-FOUND | 404 | The catalog has no such entry, or a requisition line names an unknown entry |
| PRC-SA-001 | 422 | A firm-zone schedule line is reduced, moved or removed without a reason code |
| PRC-SA-002 | 422 | A schedule line would drop below the quantity already received against it |
| PRC-SA-003 | 422 | A release would lower cumulative released below the previous release without a cumulative reset |
| PRC-SA-NOT-FOUND | 404 | Scheduling agreement not found (or outside the caller's legal entity scope) |
| PRC-SA-REL-NOT-FOUND | 404 | The scheduling agreement has no release with that number |
| PRC-INB-NOT-FOUND | 404 | Inbound change not found (or outside the caller's legal entity scope) |
| idempotency_key_reuse | 422 | Idempotency key reused with a different body or on another endpoint |

## Dependencies

Allowed set only: platform kernel, chi (through the kernel), pgx/v5,
shopspring/decimal, and `pgregory.net/rapid` for property tests (already in the
platform go.mod). The inbound consumer (slice 18) adds the platform `eventing` package and its Kafka client (franz-go),
which arrive through the platform module.

## Web module

`apps/web/src/modules/purchasing` gives the procurement workspace: requisition,
RFQ, purchase order, purchase agreement and trade agreement lists with create forms
and detail pages, the consolidation board, the price lookup with explain, sealed-bid
and scoring screens on the RFQ, the invoicing plan page per PO line, the price
change pages (one PO, and the run over many), and the scheduling agreement
monitor, create form, detail page (schedule editor, release run, releases) and
release documentation, the purchasing policy list and editor, the procurement catalog list, create form and detail page, and the catalog picker on the requisition form. See its README.
