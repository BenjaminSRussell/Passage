# EAM (enterprise asset management) — wave 44, slices 1 and 2

Ticket PSG-44. Passage's maintenance context: plant assets, counters and condition
rules, maintenance plans that generate preventive work orders, maintenance requests,
work orders with labor and status transitions, and asset reliability KPIs.
Slice 2 adds asset retirement, criticality assessment, vendor warranties with the
work order warranty check, fault catalogs with fault registration, downtime windows
and request triage. The authoritative requirements are PRD section 8.44;
NOT-IMPLEMENTED.md lists what the slices leave out and why.

## What slice 1 does

- **Assets** (FR-44.2): create in CREATED, activate to ACTIVE with an If-Match version.
  Only ACTIVE assets receive work orders (EAM-AST-001). The asset detail read carries
  its counters, condition rules, open work orders and the sum of its work order costs.
- **Counters and readings** (FR-44.3): cumulative counters reject a reading below the
  reading before it in time, or above the reading after it (readings may arrive out of
  order), unless `reset` is set (EAM-CTR-001). Only the newest reading moves the
  counter's last value. The average daily usage is recomputed over the readings of the
  last 30 days (increments summed, a reset counting from zero). A repeated
  `Idempotency-Key` or `messageId` replays the stored reading (`Idempotent-Replayed: true`).
- **Condition rules** (FR-44.12, BR-44.4): a `>`/`<` threshold on a counter. A breach
  raises one IOT maintenance request per (asset, rule), deduplicated while a request
  is NEW or TRIAGED and within the rule cooldown (default 60 min). The rule is
  evaluated inside the reading transaction, so the request exists when the reading
  commits.
- **Plans and the scheduler** (FR-44.4/44.5, BR-44.1): TIME, COUNTER, TIME_OR_COUNTER
  and TIME_AND_COUNTER lines. Calendar arithmetic is in UTC; the counter forecast is
  whole days ahead, rounded up (the BR-44.1 example gives 2026-11-21). A line may carry
  its last completion over from a migrated plan (`lastCompletedAt`,
  `lastCompletedReading`). `domain.Compute` gives the next due date and reading;
  `domain.Generate` decides whether a work order is due within the horizon less the
  early tolerance. The scheduler runs when a plan is created, on every reading of the
  counter a line uses, on every completion of a line's work order (BR-44.2), and on
  the nightly `POST /plans/schedule` sweep. It locks the lines it evaluates and never
  creates a second open work order per line.
- **Maintenance requests** (FR-44.6): user, mobile and quality requests, plus one-action
  conversion to a work order (severity 1 becomes EMERGENCY at priority 1, otherwise
  CORRECTIVE at the criticality default, BR-44.15).
- **Work orders** (FR-44.7, BR-44.3): PREVENTIVE, CORRECTIVE, INSPECTION and EMERGENCY
  orders (the 8.44.13 default set). Without a priority, EMERGENCY gets 1 and CORRECTIVE
  gets 6 − criticality (BR-44.15). CAPITAL orders are refused with EAM-WO-005 until a
  fixed asset or CIP project can be linked (wave 37). Lifecycle transitions use
  optimistic versions (If-Match) and an Idempotency-Key that replays a retried
  transition; each one is kept in an append-only status history with its actor.
  Completion needs ordered actual times (EAM-WO-006) and records the downtime minutes.
  Emergency orders (priority 1) may start before release (BR-44.7). Labor is booked at
  trade cost in decimal (FR-44.10).
- **Reads**: paged lists (newest first, `limit` and `cursor`) of assets, plans with
  their lines and next due points, requests and work orders, the asset and work order
  details, a counter's readings and the workspace tiles.
- **KPIs** (FR-44.13, BR-44.8): MTBF, MTTR and availability for one asset over a
  window.

## What slice 2 adds

- **Asset lifecycle** (FR-44.2): `POST /assets/{id}/lifecycle` retires an ACTIVE asset to
  INACTIVE or disposes of a CREATED or INACTIVE one, with an If-Match version. DISPOSED
  is final, and an asset with open work orders cannot be disposed (EAM-AST-001).
  INACTIVE assets take no new work orders and the scheduler skips their lines;
  `/activate` brings them back.
- **Criticality assessment** (FR-44.22, BR-44.15): consequences (SAFETY, ENVIRONMENT,
  PRODUCTION, QUALITY, COST) and likelihood, each 1-5. risk = worst consequence ×
  likelihood; criticality is 5 when risk ≥ 20 or SAFETY = 5, then 4/3/2 at 15/10/5.
  Each assessment is a new version; the asset takes the derived criticality (and a new
  asset version), so new CORRECTIVE orders default to priority 6 − criticality (AC-44.16).
- **Warranties** (FR-44.17, BR-44.14): vendor warranties per asset (type, vendor party,
  start basis, start date, months, parts and/or labor coverage, excluded job types).
  Every work order creation path (by hand, request conversion, plan generation) checks
  them on the order's date (its due date, or now): the newest ACTIVE warranty in force
  whose exclusions do not name the job type is stored on the order (`warrantyId`,
  `warrantyCovered`). Completing a covered order emits
  `dev.passage.eam.warranty.claimable.v1` with the covered parts and labor amounts
  (decimal, minor unit) when they are above zero.
- **Fault catalogs and faults** (FR-44.16, AC-44.15): one catalog per asset type
  (symptoms, areas, types, causes, remedies and the allowed symptom/area/type
  combinations), read with GET (ETag = version) and replaced with PUT and If-Match
  (`"0"` for a new catalog). Technicians register the fault on an open order; the codes
  must fit the asset type's catalog (EAM-FLT-002). Registrations are append-only and the
  newest is current, so a replayed key never brings back a superseded one. CORRECTIVE
  and EMERGENCY orders cannot complete without a fault (EAM-FLT-001).
- **Downtime windows** (BR-44.5): `POST /work-orders/{id}/downtime` books [from, to) on
  the production resource (the asset's by default) of an open order. Whole minutes add
  to the order's downtime and `dev.passage.eam.downtime.recorded.v1` goes to the outbox
  for wave-23 capacity. Windows on one order may not overlap, and a completion may not
  report less downtime than the windows (EAM-DT-001).
- **Request triage** (FR-44.6): TRIAGE, DEFER and REJECT with an optional note, kept in an
  append-only history whose key replays a retried action; each change emits
  `dev.passage.eam.request.triaged.v1`. CONVERTED and REJECTED requests are final.

Domain logic is pure (`internal/domain`) and table-tested, with property tests
(`rapid`). Store integration tests are in `internal/store/store_test.go` and
`internal/store/slice2_test.go`; HTTP tests,
including a check that `api/openapi.yaml` lists exactly the served routes, are in
`internal/api`.

The web module is `apps/web/src/modules/eam`: workspace tiles, asset list, create form
and detail (counters, readings, condition rules, KPIs), plan list and editor, request
triage with one-action conversion, and the work order list, create form and detail
(status actions, completion with downtime, labor, history). Slice 2 adds asset
retirement and disposal, the criticality matrix and its versions, warranties, the fault
and downtime cards on the work order, triage actions on the request list, and the fault
catalog designer (`/eam/fault-catalogs`).

## Layout

```
cmd/eam/                 entrypoint: dbx.Connect -> dbx.Migrate -> api -> httpx.Serve
internal/domain/         pure rules: scheduler, counters, condition rules, work order lifecycle, KPIs
internal/store/          Postgres repositories (tenant-scoped transactions, outbox in the same tx)
internal/api/            HTTP handlers, duty checks, validation
internal/events/         event types and payloads
migrations/              0001 domain tables + RLS, 0002 outbox and idempotency, 0003 slice 2 tables + RLS
api/openapi.yaml         contract for every endpoint
events/asyncapi.yaml     contract for every event
charts/eam/              Helm chart (passage-eam)
```

## Run

```
DATABASE_URL=postgres://... HTTP_ADDR=:8080 go run ./cmd/eam
```

Every request needs the gateway headers (`X-Passage-Tenant`, `X-Passage-Duties`,
optional `X-Passage-User`). Creating POSTs need `Idempotency-Key`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store tests
```

Without `PASSAGE_TEST_DATABASE_URL` the store tests skip (kernel testkit rule). With
`PASSAGE_TEST_APP_DATABASE_URL` as well (a non-owner role named `passage_app`), the
row-level security test runs the store as that role.

## Duties

| Duty | Routes |
|---|---|
| EamAssetMaintain | assets, activate, lifecycle, criticality, warranties, counters, rules |
| EamCounterRegister | readings |
| EamPlanAdmin | plans, plans/schedule, fault catalogs (PUT) |
| EamRequestCreate | requests |
| EamPlanner | request convert and triage, work order create, status and downtime (or EamTechnician) |
| EamTechnician | status and downtime (or EamPlanner), labor, faults |
| EamInquire | every GET: workspace, assets, assessments, warranties, readings, plans, fault catalogs, requests, work orders, kpis |

## Error codes

Problem details are `application/problem+json`. The `code` field carries the codes below.

| Code | HTTP | Meaning |
|---|---|---|
| EAM-CTR-001 | 422 | Cumulative counter reading below the last reading without reset |
| EAM-AST-001 | 422 | Asset not in a state that allows the action (activation, retirement, disposal, work order, request conversion) |
| EAM-WO-001 | 422 | Work order transition or labor not allowed in the current state |
| EAM-WO-005 | 422 | CAPITAL work order without a fixed asset or CIP project |
| EAM-WO-006 | 422 | Completion without ordered actual start and end times |
| EAM-PLN-003 | 422 | Plan line invalid (kind, interval, counter, or tolerance) |
| EAM-REQ-001 | 422 | Request state does not allow the action (convert needs NEW or TRIAGED; triage is refused on CONVERTED or REJECTED) |
| EAM-FLT-001 | 422 | CORRECTIVE or EMERGENCY work order completed without a registered fault |
| EAM-FLT-002 | 422 | Fault not allowed by the asset type's catalog, or the type has no catalog |
| EAM-DT-001 | 422 | Downtime window overlaps one on the order, no production resource to book, or completion downtime below the windows |
| EAM-NF-001 | 404 | Asset, counter, work order, request, plan or fault catalog not found |
| EAM-VAL-001 | 400 | Request failed validation |
| EAM-DUP-001 | 409 | Plan code or counter name (per asset) already used |
| idempotency_key_reuse | 422 | Idempotency-Key already used for another counter, work order, rule, request, asset assessment or warranty |
| conflict_version | 412 | If-Match version does not match the current version |
| precondition_required | 428 | If-Match missing on a versioned action |
| idempotency_key_required | 400 | Idempotency-Key missing on a creating or custom POST |
| AUTH-0001 | 401 | No tenant or principal |
| AUTH-0003 | 403 | Missing duty |

Codes from PRD 8.44.18 that these slices do not raise (EAM-WO-002..004, EAM-PMT-*,
EAM-BUD-001, EAM-REF-001) belong to later slices. EAM-WAR-001 is informational: the work
order carries `warrantyCovered` and `warrantyId` instead of a message.

## Wave reference

PRD section 8.44 (`docs/prd/phase-8-projects-people-service.md`). Depends on PSG-43.
Downstream consumers: wave 23 scheduler (`downtime.recorded`), wave 45 supplier
warranty claims (`warranty.claimable`), wave 16 inventory (parts, later), ledger posting
(WO costs, later).
