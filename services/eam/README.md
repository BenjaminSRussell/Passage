# EAM (enterprise asset management) — wave 44, slices 1 to 5

Ticket PSG-44. Passage's maintenance context: plant assets, counters and condition
rules, maintenance plans that generate preventive work orders, maintenance requests,
work orders with labor and status transitions, and asset reliability KPIs.
Slice 2 adds asset retirement, criticality assessment, vendor warranties with the
work order warranty check, fault catalogs with fault registration, downtime windows
and request triage. Slice 3 adds permits to work and lockout/tagout (LOTO), which gate
the start of work orders. Slice 4 adds the functional-location hierarchy with asset
installation history, and maintenance sequences. Slice 5 adds maintenance rounds, the
emergency regularization and plan review reminders, and the cost and backlog KPIs. The
authoritative requirements are PRD section 8.44;
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

## What slice 3 adds

- **Permit types and job requirements** (FR-44.19, 8.44.13): `POST /permit-types`
  creates or replaces a type (code, name, approver role, maximum validity 1-168 h,
  requires isolation, requires gas test). `PUT /job-types/{jobType}/permits` replaces
  the permit types a job type requires; work orders carry the job type.
- **Isolation points** (FR-44.19): energy type, location, isolation method,
  verification step and tag code (unique per asset), per asset.
- **Permits** (BR-44.11): `POST /permits` requests a permit (`EAM-PMT-n`) for an open
  work order with a validity window inside the type's maximum; one permit of a type is
  in force per work order at a time. `POST /permits/{id}/transition` (If-Match) moves it
  REQUESTED → APPROVED → ISSUED ⇄ SUSPENDED → CLOSED, or cancels it before issue.
  The approver must differ from the requester; an isolation permit is issued only when
  every isolation point of the asset has a verified lock held by someone besides the
  issuer; CLOSE and CANCEL need every lock removed, and CLOSE needs the work order not
  IN_PROGRESS. Every transition is an append-only `permit_transition` row and a
  `dev.passage.eam.permitstatus.changed.v1` event in the same transaction.
- **Personal LOTO locks** (BR-44.11): `POST /permits/{id}/locks` applies, verifies (the
  zero-energy check) or removes the caller's own lock (`LOCK-n`) on an isolation point;
  one open lock per technician per point. Each action is logged append-only with its key.
- **The work order gate** (BR-44.11, AC-44.11): moving a work order to IN_PROGRESS
  needs, for each permit type its job type requires, a permit that is ISSUED and valid
  now (EAM-PMT-001); for an isolation permit every isolation point must carry a verified
  lock first (EAM-PMT-002).
- **Expiry** (8.44.20 `eam-permit-expiry`): `POST /permits/expire` suspends every ISSUED
  permit past its validity for the caller's tenant and lists them with EAM-PMT-004,
  flagging work orders still IN_PROGRESS. Reruns suspend nothing more.

## What slice 4 does

- **Functional locations** (FR-44.1): `POST /functional-locations` adds a root location
  of a site or a child of a parent in the same site (EAM-FL-002), with an optional
  location type. The path joins the codes from the root with `/` (for example
  `PLANT-1/LINE-2/PUMP-3`), is unique per site (EAM-DUP-001), and the depth is at most
  12 (EAM-FL-001). `GET /functional-locations` pages a site's locations in path order,
  optionally one location's subtree (`under`); the detail lists the children and the
  installed assets.
- **Asset placement and history** (FR-44.1, FR-44.2): `POST /assets/{id}/location`
  (If-Match) installs, moves or removes (`null`) the asset at a location of its own
  site (EAM-FL-002 for another site or no change; EAM-AST-001 for a DISPOSED asset),
  moves the asset version on, and appends INSTALL, MOVE or REMOVE with the locations
  left and reached to the append-only `asset_location_history`. Disposal removes an
  installed asset from its location the same way. The asset carries
  `functionalLocationId` and its detail the location.
- **Maintenance sequences** (FR-44.15, BR-44.10): `POST /sequences` defines job-type
  steps on one asset or one asset type, over a base interval in a time unit or on a
  counter of the asset. Steps have unique codes and hierarchies, multiples of 1 to 1000,
  and may include only lower-hierarchy steps (EAM-SEQ-001). For occurrence k the due
  steps are those with k mod multiple = 0; the one with the highest hierarchy is
  performed, the steps it includes are suppressed, and the other due steps are extra
  jobs. `GET /sequences/{id}/simulation` shows the next occurrences without changing
  anything. `POST /sequences/{id}/occurrences` generates the next occurrence of an asset
  sequence and opens a PREVENTIVE work order for the selected step's job type
  (`workorder.created` in the same transaction); the previous occurrence's order must be
  finished or canceled first (EAM-SEQ-002). Asset type sequences are templates.

## What slice 5 adds

- **Rounds** (FR-44.14): `POST /rounds` (EamPlanAdmin) defines 1 to 50 stops numbered
  1..n in order, each an asset of the round's site with a job type code, a mandatory
  flag and the travel minutes from the previous stop (EAM-RND-003; code unique,
  EAM-DUP-001). A stop job type that requires a permit to work is refused: a round
  order carries no permit per stop. Rounds are create-only.
- **ROUND plan lines** (BR-44.9): a plan line of kind `ROUND` names a `roundId` and a
  time interval (no asset, job type or counter of its own) and schedules by BR-44.1 on
  time only. Each occurrence is one ROUND work order booked on the first stop's asset,
  with the stops copied in order (`work_order_stop`, one job per stop) and
  `workorder.created` in the same transaction. Completion resets the line once for the
  whole round and reschedules it. `POST /rounds/{id}/work-orders` (EamPlanner) opens an
  extra occurrence outside the plan. Every stop asset must be ACTIVE (EAM-AST-001; the
  scheduler skips the line). ROUND orders are never warranty covered, and
  `POST /work-orders` still refuses the ROUND type.
- **Stop outcomes** (BR-44.9): `POST /work-orders/{id}/stops/{seq}` (EamTechnician)
  records DONE or SKIPPED once, while the order is IN_PROGRESS (EAM-RND-002). SKIPPED
  needs a reason; a skipped mandatory stop raises a maintenance request with source
  ROUND for the stop's asset (`request.created`) in the same transaction. The order
  cannot complete while a stop has no outcome (EAM-RND-001), and cannot start while a
  stop's job type has gained a permit requirement (EAM-PMT-001, fail closed). The
  work order detail carries the stops.
- **Emergency regularization** (BR-44.7): `GET /work-orders/emergency-overdue?hours=24`
  lists open priority 1 orders that were never SCHEDULED or RELEASED and were raised at
  least the window ago (1 to 72 h, default 24), each with the warning EAM-WO-003.
- **Plan review reminder** (BR-44.15): `GET /assets/plan-reviews` lists assets of
  criticality 4 or 5 (not disposed) never assessed or last assessed 12 months ago or
  more; the latest criticality assessment is the review record.
- **KPIs** (FR-44.13): `GET /kpis/cost?from&to[&assetType]` sums parts and labor of the
  orders completed in the window per asset and currency (numeric in SQL, decimal in
  Go). `GET /kpis/backlog[?site]` counts open orders per priority with the overdue count
  and the oldest due date.

Domain logic is pure (`internal/domain`) and table-tested, with property tests
(`rapid`). Store integration tests are in `internal/store/store_test.go` and
`internal/store/slice2_test.go`, `internal/store/permits_test.go` and
`internal/store/locations_sequences_test.go` and `internal/store/rounds_test.go`
(AC-44.9 end to end, and the slice 5 paths as `passage_app`); HTTP tests,
including a check that `api/openapi.yaml` lists exactly the served routes, are in
`internal/api`.

The web module is `apps/web/src/modules/eam`: workspace tiles, asset list, create form
and detail (counters, readings, condition rules, KPIs), plan list and editor, request
triage with one-action conversion, and the work order list, create form and detail
(status actions, completion with downtime, labor, history). Slice 2 adds asset
retirement and disposal, the criticality matrix and its versions, warranties, the fault
and downtime cards on the work order, triage actions on the request list, and the fault
catalog designer (`/eam/fault-catalogs`). Slice 3 adds the permit register
(`/eam/permits`), the permit request form, the permit detail with lifecycle actions,
isolation points and personal lock actions, permit type and job requirement setup
(`/eam/permit-types`), isolation points on the asset page and a permits card on the
work order page. Slice 4 adds the plant structure (`/eam/locations`: location tree
with subtree filter, create form, location detail with children and installed assets),
the asset's location card with install, move, remove and history, and maintenance
sequences (`/eam/sequences`: list, editor with step hierarchy, detail with a
simulation, occurrence generation and the generated occurrences). Slice 5 adds rounds
(`/eam/rounds`: list, editor with ordered stops, detail with an extra occurrence), the
round stops card on a ROUND work order (record done or skipped), ROUND lines in the
plan editor, and the KPI page (`/eam/kpis`: cost per asset with a window and asset type
filter, backlog by priority, emergency orders to regularize, plan reviews due).

## Layout

```
cmd/eam/                 entrypoint: dbx.Connect -> dbx.Migrate -> api -> httpx.Serve
internal/domain/         pure rules: scheduler, counters, condition rules, work order lifecycle, KPIs
internal/store/          Postgres repositories (tenant-scoped transactions, outbox in the same tx)
internal/api/            HTTP handlers, duty checks, validation
internal/events/         event types and payloads
migrations/              0001 domain tables + RLS, 0002 outbox and idempotency, 0003 slice 2 tables + RLS,
                         0004 passage_app grants, 0005 permits and LOTO + RLS + grants,
                         0006 functional locations, location history, sequences + RLS + grants,
                         0007 rounds, round stops, work order stops, ROUND plan lines + RLS + grants
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
| EamPlanAdmin | plans, plans/schedule, fault catalogs (PUT), sequences and their occurrences (POST), rounds (POST) |
| EamStructureMaintain | functional locations (POST), asset placement |
| EamRequestCreate | requests |
| EamPlanner | request convert and triage, work order create, round work orders, status and downtime (or EamTechnician) |
| EamTechnician | status and downtime (or EamPlanner), labor, faults, round stop outcomes |
| EamPlanner | permit request |
| EamPermitIssuer | permit transitions (approve, issue, suspend, close, cancel), permit expiry run |
| EamTechnician | personal LOTO lock actions |
| EamSetupAdmin | permit types, job permit requirements (PUT), isolation points (POST) |
| EamInquire | every GET: workspace, assets, assessments, warranties, readings, plans, fault catalogs, requests, work orders, kpis, permit types, job permit requirements, isolation points, permits, functional locations, location history, sequences, simulation, occurrences, rounds, emergency-overdue, plan reviews, cost and backlog KPIs |

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
| EAM-PMT-001 | 422 | Work order cannot start: its job type requires an ISSUED permit valid now |
| EAM-PMT-002 | 422 | Isolation points lack a verified lock (or the asset has none) for an isolation permit |
| EAM-PMT-003 | 422 | Permit cannot close or cancel while locks are applied |
| EAM-PMT-004 | 422 | Permit validity has ended (issue refused; the expiry run lists suspended permits with it) |
| EAM-PMT-005 | 422 | Separation of duties: approver is the requester, or the issuer is the only lock holder |
| EAM-PMT-006 | 422 | Permit or lock state does not allow the action |
| EAM-PMT-007 | 422 | The technician already holds an open lock on the isolation point |
| EAM-PMT-008 | 422 | Permit cannot close while its work order is IN_PROGRESS |
| EAM-FL-001 | 422 | Functional location depth exceeds 12 |
| EAM-FL-002 | 422 | Location rule: parent or target location in another site, or the asset is already there (or not installed, on removal) |
| EAM-SEQ-001 | 422 | Sequence invalid (steps, includes, scope or interval), or an occurrence asked of an asset type sequence |
| EAM-SEQ-002 | 422 | Sequence occurrence while the previous occurrence's work order is still open |
| EAM-RND-001 | 422 | ROUND work order completion while a stop has no outcome (BR-44.9) |
| EAM-RND-002 | 422 | Stop outcome not allowed: order not ROUND or not IN_PROGRESS, stop already recorded, skip without a reason |
| EAM-RND-003 | 422 | Round definition breaks a rule: stop numbering, job type code, travel, stop asset on another site, job type that needs a permit |
| EAM-WO-003 | warning (in the emergency-overdue items) | Emergency work order not regularized within the window (BR-44.7) |
| EAM-NF-001 | 404 | Asset, counter, work order, request, plan, fault catalog, permit type, isolation point, permit, lock, functional location, sequence, round or round stop not found |
| EAM-VAL-001 | 400 | Request failed validation |
| EAM-DUP-001 | 409 | Plan code, counter name, isolation tag code (per asset), location path (per site) or sequence code already used, or a permit of the type is already in force on the work order |
| idempotency_key_reuse | 422 | Idempotency-Key already used for another counter, work order, rule, request, asset assessment, warranty, isolation point, permit, lock, asset placement or sequence occurrence |
| conflict_version | 412 | If-Match version does not match the current version |
| precondition_required | 428 | If-Match missing on a versioned action |
| idempotency_key_required | 400 | Idempotency-Key missing on a creating or custom POST |
| AUTH-0001 | 401 | No tenant or principal |
| AUTH-0003 | 403 | Missing duty |

Codes from PRD 8.44.18 that these slices do not raise (EAM-WO-002..004, EAM-BUD-001,
EAM-REF-001) belong to later slices. EAM-PMT-005..008, EAM-FL-002 and EAM-SEQ-001..002
are service codes for BR-44.11, FR-44.1, FR-44.15 and 8.44.17 rules the PRD states
without a code. EAM-WAR-001 is informational: the work order carries `warrantyCovered`
and `warrantyId` instead of a message.

## Wave reference

PRD section 8.44 (`docs/prd/phase-8-projects-people-service.md`). Depends on PSG-43.
Downstream consumers: wave 23 scheduler (`downtime.recorded`), wave 45 supplier
warranty claims (`warranty.claimable`), mobile and safety notifications
(`permitstatus.changed`), wave 16 inventory (parts, later), ledger posting
(WO costs, later).
