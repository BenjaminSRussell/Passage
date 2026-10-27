# transport (wave 34, PSG-34)

Transportation management and freight: rate cards, rate shopping, freight bills,
carrier invoice reconciliation, carrier tendering, shipment tracking and landed-cost
voyages. Replaces D365 TMS and SAP TM rating, tendering, tracking, freight
reconciliation and landed cost. Slice 1 is the rules core plus the rating and
reconciliation API; slice 2 adds the tender cascade, tracking ingest and timelines,
and voyages with landed-cost allocation and posting; slice 3 adds rule-based load
building, freight bill approval and dispute, the transport dangerous-goods check with
DG shipping-paper lines and a tender gate, the denied-party screening hook, and the pure
Incoterm transfer, gate and detention rules; slice 4 adds driver check-in and check-out
with seal verification, detention and an append-only gate log, and goods-in-transit
orders that move voyage receipt lines into transit at the Incoterm transfer event; slice 5
adds screening holds with their trade-compliance release and the tender gate, the
rate-shop cache, route optimization through the routing-py VRP with stored plans keyed
by request hash, and layer-based load plans. What is not built yet is listed
in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

The solvers (OR-Tools VRP and the load-plan stacker) live in the sibling Python service
`services/routing-py`; `internal/optimizer` calls it over HTTP at `TMS_OPTIMIZER_URL`.

## Layout

```
cmd/transport/          entrypoint: dbx.Connect -> (MIGRATE_ON_START) dbx.Migrate -> httpx.NewRouter -> api.Register -> httpx.Serve
internal/domain/        pure rules, no I/O: rating, freight-bill matching, ISO 6346 container check digit,
                        landed-cost allocation, tender cascade state machine, tracking stop
                        projection (BR-34.9), voyage validation and allocation, load building
                        (BR-34.8), freight bill decisions (BR-34.4), DG check (BR-34.15),
                        screening hook (BR-34.17), Incoterm transfer (BR-34.11), gate
                        arrival, seal and detention (BR-34.13, BR-34.14), gate visits,
                        goods-in-transit orders (BR-34.11), screening holds and their
                        release (BR-34.17), route requests and solution checks (BR-34.1),
                        load plan requests and plan checks (FR-34.18)
internal/store/         pgx repositories, every query inside dbx.WithTenantTx (RLS applies)
internal/api/           HTTP handlers, request validation, duty checks
internal/events/        event type constants and payload structs
internal/optimizer/     HTTP client for routing-py: POST /v1/routes:solve, POST /v1/load-plans:stack
migrations/             0001_init.sql (rate_card, freight_bill, carrier_invoice, outbox),
                        0002_tender_tracking_voyage.sql (tender, tracking_event, voyage),
                        0003_load_dg_decision.sql (load_build, load, load_shipment, dg_check,
                        freight_bill_decision), 0004_gate_goods_in_transit.sql (gate_visit,
                        gate_event, goods_in_transit_order, goods_in_transit_line),
                        0005_screening_hold_route_plan.sql (screening_hold, route_plan), applied in order
api/openapi.yaml        OpenAPI 3.1 for every endpoint in this slice
events/asyncapi.yaml    AsyncAPI 3 for every event the service publishes
charts/transport/       Helm chart (image, DATABASE_URL from a secret, probes, ServiceMonitor toggle)
```

## Endpoints (slice 1)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | /v1/transport/rate-cards | TMS_RATE_APPROVE | Effective-dated card; overlap per carrier, service and basis is TMS-0102; `Idempotency-Key` optional |
| GET | /v1/transport/rate-cards | TMS_RATE | Filter by `carrierId`, `serviceId` |
| POST | /v1/transport/shipments/{id}:rate | TMS_RATE | One quote per carrier service with the card valid on the ship date, ranked, with explain trace; not cached yet |
| POST | /v1/transport/freight-bills | TMS_RECONCILE | Expected charges for a load; `Idempotency-Key` optional |
| GET | /v1/transport/freight-bills | TMS_VIEW | Newest first, at most 500; filter by `matchStatus`, `carrierId` |
| GET | /v1/transport/freight-bills/{id} | TMS_VIEW | The bill, its carrier invoice and the per-charge comparison once matched |
| POST | /v1/transport/carrier-invoices | TMS_RECONCILE | Matched to the named bill in the same transaction; no bill makes it UNEXPECTED_INVOICE |
| GET | /v1/transport/carrier-invoices | TMS_RECONCILE | Newest first, at most 500; `matchStatus=UNEXPECTED_INVOICE` is the exception queue |
| GET | /v1/transport/carrier-invoices/{id} | TMS_RECONCILE | |

## Endpoints (slice 2)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | /v1/transport/loads/{id}:tender | TMS_TENDER | Carriers in rank order with optional quoted rates; `timeoutMin` 5 to 1440 (default 120); one open tender per load (TMS-0114); `Idempotency-Key` optional |
| GET | /v1/transport/tenders | TMS_TENDER | Tender board, newest first, at most 500; filter by `status`, `loadId`; `overdue` flags a pending attempt past its deadline |
| POST | /v1/transport/tenders:expire | TMS_TENDER | Expiry sweep for the tenant: cascades every overdue tender (at most 500 per call) |
| GET | /v1/transport/tenders/{id} | TMS_TENDER | One tender with its attempts |
| POST | /v1/transport/tenders/{id}:respond | TMS_CARRIER_RESPOND | `{carrierId, accept}`; late is TMS-0105, wrong carrier TMS-0113; the same response again returns the tender unchanged |
| POST | /v1/transport/tracking-events | TMS_TRACK_INGEST | EDI 214 codes X3, AF, X1, D1; source + ref is the idempotency key (201 stored, 200 replay with `Idempotent-Replayed: true`) |
| GET | /v1/transport/tracking-events | TMS_VIEW | With `loadId`: that load's events by occurred_at; without: the newest 500 received |
| GET | /v1/transport/loads/{id}/tracking | TMS_VIEW | Timeline plus the projected state of every reported stop |
| POST | /v1/transport/voyages | TMS_LANDED_COST | Legs, containers (ISO 6346, TMS-0124), folios, cost lines, receipt lines; starts ESTIMATED; `Idempotency-Key` optional |
| GET | /v1/transport/voyages | TMS_LANDED_COST | Newest first, at most 500, without documents; filter by `status` |
| GET | /v1/transport/voyages/{id} | TMS_LANDED_COST | |
| POST | /v1/transport/landed-cost/voyages/{id}:allocate | TMS_LANDED_COST | Allocates every cost line, posts the voyage and publishes `landedcost.posted`; a POSTED voyage is returned unchanged |

## Endpoints (slice 3)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | /v1/transport/loads:build | TMS_PLAN | Shipments grouped by ship-from site, ship-to region, mode and pickup date, packed first-fit decreasing by weight; never over capacity (BR-34.8); too large TMS-0115, already planned TMS-0116; `Idempotency-Key` optional |
| GET | /v1/transport/loads | TMS_VIEW | Newest first, at most 500; filter by `status` |
| GET | /v1/transport/loads/{id} | TMS_VIEW | The load with its DG checks, newest first (at most 20) |
| POST | /v1/transport/loads/{id}:dg-check | TMS_PLAN | ADR or 49CFR check of the request's DG lines; stored with findings and shipping-paper lines; a FAIL blocks tendering |
| POST | /v1/transport/loads/{id}:screen | TMS_PLAN | Screens the parties through the `ScreeningProvider` hook; HOLD on a hit or timeout (TMS-0133), WARN when cross-border and no provider (TMS-0134); not stored |
| GET | /v1/transport/transfer-rules/{incoterm} | TMS_VIEW | Default goods-in-transit transfer event for an Incoterms 2020 rule (BR-34.11) |
| POST | /v1/transport/freight-bills/{id}:approve | TMS_RECONCILE_APPROVE | MATCHED, VARIANCE or DISPUTED to APPROVED; a reason is required except for MATCHED; `If-Match` required; publishes `freightbill.approved` |
| POST | /v1/transport/freight-bills/{id}:dispute | TMS_RECONCILE | VARIANCE to DISPUTED with a reason; `If-Match` required |

## Endpoints (slice 4)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | /v1/transport/gate/check-in | TMS_GATE | Direction, carrier SCAC, trailer, optional seal and load, driver name and license, appointment id with its time (or a walk-in); ON_TIME within `toleranceMin` (default 60), else EARLY or LATE with TMS-0127; a trailer already in the site's yard is TMS-0118; publishes `gate.checkedin`; `Idempotency-Key` optional |
| POST | /v1/transport/gate/check-out | TMS_GATE | `{visitId, sealNo, checkOutAt?, hourlyRate?}`; seal mismatch or an outbound visit without a seal is 422 TMS-0129 and logged as CHECK_OUT_BLOCKED; detention per BR-34.13 (TMS-0126); `Idempotency-Key` optional |
| GET | /v1/transport/gate/visits | TMS_GATE | Gate log, newest check-in first, at most 500; filter by `siteId`, `status`, `carrierScac`, `date` (UTC day) |
| GET | /v1/transport/gate/visits/{id} | TMS_GATE | The visit with its gate log rows in the order written |
| POST | /v1/transport/gate/visits/{id}:seal | TMS_GATE | Records the seal applied at load confirmation while IN_YARD; logged as SEALED |
| POST | /v1/transport/goods-in-transit | TMS_LANDED_COST | `{voyageId, incoterm, siteId, receiptLines?}` on an ESTIMATED voyage; origin Incoterms only (TMS-0120 otherwise); each line's estimate per cost type is its BR-34.6 share; a line is on one order (TMS-0119); `Idempotency-Key` optional |
| GET | /v1/transport/goods-in-transit | TMS_LANDED_COST | Newest first, at most 500; filter by `voyageId`, `status` |
| GET | /v1/transport/goods-in-transit/{id} | TMS_LANDED_COST | |
| POST | /v1/transport/goods-in-transit/{id}:ship | TMS_LANDED_COST | `{shippedAt?}`; PENDING to IN_TRANSIT (TMS-0121) and publishes `goodsintransit.shipped`; an IN_TRANSIT order is returned unchanged |

## Endpoints (slice 5)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | /v1/transport/loads/{id}:screen | TMS_PLAN | Now stores a HOLD as a screening hold (one OPEN per load; screening again refreshes its party results) and returns `holdId`; CLEAR and WARN never clear it |
| GET | /v1/transport/screening-holds | TMS_VIEW | Screening holds inquiry, newest first, at most 500; filter by `status` (OPEN, RELEASED) and `loadId` |
| GET | /v1/transport/screening-holds/{id} | TMS_VIEW | The hold with its party results and release |
| POST | /v1/transport/screening-holds/{id}:release | TRADE_COMPLIANCE_RELEASE | `{reason}` (1 to 500); user and reason stay on the hold; a BLOCKED party is 422 TMS-0133; released by someone else or for another reason 409 TMS-0122; the same release again returns the hold |
| POST | /v1/transport/routes:optimize | TMS_PLAN | Depot, stops (window in minutes, service time, weight, volume, pallets), vehicles; `timeLimitS` 5 to 25 (default 20), `latenessPerMin` default 50; 201 with the stored plan, 200 `replayed` for the same request; dropped stops warn TMS-0104; optimizer missing or failing 503 TMS-0123 |
| GET | /v1/transport/routes | TMS_PLAN | Plan summaries, newest first, at most 500 |
| GET | /v1/transport/routes/{id} | TMS_PLAN | The plan with its request, routes and dropped stops |
| POST | /v1/transport/load-plans:stack | TMS_PLAN | Trailer and up to 200 pallets; placements bottom to top, unplaced pallets, floor positions, layers, load sequence and kingpin / rear-axle estimate; not stored; 503 TMS-0123 without an optimizer |

`POST /loads/{id}:tender` refuses a load with an OPEN screening hold with 422 TMS-0133
and stores no tender (BR-34.17, AC-34.16). `POST /shipments/{id}:rate` answers from a
cache per tenant and rating input for `TMS_RATING_CACHE_MIN` minutes (default 10, 0
off) and says `X-Passage-Cache: HIT` or `MISS`; saving a rate card drops that tenant's
cache on the replica.

Configuration: `TMS_OPTIMIZER_URL` (chart `optimizer.url`) is the routing-py base URL;
without it optimize and load-plan calls are 503 TMS-0123. `TMS_RATING_CACHE_MIN` sets
the rate-shop cache window (values outside 0 to 60 are clamped; a non-number stops the
service at start).

TMS_GATE also guards the gate log reads: the visits carry driver names and license ids
(personal data, 90-day retention), so TMS_VIEW does not read them. The gate log rows and
the `gate.checkedin` event carry no driver PII; the application role may only insert and
read `gate_event`, and a trigger refuses updates and deletes.

Browser paths are `/api/v1/transport/...`, rewritten by the web BFF to `/v1/transport/...`
like every other service. The PRD writes the paths as `/tms/v1/...`; the route names after
the prefix follow the PRD.

Idempotency: `Idempotency-Key` (1 to 200 characters) is stored on the rate card, freight
bill, tender, voyage, load build, freight bill decision, gate visit or goods-in-transit order it creates with a SHA-256 fingerprint of method, path and body. A repeat with the same
body returns that record; a different body is 422 `idempotency_key_reuse`; a concurrent
repeat waits for the first and returns its record. A carrier invoice's natural key is the
carrier and invoice number (TMS-0108); a tracking event's is source and ref (a different
report under the same pair is 422 `idempotency_key_reuse`). Tender responses and voyage
allocation are naturally idempotent: repeating them returns the stored result and writes
no event; so is shipping a goods-in-transit order. A gate check-out stores its own key on
the visit, so a retried check-out returns the checked-out visit. Freight bill decisions also need `If-Match` with the bill version (the ETag of
`GET /freight-bills/{id}`): without it 428 `COM-0428`, stale 412 `conflict_version`. A
replayed decision key returns the bill as it is now.

The web module `apps/web/src/modules/transport` gives the screens: workspace, rate shop
with explain trace, rate card list and form, freight bill list, detail (reconciliation
grid) and form, carrier invoice list and registration form, tender board, start-tender
form and tender detail with the carrier response, tracking list, load timeline and
status form, voyage list, form and detail with allocation, load list, build-loads form
and load detail with the DG check (findings and shipping papers) and party screening,
approve and dispute on the freight bill detail with its decision history, the gate log
with check-in, seal and check-out (blocked attempts shown in the visit's log), and
goods-in-transit orders (list, form prefilled from a voyage, detail with ship),
screening holds (list, detail with the release form; the load detail links a new hold),
route plans (list, optimize form, detail with each vehicle's route and dropped stops) and
the load plan (form with the top view, floor positions, load sequence and axle estimate).

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_transport
export MIGRATE_ON_START=true     # dev only; stage/prod run migrations as an Argo PreSync Job
go run ./cmd/transport
```

Configuration: `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves
`/metrics`), `DATABASE_URL`, `MIGRATE_ON_START`, `PASSAGE_ENV`, `SERVICE_NAME`. The tenant
and duties come from the gateway headers that `platform/go/tenant` reads; OPA is not wired yet.

## Test

```sh
go vet ./... && go test ./...
```

Domain tests are pure table tests with `pgregory.net/rapid` property checks. API tests use
a fake `store.Store`. Store tests use `testkit.DBWithApp` and run as the application role,
so row-level security is exercised; they are skipped without `PASSAGE_TEST_DATABASE_URL`
and need `PASSAGE_TEST_APP_DATABASE_URL` (a non-owner role without BYPASSRLS):

```sh
PASSAGE_TEST_DATABASE_URL=postgres://postgres@localhost:5432/passage_test \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:app@localhost:5432/passage_test \
go test ./internal/store/
```

## Business rules implemented

- BR-34.2 rating: per carrier service, the card valid on the ship date (one per basis);
  lane and zone lookup with `*` wildcards and the most specific row winning; break bands in
  kg, pallets, containers, per shipment, km or miles (DISTANCE basis); the next-break
  deficit rule on WEIGHT_BREAK cards; dimensional weight (divisor per card, default 5000
  cm³/kg); minimum charge; flat or percent accessorials; fuel surcharge percent of base;
  each charge rounded to cents half-up; total = Σ charges; explain trace with the
  chargeable weight, base, weight-break, minimum, accessorial and fuel steps.
- BR-34.10: a service with no card valid on the ship date yields one `NO_VALID_RATE_CARD`
  option whose trace says so; overlap on save is TMS-0102, serialised per carrier service
  with a transaction advisory lock so concurrent saves cannot both pass.
- BR-34.4 match states: MATCHED when every charge is within tolerance, otherwise VARIANCE;
  an invoice without a bill is UNEXPECTED_INVOICE and is never auto-approved; duplicate
  invoice numbers per carrier are TMS-0108; a bill takes one invoice (TMS-0111) from its
  own carrier in its own currency (TMS-0112). Tolerance per charge: the smaller of $5.00
  and 2 % of the expected amount, so a charge must pass both limits (this is what makes
  AC-34.4's $5.50 on $412.50 a variance).
- BR-34.6 landed-cost allocation by weight, volume, value, quantity or equal split per
  voyage cost line, shares rounded to cents with the residual on the highest-value line
  (ties: lowest line number); the receipt-line totals equal the voyage cost total to the
  cent, which the store checks again before posting.
- BR-34.12 ISO 6346 container check digit on every voyage container (TMS-0124).
- BR-34.3 tender cascade: rank 1 first; DECLINED or EXPIRED moves to the next carrier;
  after the last, TENDER_FAILED with a planner task (TMS-0106 on the tender); a late
  acceptance is TMS-0105. Deadlines are applied in order however late the sweep or the
  response arrives, and the expiry is saved even when the late response is rejected.
  Invariant: a load has at most one ACCEPTED and at most one PENDING tender (partial
  unique index), so a load is never in two cascades.
- BR-34.9 tracking: events apply by occurred_at (ties by source and ref), so ingest order
  does not matter; a D1 with no earlier AF sets the stop DELIVERED and backfills the
  pickup as inferred; a stop's status never moves back.
- FR-34.1 / BR-34.8 load building by rule: one load per ship-from site, ship-to region,
  mode and pickup date until it is full in weight, volume or pallets; a shipment that does
  not fit opens another load, so AC-34.8's 30 × 1,600 kg on 20,000 kg gives 3 loads. A
  shipment is on at most one load (primary key on `load_shipment`), also under concurrent
  builds; the database also checks that no load is over its capacity.
- BR-34.4 decisions: MATCHED approves as is; VARIANCE is disputed or approved with a
  reason; DISPUTED is closed by approving with a reason (the "APPROVED with adjustment"
  path, at the invoiced amount); anything else is TMS-0117. Every decision is kept with
  user, time, reason and the state change.
- BR-34.15 DG check: FAIL for divisions 1.1 or 1.2 with any of classes 2 to 8 (except 6.2) on one load
  (49 CFR 177.848 or ADR 7.5.2, TMS-0130) and for regulated goods without a 24-hour
  emergency phone; ADR 1.1.3.6 points = Σ quantity × 50/3/1/0 by transport category, above
  1,000 is WARN TMS-0131 (AC-34.14); category 0 and limited quantities WARN. The latest
  FAIL blocks a tender of the load with TMS-0130 (AC-34.13, `dg_check_mode` BLOCK).
- BR-34.17 screening: every party through the provider with a 5 s timeout; POTENTIAL_MATCH,
  BLOCKED, an error, a timeout or an unknown status hold the load; the hook never clears.
  A HOLD is stored; the load cannot be tendered while it is OPEN (TMS-0133, AC-34.16). A
  TRADE_COMPLIANCE_RELEASE user releases POTENTIAL_MATCH and SCREENING_PENDING holds
  with a reason, kept with the user and time; a BLOCKED party stays held until the list
  changes. One OPEN hold per load (partial unique index).
- FR-34.2 / BR-34.1 route optimization: the request is normalized (defaults: speed 60
  km/h, lateness 50 per minute, 660 min per route, solver 20 s) and hashed; the hash is
  the idempotency key, so the same request returns the stored plan without solving. A
  reply that loses a stop, names an unknown stop or vehicle, or serves a stop twice is
  refused (503 TMS-0123), so a stop is never dropped silently; dropped stops warn TMS-0104.
- FR-34.18 load plans: a reply that loses a pallet or stacks on a non-stackable one is
  refused (AC-34.18 is checked on the Go side too).
- FR-34.4 rate-shop cache: per tenant and input hash for `rating_cache_min`; a new rate
  card is used at once on the replica that saved it.
- BR-34.11, BR-34.13, BR-34.14: Incoterm transfer event, arrival ON_TIME / EARLY / LATE
  within `gate_tolerance_min` (TMS-0127), seal match (TMS-0129), walk-in check (TMS-0128),
  detention = ceil((dwell − free time) ÷ 60) × hourly rate (AC-34.11), raised at check-in
  and check-out. The declared seal must equal the recorded one: the seal of an inbound
  sealed trailer from check-in, or the seal recorded at load confirmation; an outbound
  visit without a seal has no confirmed load and cannot leave (AC-34.12). A trailer is in
  a site's yard once (partial unique index plus a per-trailer advisory lock).
- FR-34.12 goods in transit: an origin Incoterm (EXW, FCA, FAS, FOB, CFR, CIF, CPT, CIP)
  opens a PENDING order for receipt lines of an ESTIMATED voyage into `TRANSIT-{site}`;
  each line carries its BR-34.6 share of every voyage cost line, so orders that cover the
  voyage carry its cost total to the cent. A receipt line is on one order
  (`goods_in_transit_line` key). Shipping moves ownership (TMS-0121).

## Error codes

Envelope: RFC 9457 `application/problem+json`, `type` = `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Missing duty |
| COM-0001 | 400 | Request failed validation; field errors in `errors` |
| COM-0500 | 500 | Internal error (detail hidden) |
| idempotency_key_reuse | 422 | Idempotency-Key replayed with a different body |
| TMS-0100 | 404 | Freight bill or carrier invoice not found (slice-local; the catalog has no not-found code) |
| TMS-0101 | 200 | No valid rate card; the option carries status `NO_VALID_RATE_CARD` (not an HTTP error) |
| TMS-0102 | 422 | Rate card overlaps an existing card for the same carrier, service and basis |
| TMS-0105 | 409 | Tender expired for this carrier; the response is rejected (the cascade it uncovered is saved) |
| TMS-0106 | state | All carriers declined or timed out: the tender is TENDER_FAILED with `plannerTask: true` (not an HTTP error) |
| TMS-0108 | 409 | Carrier invoice number already registered for this carrier |
| TMS-0109 | 201 | Invoice has no freight bill; stored as UNEXPECTED_INVOICE |
| TMS-0110 | info | Allocation residual assigned to the highest-value line; shown as `residual` / `residualLine` per cost allocation |
| TMS-0111 | 409 | The freight bill already has a carrier invoice (slice-local; catalog gap 0111 to 0120) |
| TMS-0112 | 422 | The invoice's carrier or currency differs from the freight bill (slice-local) |
| TMS-0113 | 409 | The tender is not waiting on this carrier (slice-local) |
| TMS-0114 | 409 | The load already has a pending or accepted tender (slice-local) |
| TMS-0115 | 422 | A shipment exceeds the capacity of an empty load on its own (slice-local) |
| TMS-0116 | 409 | A shipment is already on a load (slice-local) |
| TMS-0117 | 422 | The decision does not fit the freight bill's state, or approving a variance or dispute without a reason (slice-local) |
| TMS-0118 | 409 | The gate visit is already checked out, a seal is recorded after check-out, or the trailer is already in the site's yard (slice-local) |
| TMS-0119 | 409 / 422 | The voyage is not ESTIMATED, a receipt line is already on another goods-in-transit order (409), or a line is not on the voyage (422 with `errors.receiptLines`) (slice-local) |
| TMS-0120 | 422 | The Incoterm transfers ownership at destination receipt; no goods-in-transit order is opened (slice-local) |
| TMS-0121 | info | An origin Incoterm opens a goods-in-transit order; on the transfer rule, and on a shipped order |
| TMS-0126 | info | Detention accrued; on the visit's `detention.code` at check-out |
| TMS-0127 | warning | Arrival EARLY or LATE; on the visit's `arrival.code` |
| TMS-0128 | 422 | No appointment and the site refuses walk-ins (not reachable until sites have the setting) |
| TMS-0129 | 422 | Declared seal does not match, or an outbound visit has no seal; the attempt is in the gate log |
| TMS-0124 | 422 | A voyage container number fails the ISO 6346 check digit; the container is named in `errors` |
| TMS-0130 | 422 / finding | DG segregation FAIL finding; a tender of a load whose latest DG check FAILed is refused with it |
| TMS-0131 | finding | ADR 1.1.3.6 points above 1,000 |
| TMS-0133 | screening / 422 | A party is POTENTIAL_MATCH, BLOCKED or SCREENING_PENDING; the load is held. 422 on a tender of a held load and on releasing a hold with a BLOCKED party |
| TMS-0104 | warning | Stops dropped as infeasible by the route optimizer; in the plan's `warnings` |
| TMS-0122 | 409 | The screening hold is already released by another user or for another reason (slice-local; the catalog leaves 0122 unassigned) |
| TMS-0123 | 503 | The optimizer is not configured, unreachable, failing, or answered with something that is not a plan (slice-local) |
| TMS-0134 | screening | No provider and the load is cross-border: NOT_SCREENED |
| COM-0428 | 428 | `If-Match` missing on approve or dispute |
| conflict_version | 412 | `If-Match` is not the bill's current version |

TMS-0100 is also the not-found code for tenders, voyages, loads, gate visits,
goods-in-transit orders, screening holds and route plans. Codes not yet raised (TMS-0103,
0107, 0125, 0132, 0135) are listed in NOT-IMPLEMENTED.md.

## Events

| Type | When | Subject |
|---|---|---|
| dev.passage.tms.freightbill.matched.v1 | A carrier invoice is matched to its bill | freight bill id |
| dev.passage.tms.tender.accepted.v1 / declined.v1 / expired.v1 | Each attempt outcome; payload has rank and the quoted rate | tender id |
| dev.passage.tms.tracking.event.v1 | A status report is stored (not on replay) | load id |
| dev.passage.tms.landedcost.posted.v1 | A voyage is allocated and posted; Σ receiptLines = total | voyage id |
| dev.passage.tms.freightbill.approved.v1 | A freight bill is approved; expected (accrued), invoiced and variance amounts for AP and the ledger | freight bill id |
| dev.passage.tms.gate.checkedin.v1 | A driver and trailer check in (not on replay); no driver PII | gate visit id |
| dev.passage.tms.goodsintransit.shipped.v1 | A goods-in-transit order ships; goods value and estimated cost per line and cost type, Σ lines = estimatedTotal | order id |

Each is written to the outbox in the transaction that makes the change.

## Dependencies

No new third-party dependencies beyond the platform module and what it pulls in
(chi, pgx/v5, shopspring/decimal, pgregory.net/rapid for tests).
