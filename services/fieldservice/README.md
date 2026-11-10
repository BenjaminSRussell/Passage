# fieldservice (wave 45, slices 1 to 7)

Service management core of Passage: service agreements with SLA profiles, the
entitlement resolver, SLA clocks on service cases with the case status machine,
pause and resume, the SLA monitor (warning and breach events), service work orders
with coverage-aware billing, the work-order status machine with follow-up orders and
first-time fix, service KPIs, the installed base with warranty terms that cover
work-order lines, activities on any record with an iCalendar export, and the
knowledge base with versioned articles, review, publishing and suggestions, and
generic case management: case processes with stage gates, a case category tree with
per-category SLA profile, resolution codes and access duty, cases in any non-service
category with associations to any record, a case log and reopen, supplier warranty
claims with vendor recovery, email-to-case intake through case mailboxes, and dispatch: technicians,
work-order scheduling data, manual assignment under the BR-45.3 hard constraints, the dispatch board
and publishing a territory-day. It replaces AX
service management (agreements, service orders, service objects), the AX/D365 case
framework (CaseDetail, CaseCategory, CaseProcess) and SAP CS (service orders,
installed base, warranty check, solution database, WTY warranty claims) for these slices.

Wave reference: PSG-45 / `docs/prd/phase-8-projects-people-service.md` section 8.45.
Dependencies on earlier waves (locations, pricing, serial shipments, invoicing,
workers) are reached through events or handoffs; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/fieldservice        entrypoint: dbx.Connect -> dbx.Migrate (optional) -> httpx router -> serve
internal/domain         pure rules: SLA business-minute calendar, SLA clocks with pauses and the
                        monitor's warning/breach verdicts, case status machine, entitlement
                        resolver (BR-45.1), work-order status machine and coverage posting
                        (BR-45.4/5), KPI rates, warranty start and term (FR-45.2), activity
                        status moves and the RFC 5545 export, the article lifecycle (BR-45.11),
                        case process stages, gate, resolution and reopen rules (BR-45.9, BR-45.10)
internal/store          Postgres repositories under RLS (dbx.WithTenantTx), idempotency, outbox
internal/api            HTTP handlers under /v1/service and /v1/cases, validation, problem details
internal/events         event types and payloads (see events/asyncapi.yaml)
migrations/             0001_init.sql (RLS, tenant_id first, money numeric(20,6) + currency),
                        0002/0003 passage_app grants, 0004_case_lifecycle.sql (case version, pause,
                        warning instants, emitted alerts; work-order completion, follow-up, first-time fix),
                        0005_objects_activities_knowledge.sql (service objects, activities, knowledge
                        articles and votes, with their passage_app grants),
                        0006_generic_cases.sql (case processes, categories, generic cases,
                        associations and the case log, with their passage_app grants),
                        0007 supplier claims and email intake, 0008_agreement_lifecycle_visits.sql
                        (agreement version and status log, included-visit consumption),
                        0009_dispatch.sql (technicians, work-order scheduling and assignment columns,
                        published dispatch runs, with their passage_app grants),
                        0011_dispatch_optimization.sql (optimizer runs: snapshot, result, outcome, backoff)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every event published
charts/fieldservice     Helm chart (deployment, service, optional ServiceMonitor)
```

## Endpoints

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/service/sla-profiles` | fieldservice.agreement.maintain |
| GET | `/v1/service/sla-profiles`, `/v1/service/sla-profiles/{id}` | fieldservice.inquire |
| POST | `/v1/service/agreements` | fieldservice.agreement.maintain |
| GET | `/v1/service/agreements` (`status`, `customerPartyId`), `/v1/service/agreements/{id}` (ETag) | fieldservice.inquire |
| POST | `/v1/service/agreements/{id}:transition` (activate, suspend, resume, cancel) | fieldservice.agreement.maintain (If-Match, optional Idempotency-Key) |
| POST | `/v1/service/agreements:expire` (one expiry pass, `limit`) | fieldservice.agreement.maintain |
| POST | `/v1/service/agreements/{id}/visits` | fieldservice.agreement.maintain (Idempotency-Key) |
| GET | `/v1/service/agreements/{id}/visits`, `/v1/service/agreements/{id}/history` | fieldservice.inquire |
| POST | `/v1/service/cases` | fieldservice.service-case.create |
| GET | `/v1/service/cases` (`status`, `slaState`, `customerPartyId`), `/v1/service/cases/{id}` | fieldservice.inquire |
| POST | `/v1/service/work-orders` | fieldservice.work-order.maintain |
| GET | `/v1/service/work-orders` (`status`, `territoryId`, `caseId`, `customerPartyId`), `/v1/service/work-orders/{id}` | fieldservice.inquire |
| POST | `/v1/service/work-orders/{id}:transition` (not to SCHEDULED or DISPATCHED; completing needs the signature) | fieldservice.work-order.maintain (If-Match) |
| POST | `/v1/service/work-orders/{id}:post` | fieldservice.billing.post (If-Match, Idempotency-Key) |
| POST | `/v1/service/cases/{id}:transition` | fieldservice.service-case.maintain (If-Match; Idempotency-Key optional) |
| POST | `/v1/service/sla:scan` (`limit`, 1 to 5000, default 500) | fieldservice.service-case.maintain |
| POST | `/v1/service/work-orders/{id}:follow-up` | fieldservice.work-order.maintain (If-Match, Idempotency-Key) |
| GET | `/v1/service/kpis` (`from`, `to`, `territory`) | fieldservice.inquire |
| POST | `/v1/service/service-objects` | fieldservice.service-object.maintain (Idempotency-Key) |
| POST | `/v1/service/service-objects:receive-shipment` | fieldservice.service-object.maintain (idempotent per item and serial) |
| GET | `/v1/service/service-objects` (`customerPartyId`, `itemId`, `serial`, `parentId`, `status`), `/v1/service/service-objects/{id}` | fieldservice.inquire |
| POST | `/v1/service/service-objects/{id}:install` | fieldservice.service-object.maintain (If-Match) |
| POST | `/v1/service/activities` | fieldservice.activity.maintain (Idempotency-Key) |
| GET | `/v1/service/activities` (`owner`, or `entityType` with `entityId`; `status`), `/v1/service/activities/{id}` | fieldservice.activity.maintain |
| GET | `/v1/service/activities:calendar` (same filter) | fieldservice.activity.maintain |
| POST | `/v1/service/activities/{id}:complete`, `:cancel`, `:reschedule` | fieldservice.activity.maintain (If-Match) |
| POST | `/v1/service/knowledge/articles` | fieldservice.knowledge-article.author (Idempotency-Key) |
| GET | `/v1/service/knowledge/articles` (`status`, `lang`, `number`), `/v1/service/knowledge/articles/{id}` | fieldservice.knowledge-article.read |
| GET | `/v1/service/knowledge/articles/search` (`q`, `itemId`, `incidentTypeId`, `assetTypeId`, `lang`, `limit` 1 to 20) | fieldservice.knowledge-article.read |
| PUT | `/v1/service/knowledge/articles/{id}` | fieldservice.knowledge-article.author (If-Match) |
| POST | `/v1/service/knowledge/articles/{id}:new-version`, `:submit` | fieldservice.knowledge-article.author (If-Match) |
| POST | `/v1/service/knowledge/articles/{id}:publish`, `:reject`, `:retire` | fieldservice.knowledge-article.publish (If-Match) |
| POST | `/v1/service/knowledge/articles/{id}:vote` | fieldservice.knowledge-article.read |
| POST | `/v1/case-processes`, `/v1/case-categories` | fieldservice.case-setup.admin (Idempotency-Key) |
| GET | `/v1/case-processes`, `/v1/case-categories` | any of fieldservice.case.maintain, fieldservice.case.create, fieldservice.case-setup.admin |
| POST | `/v1/cases` | fieldservice.case.create (Idempotency-Key; and the category's access duty) |
| GET | `/v1/cases` (`categoryId`, `status`, `stage`, `ownerId`, `entityType` with `entityId`), `/v1/cases/{id}` | fieldservice.case.maintain (and BR-45.10 access) |
| POST | `/v1/cases/{id}/stage`, `/v1/cases/{id}:transition`, `/v1/cases/{id}:reopen` | fieldservice.case.maintain (If-Match, Idempotency-Key) |
| POST | `/v1/cases/{id}/associations` | fieldservice.case.maintain (Idempotency-Key) |
| POST | `/v1/service/supplier-claims` | fieldservice.supplier-claim.maintain (Idempotency-Key) |
| GET | `/v1/service/supplier-claims` (`vendorPartyId`, `status`), `/v1/service/supplier-claims/{id}` | fieldservice.supplier-claim.maintain or fieldservice.inquire |
| POST | `/v1/service/supplier-claims/{id}:submit`, `/{id}/decision`, `/{id}:settle`, `/{id}:close` | fieldservice.supplier-claim.maintain (If-Match, Idempotency-Key) |
| POST | `/v1/case-mailboxes` | fieldservice.case-setup.admin (upsert by address) |
| GET | `/v1/case-mailboxes` | any of fieldservice.case.maintain, fieldservice.case.create, fieldservice.case-setup.admin |
| POST | `/v1/inbound-messages` | fieldservice.case.create (idempotent per Message-ID; and the default category's access duty for a new case) |
| GET | `/v1/inbound-messages` (`mailbox`, `caseId`, `route`) | fieldservice.case.maintain (and BR-45.10 access) |

| POST | `/v1/service/technicians` | fieldservice.technician.dispatch (Idempotency-Key) |
| GET | `/v1/service/technicians` (`territoryId`, `active`), `/v1/service/technicians/{id}` (ETag) | fieldservice.inquire |
| PUT | `/v1/service/technicians/{id}` | fieldservice.technician.dispatch (If-Match) |
| PUT | `/v1/service/work-orders/{id}/scheduling` (skills, customer window, address, position, estimate; UNSCHEDULED orders) | fieldservice.work-order.maintain (If-Match) |
| POST | `/v1/service/work-orders/{id}:assign`, `:unassign` | fieldservice.technician.dispatch (If-Match) |
| GET | `/v1/service/dispatch/board` (`territoryId`, `date`, `timeZone`) | fieldservice.inquire |
| POST | `/v1/service/dispatch/publish` | fieldservice.technician.dispatch (Idempotency-Key) |
| POST | `/v1/service/work-orders/{id}/actions` (a batch of the technician's offline actions; each action id idempotent) | fieldservice.work-order.execute |
| GET | `/v1/service/work-orders/{id}/actions` (the log in merge order) | fieldservice.inquire |
| GET | `/v1/service/dispatch/runs` (`territoryId`), `/v1/service/dispatch/runs/{id}` | fieldservice.inquire |
| POST | `/v1/service/dispatch/optimizations` (`territoryId`, `date`, `timeZone`, `timeLimitSeconds`), `/{id}:sync`, `/{id}:retry`, `/{id}:accept` | fieldservice.technician.dispatch (Idempotency-Key on create) |
| GET | `/v1/service/dispatch/optimizations` (`territoryId`), `/v1/service/dispatch/optimizations/{id}` | fieldservice.inquire |
Lists are newest first (activities: earliest due first, undated last) and page with `cursor` (the previous page's `nextCursor`)
and `limit` (1 to 200, default 50); they see only the caller's legal entities.
Work-order, case (service and generic), service-object and activity responses carry `ETag: "<version>"`, and
article responses `ETag: "<revision>"`, which `If-Match` echoes back.
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
the work-order detail, and the KPI dashboard (`/service/kpis`). Slice 3 adds the
installed base (list with customer and serial search, register, receive a shipment,
detail with warranty, components and installation), "My activities" (list by owner
and status, create on any record, complete, cancel, reschedule, calendar download)
with an activities panel on the case detail, and the knowledge base (list by status,
search, author, edit a draft, submit, publish, send back, retire, new version, vote)
with article suggestions on the case detail. Slice 4 adds generic case management:
the case list across categories (`/service/case-management`, filtered by category,
status, stage and associated record), opening a case, the case detail (stages with
the current stage's required activities and "Add" links that create them on the
case, advance, back with a reason, resolve with a category code, close, cancel,
reopen, associated records, the case log and the activities panel) and case setup
(`/service/case-setup`: category and process lists, new category, new process).
Slice 5 adds supplier warranty claims (`/service/supplier-claims`: list by status,
create from posted work orders or EAM work order lines, detail with lines, submit,
the vendor decision, the credit-note match and close) and the case mailbox
(`/service/case-mailbox`: mailboxes and their default categories, set up a mailbox,
and the inbound email list with where each message landed, linking to its case).
Slice 6 adds the agreement lifecycle on the agreement detail (activate, suspend and
cancel with a reason, resume, the status history), the included visits of each line
(consumed, listed and recorded with an optional work order) and the "Expire ended
agreements" pass on the agreement list. Slice 7 adds technicians (`/service/technicians`:
list by status and territory, register, detail with edit), the dispatch board
(`/service/dispatch`: pick a territory and day, see each technician's visits and booked
minutes and the unscheduled work orders, assign with a start time and a pin, unassign,
publish the day; a refused assignment lists every rule it broke) and a scheduling panel
on the work-order detail (required skills, customer window, address, position, estimate,
and the assignment). Times on these screens are UTC.

## Run

```
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_service
export MIGRATE_ON_START=true          # dev only
go run ./cmd/fieldservice             # HTTP_ADDR :8080, ADMIN_ADDR :9090
```

Environment: `DATABASE_URL` (required), `MIGRATE_ON_START`, `HTTP_ADDR`, `ADMIN_ADDR`, `DISPATCH_URL` (base URL of passage-dispatch; without it optimizations answer 503 `SVC-DSP-017`) with the service-token signing key.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:...@... go test ./...
# store and API tests against Postgres, application queries as the NOSUPERUSER NOBYPASSRLS passage_app
```

Domain tests are table-driven plus `pgregory.net/rapid` properties (calendar
add/subtract inverse, DST-safe). Store and API tests use `platform/testkit` and skip
without `PASSAGE_TEST_DATABASE_URL`.

## Behaviour notes

- Dispatch optimization (BR-45.3, issue #45): a request snapshots the territory-day's open work orders, qualified technicians and held visits into one run for passage-dispatch (CP-SAT, greedy fallback), over a service token carrying only `dispatch.run.create` or `dispatch.run.read`. There is no background worker: `:sync` polls the run. A failure backs off `min(3600, 60*2^n)` seconds and parks at the 5th failure, or at once when dispatch refuses the problem (400/422); `:retry` revives a parked run. `:accept` writes the plan through the normal assignment path, one savepoint per visit, reports what it could not apply, and a second accept returns the stored outcome. A held visit the technician no longer qualifies for (skill, territory, shift or window changed since the assignment) takes the technician out of the run with the reason `HELD_VISIT_STALE`, listed with the visit in `skipped` so a dispatcher can fix the assignment; the other technicians are still planned. A run still QUEUED or RUNNING after 10 minutes counts as a failure and is submitted again, and each `:retry` starts a new idempotency generation (`<id>:<generation>:<attempt>`) so dispatch does not replay the earlier failed run. Requesting needs a caller scoped to the one legal entity (403 otherwise).
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
  Posted orders are immutable. Warranty coverage comes from the service object
  named on the work order's case: its term is active from `warrantyStartsOn` for
  `months` months on the completion day of the order, and its parts, labor and
  travel flags decide which lines are WARRANTY (zero-priced, unit cost recorded as
  warranty cost).
- Installed base (FR-45.1, FR-45.2): a service object is one serial of an item for a
  customer, unique per item and serial, optionally under a parent of the same
  customer. Its warranty starts on the ship day (SHIP), the installation day
  (INSTALL, set by `:install`) or the day it was registered in the installed base
  (REGISTRATION); `warrantyEndsOn` is the first day it no longer covers. A case may
  name only an object of its own customer (`SVC-OBJ-005`).
  `:receive-shipment` stands in for the wave-28 shipment event: it creates one ACTIVE
  object per serial and counts serials that already have one as existing.
- Activities (FR-45.14): TASK, APPOINTMENT, CALL_NOTE or EMAIL_NOTE owned by a worker,
  with due, start, end, reminder and associations to records (`case`, `work_order`,
  `project`, ...). OPEN can be completed (DONE with `completedAt`), canceled or
  rescheduled; DONE and CANCELED are final, and repeating the same move is a no-op.
  The calendar export is RFC 5545 (escaped, folded at 75 octets, CRLF), one VEVENT
  per activity with a start or due instant.
- Knowledge base (FR-45.13, BR-45.11): each version is a row grouped by `number`
  (KB-{seq}); DRAFT can be edited and submitted, IN_REVIEW published (by a reviewer
  other than the author, `KB-PUB-001`) or sent back, PUBLISHED retired or revised
  into a new DRAFT version (one open version at a time, `KB-ART-003`). Publishing
  retires the earlier PUBLISHED version of the same language in the same transaction
  (`KB-PUB-002` warning) and writes `dev.passage.knowledge.article.published.v1`.
  Search ranks PUBLISHED articles of the language by ts_rank (normalized to the best
  match) × 0.6 + item 0.25 + incident type 0.10 + asset type 0.05 and returns the top
  5 by default. Each user has one helpfulness vote per version; the helpful percent
  shows from ten votes.
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
- Generic cases (FR-45.12) live in `service_generic_case`; SERVICE and
  CUSTOMER_SERVICE categories keep the entitlement and SLA engine of
  `/v1/service/cases` and are refused by `POST /v1/cases` (`CASE-CAT-002`). Both
  kinds share the `CS` number sequence, so a CS number names one case. COLLECTIONS
  needs a customer (`CASE-CUS-001`). Priority defaults to the category's default
  priority. A category SLA profile sets `slaResponseDue` and `slaResolutionDue` at
  open with the business calendar of BR-45.2; there is no pause or monitor on these
  dues yet.
- Stages (BR-45.9): a case starts at the first stage of its category's process.
  `POST /v1/cases/{id}/stage` enters the next stage only when every required activity
  of the current stage is DONE on the case: an activity whose associations hold
  `{entityType: "case", entityId: <case id>}` and whose subject equals the template.
  A refusal is `CASE-STG-001` and names what is missing. Back one stage needs a
  reason; anything else is `CASE-STG-002`. Each move is logged and writes
  `dev.passage.case.stage.changed.v1` in the same transaction.
- Status: OPEN to RESOLVED (a resolution code, from the category's list when it has
  one: `CASE-RES-001`, `CASE-RES-002`) or CANCELED; RESOLVED to CLOSED. CANCELED is
  final. `:reopen` takes a RESOLVED or CLOSED case: within the category's reopen
  window (days since resolution, default 14; 0 disables it) the case itself returns
  to OPEN at its stage; after it a new case opens at the first stage, linked by
  `reopenedFrom`, carrying the associations, and the call answers 201.
- Access (BR-45.10): a category's `accessDuty` limits its cases to holders of that
  duty and the case owner (the user who opened it), for open, read, list and every
  change (`CASE-SEC-001`, 403); the list leaves inaccessible cases out. A category
  without one is open to every fieldservice.case.maintain holder.
- Associations: any registry record type of `domain.AssociableTypes` with role
  SUBJECT or RELATED; associating the same record again replaces the role. The case
  version moves (the ETag changes), but the call needs no If-Match (PRD 8.45.5).
- Idempotency on the generic case commands: create calls store the key on the new
  row; stage, transition, reopen and association calls store their answer in
  `idempotency_keys`, so a retry with the same key and body returns the first
  answer (even after the version moved on); the fingerprint includes the command and
  the case, so a key reused elsewhere is refused (`idempotency_key_reuse`).
- Supplier warranty claims (FR-45.15, BR-45.12): a SERVICE_WO claim names POSTED
  work orders and takes one line per order from its WARRANTY lines (part warranty
  cost and labor hours, with the item and serial of the case's service object); an
  EAM_WO claim carries its lines. A line claims part + hours × the vendor's labor
  rate, rounded half-even to the currency; a rate of 0 is a vendor that does not
  cover labor. A work order is on one claim only (`SVC-SWC-009`). DRAFT →
  SUBMITTED (today inside `windowDays` after the completion, default 30:
  `SVC-SWC-001`; a return order first when `returnRequired`: `SVC-SWC-002`) →
  ACCEPTED, PARTIALLY_ACCEPTED or REJECTED (`/decision`; never more than claimed per
  line: `SVC-SWC-003`) → SETTLED by the credit note quoting the vendor reference and
  crediting exactly the accepted total, which writes
  `dev.passage.service.supplierclaim.settled.v1` for the GL warranty recovery →
  CLOSED. A REJECTED claim closes with its reason and posts nothing.
- Email intake (FR-45.16, BR-45.13): a mailbox maps an address to a legal entity
  and a default category (not SERVICE, CUSTOMER_SERVICE or COLLECTIONS). An inbound
  message is attached to the case its `[CS-n]` subject token names (padding
  optional: `[CS-1042]` is CS-001042) or the case already holding its In-Reply-To,
  token first, and logged on that case (`EMAIL`, info `CASE-MAIL-001`); when that case
  is CLOSED or CANCELED a new case continues it (same category, customer and
  associations) linked by `reopenedFrom`; otherwise a new case opens in the
  mailbox's default category, owned by the caller (the hub principal). Only a new
  case is acknowledged, and never for an auto-reply (Auto-Submitted other than
  `no`, or X-Autoreply): the response says `acknowledge` and the hub sends it. A
  case the caller may not access is never matched. The Message-ID is unique per
  tenant; a redelivery answers the stored result with `Idempotent-Replayed`.
- Dispatch (FR-45.7, BR-45.3). A technician has territories, skill codes (free text until wave 42
  owns skills), a time zone, working weekdays, a shift (minutes of the local day), a daily maximum
  and an overtime flag. `:assign` places an UNSCHEDULED or SCHEDULED work order at a start time (the
  end is start + the estimate) and makes it SCHEDULED, locking the technician's row so two
  dispatchers cannot book the same time. It refuses with every broken hard constraint at once (422,
  `code` the first, `errors` all): the technician must be active and of the order's legal entity
  and territory (`SVC-DSP-004`), hold every required skill (`SVC-DSP-003`), keep the visit inside
  the customer window (`SVC-DSP-005`) and inside one shift of their local day, overtime widening the
  end by 120 minutes (`SVC-DSP-006`), keep a 5 minute buffer to every other booking
  (`SVC-DSP-007`), and stay within the daily maximum counting work plus buffers
  (`SVC-DSP-008`). Travel between visits is the fixed buffer until the matrix client exists.
  `:unassign` releases a SCHEDULED order (pinned or not). `dispatch/publish` moves the
  SCHEDULED orders that start on the territory's local day to DISPATCHED (which stops the response
  clock of their case), records the run with a route sequence per technician and writes
  `dispatch.published.v1`; with nothing to dispatch it answers `SVC-DSP-009`. Scheduling data of an
  order cannot change once it is scheduled. Assigning and unassigning write
  `assignment.changed.v1`. A work order moved by hand with `:transition` to SCHEDULED or
  DISPATCHED has no technician; the board does not show it.
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
| `SVC-AGR-005` | 422 | Agreement status move not allowed (EXPIRED is set only by the expiry pass) |
| `SVC-AGR-006` | 422 | Activating an agreement whose validTo is behind today |
| `SVC-AGR-007` | 422 | Visits are recorded on ACTIVE agreements only |
| `SVC-AGR-008` | 422 | The visit date is outside the agreement's validity |
| `SVC-AGR-009` | 422 | The agreement has no such line |
| `SVC-AGR-010` | 422 | The line's included visits are all used |
| `SVC-AGR-011` | 422 | The work order is unknown or belongs to another agreement |
| `SVC-AGR-012` | 422 | The work order already consumed a visit |
| `SVC-TECH-001` | 409 | Technician code already exists |
| `SVC-TECH-002` | 404 | Technician not found |
| `SVC-TECH-003` | 422 | The technician still has work orders ahead and cannot be deactivated |
| `SVC-TECH-004` | 422 | The legal entity of a technician does not change |
| `SVC-TECH-005` | 422 | The technician edit would put a SCHEDULED visit in breach of the dispatch rules (errors name each work order) |
| `SVC-DSP-003` | 422 | The technician lacks a required skill |
| `SVC-DSP-004` | 422 | The technician is inactive, of another legal entity or does not cover the territory |
| `SVC-DSP-005` | 422 | The visit is outside the customer's window |
| `SVC-DSP-006` | 422 | The visit is outside the technician's shift |
| `SVC-DSP-007` | 422 | The visit overlaps another booking or leaves less than the 5 minute buffer |
| `SVC-DSP-008` | 422 | The day would exceed the technician's daily maximum |
| `SVC-WO-001` | 422 | Completing needs the customer's signature (unless the work order is `signatureExempt`); as an action it is REJECTED, not an HTTP error |
| `SVC-WO-002` | 422 | Posting needs every PART line consumed (from the action log) |
| `SVC-SYNC-001` | 200 | Message on an action batch: offline actions merged and status conflicts resolved |
| `SVC-SYNC-002` | action | REJECTED: the status move cannot follow the work order's state, or it is not worked from the device |
| `SVC-SYNC-003` | action | REJECTED: the work order is not assigned to this technician |
| `SVC-SYNC-004` | action | REJECTED: not a PART line of the work order, or the quantity would exceed the line |
| `SVC-SYNC-003` | 403 | The caller is neither the technician's worker (user id = `workerId`) nor office staff with `fieldservice.work-order.maintain`; the whole batch is refused |
| `SVC-SYNC-005` | 422 | An action id was already used for another action (another work order, technician, kind, device time or payload) |
| `SVC-SYNC-006` | action | REJECTED: the device time is more than 5 minutes before the work order was created (check the device clock) |
| `SVC-DSP-009` | 422 | Nothing scheduled to dispatch on that territory-day (or an unreadable day) |
| `SVC-DSP-010` | 404 | Dispatch run not found |
| `SVC-DSP-014` | 404 | Optimization not found |
| `SVC-DSP-015` | 422 | Optimization is not in a state that allows the action |
| `SVC-DSP-016` | 422 | Nothing to optimize: no qualified technicians or no open work orders on that territory-day |
| `SVC-DSP-017` | 503 | The dispatch service is not configured |
| `SVC-ENT-001` | — | Info in the entitlement trace: no agreement matched, fallback profile applied |
| `SVC-ENT-002` | 422 | No agreement matched and no fallback SLA profile supplied |
| `SVC-CASE-001` | 404 / 422 | Service case not found (422 when a work order references it) |
| `SVC-CASE-002` | 422 | The case belongs to another customer or legal entity |
| `SVC-CASE-003` | 422 | Case status transition not allowed by the case status machine |
| `SVC-WO-004` | 404 | Service work order not found |
| `SVC-WO-010` | 422 | Posting requires a COMPLETED work order |
| `SVC-WO-011` | 422 | Status transition not allowed by the work-order state machine (BR-45.5) |
| `SVC-WO-012` | 422 | A follow-up needs a COMPLETED or POSTED work order (BR-45.5) |
| `AUTH-0003` | 403 | Missing duty, or the caller has no UUID user id to record as author, reviewer or voter |
| `SVC-OBJ-001` | 409 | The item and serial already have a service object |
| `SVC-OBJ-002` | 404 | Service object not found |
| `SVC-OBJ-003` | 422 | The parent service object does not exist or belongs to another customer |
| `SVC-OBJ-004` | 422 | The service object was already installed |
| `SVC-OBJ-005` | 422 | The case names a service object that does not exist or belongs to another customer |
| `SVC-ACT-001` | 404 | Activity not found |
| `SVC-ACT-002` | 422 | The activity's status does not allow the move (DONE and CANCELED are final) |
| `KB-ART-001` | 404 | Knowledge article not found |
| `KB-ART-002` | 422 | The article's status does not allow the action |
| `KB-ART-003` | 422 | Another version of the article is already in draft or review |
| `KB-PUB-001` | 422 | The reviewer must differ from the author (PRD message catalog) |
| `CASE-GEN-001` | 404 | Generic case not found |
| `CASE-CAT-001` | 409 | Case category code already exists |
| `CASE-CAT-002` | 422 | SERVICE or CUSTOMER_SERVICE category used on `POST /v1/cases` |
| `CASE-CAT-003` | 422 | Referenced case category (case or parent) not found |
| `CASE-PRC-002` | 409 | Case process code already exists |
| `CASE-PRC-003` | 422 | Referenced case process not found |
| `CASE-CUS-001` | 422 | The category requires a customer party |
| `CASE-STG-001` | 422 | Required activities of the current stage are not DONE (PRD message catalog) |
| `CASE-STG-002` | 422 | Stage move not allowed (not adjacent, same stage, unknown stage, no reason for a move back, or no process) |
| `CASE-RES-001` | 422 | A resolution code is required to resolve |
| `CASE-RES-002` | 422 | The resolution code is not one of the category's codes |
| `CASE-ST-001` | 422 | Case status move not allowed, or a stage move, association or reopen in a status that does not allow it |
| `CASE-SEC-001` | 403 | The caller has no access to cases of this category (BR-45.10) |
| `CASE-MAIL-001` | — | Info returned when an inbound email is attached to a case (PRD message catalog) |
| `CASE-MAIL-002` | 422 | No case mailbox is set up for the address |
| `CASE-MAIL-003` | 422 | A mailbox cannot default to a category whose cases require a customer |
| `SVC-SWC-001` | 422 | The vendor's claim window ended (PRD message catalog) |
| `SVC-SWC-002` | 422 | A failed-part return is required before submission (PRD message catalog) |
| `SVC-SWC-003` | 422 | An accepted amount exceeds the claimed amount (PRD message catalog) |
| `SVC-SWC-004` | 422 | The claim's status does not allow the move |
| `SVC-SWC-005` | 422 | The credit note does not credit the accepted total in the claim currency |
| `SVC-SWC-006` | 422 | The vendor decision is inconsistent (ACCEPTED not in full, partial bounds, amounts per line, rejection without a reason or with an amount) |
| `SVC-SWC-007` | 404 | Supplier claim not found |
| `SVC-SWC-008` | 422 | A source work order is unknown, not POSTED, has no WARRANTY line, or differs in legal entity or currency, or the claim claims nothing |
| `SVC-SWC-009` | 409 | A source work order is already on a supplier claim |
| `SVC-SWC-010` | 422 | The credit note's vendor reference does not match the claim's |

`SVC-SLA-001` (warning) and `SVC-SLA-002` (breach) are the messages of the
`sla.warning` and `sla.breached` events, not HTTP errors. `KB-PUB-002` (publishing
retires version {v}) is a warning in the publish response. The PRD message catalog
(8.45.18) also reserves `SVC-WO-001/002`,
`SVC-DSP-*`, `SVC-TRK-001` and `SVC-SYNC-001` for the features that raise
them; this service does not reuse those numbers.

Codes are stable; new codes are additive. Error codes returned by the platform kernel
are documented there.

## Dependencies

No dependencies beyond the platform kernel module and what it already pulls in
(chi, pgx/v5, shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
