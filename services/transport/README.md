# transport (wave 34, PSG-34)

Transportation management and freight: rate cards, rate shopping, freight bills,
carrier invoice reconciliation, carrier tendering, shipment tracking and landed-cost
voyages. Replaces D365 TMS and SAP TM rating, tendering, tracking, freight
reconciliation and landed cost. Slice 1 is the rules core plus the rating and
reconciliation API; slice 2 adds the tender cascade, tracking ingest and timelines,
and voyages with landed-cost allocation and posting. What is not built yet is listed
in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

Route optimization (VRP) lives in the sibling Python service `services/routing-py`.

## Layout

```
cmd/transport/          entrypoint: dbx.Connect -> (MIGRATE_ON_START) dbx.Migrate -> httpx.NewRouter -> api.Register -> httpx.Serve
internal/domain/        pure rules, no I/O: rating, freight-bill matching, ISO 6346 container check digit,
                        landed-cost allocation, tender cascade state machine, tracking stop
                        projection (BR-34.9), voyage validation and allocation
internal/store/         pgx repositories, every query inside dbx.WithTenantTx (RLS applies)
internal/api/           HTTP handlers, request validation, duty checks
internal/events/        event type constants and payload structs
migrations/             0001_init.sql (rate_card, freight_bill, carrier_invoice, outbox),
                        0002_tender_tracking_voyage.sql (tender, tracking_event, voyage), applied in order
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

Browser paths are `/api/v1/transport/...`, rewritten by the web BFF to `/v1/transport/...`
like every other service. The PRD writes the paths as `/tms/v1/...`; the route names after
the prefix follow the PRD.

Idempotency: `Idempotency-Key` (1 to 200 characters) is stored on the rate card, freight
bill, tender or voyage it creates with a SHA-256 fingerprint of method, path and body. A repeat with the same
body returns that record; a different body is 422 `idempotency_key_reuse`; a concurrent
repeat waits for the first and returns its record. A carrier invoice's natural key is the
carrier and invoice number (TMS-0108); a tracking event's is source and ref (a different
report under the same pair is 422 `idempotency_key_reuse`). Tender responses and voyage
allocation are naturally idempotent: repeating them returns the stored result and writes
no event.

The web module `apps/web/src/modules/transport` gives the screens: workspace, rate shop
with explain trace, rate card list and form, freight bill list, detail (reconciliation
grid) and form, carrier invoice list and registration form, tender board, start-tender
form and tender detail with the carrier response, tracking list, load timeline and
status form, voyage list, form and detail with allocation.

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
| TMS-0124 | 422 | A voyage container number fails the ISO 6346 check digit; the container is named in `errors` |

TMS-0100 is also the not-found code for tenders and voyages. Codes not yet raised (TMS-0103,
0104, 0107, 0121, 0125-0135) are listed in NOT-IMPLEMENTED.md.

## Events

| Type | When | Subject |
|---|---|---|
| dev.passage.tms.freightbill.matched.v1 | A carrier invoice is matched to its bill | freight bill id |
| dev.passage.tms.tender.accepted.v1 / declined.v1 / expired.v1 | Each attempt outcome; payload has rank and the quoted rate | tender id |
| dev.passage.tms.tracking.event.v1 | A status report is stored (not on replay) | load id |
| dev.passage.tms.landedcost.posted.v1 | A voyage is allocated and posted; Σ receiptLines = total | voyage id |

Each is written to the outbox in the transaction that makes the change.

## Dependencies

No new third-party dependencies beyond the platform module and what it pulls in
(chi, pgx/v5, shopspring/decimal, pgregory.net/rapid for tests).
