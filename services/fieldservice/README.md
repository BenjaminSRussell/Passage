# fieldservice (wave 45, slices 1 and 2)

Service management core of Passage: service agreements with SLA profiles, the
entitlement resolver, SLA clocks on service cases with the case status machine,
pause and resume, the SLA monitor (warning and breach events), service work orders
with coverage-aware billing, the work-order status machine with follow-up orders and
first-time fix, and service KPIs. It replaces AX service management (agreements,
service orders) and SAP CS (service orders, warranty check) for these slices.

Wave reference: PSG-45 / `docs/prd/phase-8-projects-people-service.md` section 8.45.
Dependencies on earlier waves (locations, pricing, serial shipments, invoicing,
workers) are reached through events or handoffs; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/fieldservice        entrypoint: dbx.Connect -> dbx.Migrate (optional) -> httpx router -> serve
internal/domain         pure rules: SLA business-minute calendar, SLA clocks with pauses and the
                        monitor's warning/breach verdicts, case status machine, entitlement
                        resolver (BR-45.1), work-order status machine and coverage posting
                        (BR-45.4/5), KPI rates
internal/store          Postgres repositories under RLS (dbx.WithTenantTx), idempotency, outbox
internal/api            HTTP handlers under /v1/service, validation, problem details
internal/events         event types and payloads (see events/asyncapi.yaml)
migrations/             0001_init.sql (RLS, tenant_id first, money numeric(20,6) + currency),
                        0002/0003 passage_app grants, 0004_case_lifecycle.sql (case version, pause,
                        warning instants, emitted alerts; work-order completion, follow-up, first-time fix)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every event published
charts/fieldservice     Helm chart (deployment, service, optional ServiceMonitor)
```

## Endpoints

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/service/sla-profiles` | SvcAgreementMaintain |
| GET | `/v1/service/sla-profiles`, `/v1/service/sla-profiles/{id}` | SvcInquire |
| POST | `/v1/service/agreements` | SvcAgreementMaintain |
| GET | `/v1/service/agreements` (`status`, `customerPartyId`), `/v1/service/agreements/{id}` | SvcInquire |
| POST | `/v1/service/cases` | SvcCaseCreate |
| GET | `/v1/service/cases` (`status`, `slaState`, `customerPartyId`), `/v1/service/cases/{id}` | SvcInquire |
| POST | `/v1/service/work-orders` | SvcWoMaintain |
| GET | `/v1/service/work-orders` (`status`, `territoryId`, `caseId`, `customerPartyId`), `/v1/service/work-orders/{id}` | SvcInquire |
| POST | `/v1/service/work-orders/{id}:transition` | SvcWoMaintain (If-Match) |
| POST | `/v1/service/work-orders/{id}:post` | SvcBillingPost (If-Match, Idempotency-Key) |
| POST | `/v1/service/cases/{id}:transition` | SvcCaseMaintain (If-Match; Idempotency-Key optional) |
| POST | `/v1/service/sla:scan` (`limit`, 1 to 5000, default 500) | SvcCaseMaintain |
| POST | `/v1/service/work-orders/{id}:follow-up` | SvcWoMaintain (If-Match, Idempotency-Key) |
| GET | `/v1/service/kpis` (`from`, `to`, `territory`) | SvcInquire |

Lists are newest first and page with `cursor` (the previous page's `nextCursor`)
and `limit` (1 to 200, default 50); they see only the caller's legal entities.
Work-order and case responses carry `ETag: "<version>"`, which `If-Match` echoes back.
`internal/api/openapi_test.go` fails when a route and `api/openapi.yaml` disagree,
and `internal/events/events_test.go` does the same for the event payloads and
`events/asyncapi.yaml`.

Duties are declared per operation; OPA enforcement is wired by the platform
kernel (`authz.RequireDuty`). `/healthz`, `/readyz` (pings Postgres) and
`/metrics` (admin port) are kernel standard. The `Dockerfile` builds from the
repository root, like `services/org`.

## Web screens

`apps/web/src/modules/service` (discovered through `module.ts`): service cases (list,
open, detail with SLA clocks, entitlement trace and work orders), service agreements
(list, create, detail with lines and hours used), work orders (list, create from a
case or directly, detail with status moves and posting by coverage) and SLA profiles
(list, create). Slice 2 adds the case status actions on the case detail (start, wait
for customer or parts, resume, resolve, close and cancel, the last two confirmed
first) with the SLA alerts already emitted, the follow-up visit and first-time fix on
the work-order detail, and the KPI dashboard (`/service/kpis`).

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_service
export MIGRATE_ON_START=true          # dev only
go run ./cmd/fieldservice             # HTTP_ADDR :8080, ADMIN_ADDR :9090
```

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START`, `HTTP_ADDR`, `ADMIN_ADDR`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # store and API tests against Postgres
```

Domain tests are table-driven plus `pgregory.net/rapid` properties (calendar
add/subtract inverse, DST-safe). Store and API tests use `platform/testkit` and skip
without `PASSAGE_TEST_DATABASE_URL`.

## Behaviour notes

- SLA clocks count business minutes of the profile calendar (Monday to Friday between
  opening and closing, the profile's holidays excluded) in the profile's time zone
  (`time/tzdata` is embedded); a 24x7 profile counts wall-clock minutes.
  Due = add_business_minutes(opened, target + paused).
- Entitlement: candidates are the customer's ACTIVE agreements in the case's legal
  entity, valid on the opening date; the most specific line (object > category > any)
  wins, then the higher priority rank, then the latest validity start, then the id.
  With no match the case uses the caller's fallback SLA profile and records
  `SVC-ENT-001` in its entitlement trace; without a fallback it is refused
  (`SVC-ENT-002`).
- A work order created from a case inherits the case's agreement. Case and agreement
  must belong to the work order's customer and legal entity.
- Work-order posting (BR-45.4): the domain applies warranty first, then agreement,
  then billable per line. Agreement labor hours are consumed FIFO from the remaining
  included hours; the rest is billable. Parts and travel covered by the agreement post
  at zero. Only an ACTIVE agreement covers anything, and its row is locked while a
  post consumes hours, so concurrent posts cannot spend the same included hour twice.
  Posted orders are immutable. Warranty coverage is supported by the domain
  but not yet sourced from service objects (no installed base in this slice), so
  `warrantyCost` is zero until that lands.
- Case status (FR-45.5): `NEW`, `IN_PROGRESS`, `WAITING_CUSTOMER`, `WAITING_PARTS`,
  `RESOLVED`, `CLOSED`, `CANCELED`. Every open status can move to the other open
  statuses (except back to `NEW`), to `RESOLVED` or to `CANCELED`; `RESOLVED` only
  closes; `CLOSED` and `CANCELED` are final (reopening is BR-45.9, generic cases).
- SLA clocks (BR-45.2): leaving `NEW`, or the first `DISPATCHED` work order of the
  case, is the response. `WAITING_CUSTOMER` pauses the resolution clock, and
  `WAITING_PARTS` does when the profile sets `pauseOnParts`; leaving the pause adds its
  business minutes, and the resolution due and warning instants are recomputed as
  add_business_minutes(opened, target + paused). The response clock does not pause,
  because a case can only wait after it was answered. A clock found past due is
  `BREACHED` and stays so when resolved late; a case resolved in time is `MET`.
- SLA monitor (`POST /sla:scan`, one pass of `service-sla-monitor` for the caller's
  tenant): selects RUNNING clocks of open cases with an alert due and not yet emitted,
  oldest resolution due first, `FOR UPDATE SKIP LOCKED`, at most `limit` per pass. A
  warning fires once per clock at 75 % of the target (rounded up to the minute, so
  never early), a breach once past due; a clock first seen past due emits only the
  breach. Each alert is recorded in `sla_alerts` in the transaction that writes its
  `sla.warning` / `sla.breached` event, so repeated or overlapping passes emit nothing
  twice. A late response or a transition that finds the clock overdue emits the same
  breach event once.
- Follow-up (BR-45.5): a COMPLETED or POSTED order can take a follow-up. The new order
  is UNSCHEDULED without lines, links the parent by `followUpOf` and keeps its case,
  agreement, customer, legal entity, territory, incident type and estimate; the
  parent's `firstTimeFix` (set true at completion) becomes false. A replay with the
  same key returns the same follow-up.
- KPIs (FR-45.11): cases count by `openedAt` in [from, to), work orders by
  `completedAt`. Response is met when answered by its due and missed when answered
  later or still unanswered past due; resolution counts MET and BREACHED clocks (open
  clocks within target are not counted yet). First-time fix counts original orders
  only (not follow-ups). Rates are percentages rounded half-even to two places and
  null with an empty denominator; means are wall-clock minutes.
- Money is decimal (`shopspring/decimal` at `numeric(20,6)`), never float.

## Error codes

Problem details use `application/problem+json`; `type` is `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | Missing or malformed tenant context |
| `COM-0001` | 400 | Request failed validation (`errors` lists fields) |
| `precondition_required` | 428 | `If-Match` missing on a custom method |
| `conflict_version` | 412 | `If-Match` does not match the current version |
| `idempotency_key_reuse` | 422 | Same `Idempotency-Key` with a different body, or a post key already used on another work order |
| `SVC-PROF-001` | 409 | SLA profile code already exists |
| `SVC-PROF-002` | 404 | SLA profile not found |
| `SVC-SLA-003` | 422 | SLA profile has no target for the case priority |
| `SVC-SLA-004` | 422 | SLA calendar is invalid (hours, holidays or time zone) |
| `SVC-AGR-001` | 422 | Referenced service agreement not found |
| `SVC-AGR-002` | 422 | Referenced SLA profile not found |
| `SVC-AGR-003` | 422 | The agreement belongs to another customer or legal entity |
| `SVC-AGR-004` | 404 | Service agreement not found |
| `SVC-ENT-001` | — | Info in the entitlement trace: no agreement matched, fallback profile applied |
| `SVC-ENT-002` | 422 | No agreement matched and no fallback SLA profile supplied |
| `SVC-CASE-001` | 404 / 422 | Service case not found (422 when a work order references it) |
| `SVC-CASE-002` | 422 | The case belongs to another customer or legal entity |
| `SVC-CASE-003` | 422 | Case status transition not allowed by the case status machine |
| `SVC-WO-004` | 404 | Service work order not found |
| `SVC-WO-010` | 422 | Posting requires a COMPLETED work order |
| `SVC-WO-011` | 422 | Status transition not allowed by the work-order state machine (BR-45.5) |
| `SVC-WO-012` | 422 | A follow-up needs a COMPLETED or POSTED work order (BR-45.5) |

`SVC-SLA-001` (warning) and `SVC-SLA-002` (breach) are the messages of the
`sla.warning` and `sla.breached` events, not HTTP errors. The PRD message catalog
(8.45.18) also reserves `SVC-WO-001/002`,
`SVC-DSP-*`, `SVC-TRK-001`, `SVC-SYNC-001` and `SVC-SWC-*` for the features that raise
them; this service does not reuse those numbers.

Codes are stable; new codes are additive. Error codes returned by the platform kernel
are documented there.

## Dependencies

No dependencies beyond the platform kernel module and what it already pulls in
(chi, pgx/v5, shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
