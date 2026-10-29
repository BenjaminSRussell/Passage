# NOT-IMPLEMENTED — PSG-45 (wave 45), slices 1 and 2

Slice 1 delivers: SLA profiles with business-hour calendars and holidays; service
agreements; the entitlement resolver (BR-45.1); case open with entitlement and SLA
clock start (FR-45.5); work orders with lines, created from cases with the case's
agreement, and the status machine (FR-45.6, BR-45.5); posting with per-line coverage
and agreement hour FIFO (FR-45.10, BR-45.4, BR-45.8 rounding); list and read
endpoints for every entity; the events `case.opened` and `workorder.posted`; the
greedy dispatch checker and solver in `services/dispatch-py`; the web service module
with list, create and detail screens for cases, agreements, work orders and SLA
profiles; OpenAPI, AsyncAPI, chart, Dockerfile.

Slice 2 (PSG-4502) delivers: the case status machine (`POST /cases/{id}:transition`,
If-Match) with SLA pause and resume (WAITING_CUSTOMER, WAITING_PARTS under
`pauseOnParts`), the response stamp from leaving NEW or the first DISPATCHED work
order, MET/BREACHED resolution; one SLA monitor pass (`POST /sla:scan`) that emits
`sla.warning` at 75 % and `sla.breached` past due once per clock through the outbox;
work-order completion time, `first_time_fix` and follow-up orders (`:follow-up`,
BR-45.5); `GET /kpis` with SLA attainment, mean time to respond and resolve and the
first-time-fix rate; and in the web module the case status actions, SLA alerts, the
follow-up action and a KPI dashboard. Everything below is not built yet.

Status values: **later slice of PSG-45** (planned in this wave) or **handoff** (needs
another owner, or lives outside my owned paths).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Installed base / service objects; warranty terms; serial tracking from shipments | later slice of PSG-45 | Needs the wave-28 serialized-shipment event consumer and the ServiceObject/Warranty tables (FR-45.1, FR-45.2). Posting already accepts warranty cover from the domain, but nothing sources it yet. |
| 2 | Service agreements (entitlements, SLAs, pricing); cases to service work orders; repair orders | partial | Agreements, SLA profiles, case open and work orders from cases are built. Case status transitions with SLA pause/resume are built (slice 2, as the custom method `POST /cases/{id}:transition` rather than the PRD's `/status`). The agreement lifecycle after create (activate a DRAFT, suspend, expire, cancel), repair orders, preventive visits (`service-agreement-visits`) and pricing from price lists (wave 27) are later slices. |
| 3 | Dispatch board and scheduling with OR-Tools; mobile technician app | partial | Feasibility checker and greedy solver in `services/dispatch-py` are built. CP-SAT model, dispatch-run API (`/dispatch/runs`, publish), board UI, travel matrix client (OSRM) and `service-dispatch-auto` are later slices. The PWA is a handoff (see `apps/technician/README.md`). |
| 4 | Parts consumption from truck stock; billing to customer or warranty | partial | Posting to billable, warranty and agreement coverage is built. Truck consumption (TRUCK locations, BR-45.7) and mobile action log are later slices. Invoice creation is a handoff to wave 29 (consumes `workorder.posted.v1`). |
| 5 | Service KPIs: SLA attainment, first-time fix | partial | `GET /v1/service/kpis` (window and territory) computes response and resolution attainment, mean time to respond and resolve, and the first-time-fix rate live from the case and work-order tables, and the web module has a KPI dashboard. Not built: technician utilization and travel ratio (need dispatch assignments), filters by technician, category and priority (8.45.14), and the `svc_sla` / `svc_kpi` read projections (the analytics wave consumes the events instead). |
| 6 | Generic case management (categories, processes, associations, category security) consolidating wave 9/30 cases | later slice of PSG-45 | FR-45.12, BR-45.9, BR-45.10 and the `case-consolidate` migration are not started. The current `service_case` is the SERVICE category only. |
| 7 | Knowledge base; activities on cases, projects and records | later slice of PSG-45 | FR-45.13 and FR-45.14 are not started. Project activities depend on wave 41. |
| 8 | Supplier warranty claims with vendor recovery; email-to-case intake | later slice of PSG-45 | FR-45.15 and FR-45.16 are not started. Email intake depends on the wave-35 mailbox hub (handoff). |
| Deploy | Service workspace and technician mobile app; demo service objects from shipped serials | partial | The web service module has list, create and detail screens for cases, agreements, work orders and SLA profiles. Slice 2 adds the case status actions, the follow-up action and the KPI dashboard. The workspace tiles (SLA at risk, unscheduled work orders, completion rate), dispatch board and installed-base explorer (PRD 8.45.6) wait for their features. The PWA is a handoff. Demo objects wait for item 1. |

## Functional requirements (PRD 8.45.3)

| FR | Status | Reason |
|---|---|---|
| FR-45.1 Installed base | later slice of PSG-45 | See deliverable 1. |
| FR-45.2 Warranties | later slice of PSG-45 | Domain rule exists (`WarrantyActive`, `coverFor`); no warranty tables or term sourcing. |
| FR-45.3 Service agreements | partial | Agreements and lines are stored and listed with consumed included hours; `billing_model` and `includedVisits` are recorded, but visit consumption, preventive visits, the status lifecycle after create, `price_list_id`/`discount_pct` and the subscription hand-off to wave 29 are not built. |
| FR-45.4 SLA profiles | partial | Profiles, targets, Monday-to-Friday calendars with holiday dates, 24x7 and `pauseOnParts` are stored; calendars and holidays drive the clock arithmetic. Per-weekday hours, holidays from wave-8 calendars (handoff) and the warning threshold parameter (75 %) are not built. |
| FR-45.5 Cases | partial | Open is built with entitlement and SLA clocks; channel is recorded. Status transitions, pause/resume, the monitor pass and the warning and breach events are built (slice 2). Not built: intake from portal and IoT sources (wave 44/25 events) and the case log/timeline. |
| FR-45.6 Service work orders | partial | Create (from a case, inheriting its agreement, or directly), list, read, transition and lines are built. Follow-up orders (`follow_up_of`) and the completion time are built (slice 2). Required skills, service object, address, geo, time window and assignment are not stored yet. The 8.45.5 validations "complete requires a signature" (`SVC-WO-001`) and "post requires all parts lines consumed" (`SVC-WO-002`) wait for the mobile action log and truck stock. |
| FR-45.7 Dispatch optimization | partial | Greedy and checker only (`services/dispatch-py`); no CP-SAT, no API, no re-optimization. |
| FR-45.8 Technician PWA | handoff | `apps/technician/README.md`. |
| FR-45.9 Truck stock | later slice of PSG-45 | Needs wave-16 TRUCK locations (handoff) and BR-45.7 consumption. |
| FR-45.10 Billing | partial | Work order posting is built and emits `workorder.posted.v1`. Invoice creation is a wave-29 handoff. |
| FR-45.11 Service KPIs | partial | SLA attainment, first-time fix and mean time to respond and resolve are built; utilization and travel ratio are not. See deliverable 5. |
| FR-45.12 Generic case management | later slice of PSG-45 | See deliverable 6. |
| FR-45.13 Knowledge base | later slice of PSG-45 | See deliverable 7. |
| FR-45.14 Activities | later slice of PSG-45 | See deliverable 7. |
| FR-45.15 Supplier warranty claims | later slice of PSG-45 | See deliverable 8. |
| FR-45.16 Email-to-case intake | later slice of PSG-45 | See deliverable 8. |

## Business rules (PRD 8.45.7)

| BR | Status | Reason |
|---|---|---|
| BR-45.1 Entitlement resolution | partial | Resolver in `internal/domain/entitlement.go`, unit-tested; candidates are restricted to the case's legal entity. Not built: the parent-party match (needs the party hierarchy), the object-category match from a service object's category (no installed base yet), and the legal entity's default SLA profile parameter; the caller passes `fallbackSlaProfileId` instead. |
| BR-45.2 SLA clock rules | partial | Business-minute calendar with holidays and due arithmetic are built and property-tested. Slice 2 builds the response stamp (leaving NEW or the first DISPATCHED work order), the pauses (WAITING_CUSTOMER; WAITING_PARTS with `pauseOnParts`) with the due recomputed from the paused business minutes, the warning at 75 % and the breach, each emitted once, and BREACHED staying a miss when resolved late. Not built: the tenant warning-threshold parameter (fixed at the 75 % default) and holiday calendars from wave 8 (handoff). The response clock does not pause on WAITING_CUSTOMER, because a case can only wait after it was answered. |
| BR-45.3 Dispatch constraints | partial | Hard constraints are in the checker; objective and CP-SAT are later. Certification expiry (BR-42.8) waits for wave 42. |
| BR-45.4 Coverage per line | partial | Warranty, agreement and billable per line; FIFO hours. Warranty sourcing is later. |
| BR-45.5 Work order state machine | built | Transitions tested; completion sets `first_time_fix` and `completed_at`, and a follow-up of a COMPLETED or POSTED order links by `follow_up_of` and clears the parent's `first_time_fix` (slice 2). |
| BR-45.6 Offline merge | later slice of PSG-45 | Needs the action log endpoint. |
| BR-45.7 Truck stock | later slice of PSG-45 | See FR-45.9. |
| BR-45.8 Pricing | partial | Half-even rounding per line is built. Price lists and wave-27 pricing are a handoff. |
| BR-45.9 Case process stages | later slice of PSG-45 | Generic cases not started. |
| BR-45.10 Case category security | later slice of PSG-45 | Generic cases not started. |
| BR-45.11 Knowledge lifecycle | later slice of PSG-45 | Knowledge base not started. |
| BR-45.12 Supplier claims | later slice of PSG-45 | Not started. |
| BR-45.13 Email threading | later slice of PSG-45 | Not started. |

## Acceptance criteria (PRD 8.45.8 and ticket)

| AC | Status | Reason |
|---|---|---|
| AC-45.1 Entitlement fixtures (50) | partial | Resolver unit tests exist. The 50-case fixture set under `testdata/` is not built. |
| AC-45.2 Pause 120 min to 2026-10-27T12:00-05:00 | partial | The pause and resume are built and tested against Postgres (`TestStore_CaseTransitionPausesAndResumes`). Open question for the PRD owner: under the BR-45.2 example calendar (Mon–Fri 08:00–18:00) 480 + 120 business minutes from Fri 2026-10-23 16:00 end Mon 2026-10-26 16:00, not Tue 12:00 (Tue 12:00 needs 960 minutes). `sla_test.go` and the store test assert Mon 16:00; the AC text should be corrected or the example calendar stated. |
| AC-45.3 Warning at 75 % and single breach event | built | Domain tests (`TestDueAlerts*`) and `TestStore_SLAMonitorWarnsOnceThenBreachesOnce` (one warning, then one breach, nothing on later passes). The 60 s schedule is not built (see scheduled jobs). |
| AC-45.4 1,000 territory-days, p95 < 30 s | partial | Hypothesis property (300 examples) proves greedy feasibility. CP-SAT and the timing run are not built. |
| AC-45.5 Pinned assignment unchanged | partial (greedy) | Verified by property test in the greedy solver. The CP-SAT re-optimization path is not built. |
| AC-45.6 Warranty and agreement billing split | partial | Agreement 1 h of 3 h labor split is unit-tested. Warranty part is not sourced (see FR-45.2). |
| AC-45.7 Offline replay equals device state | later slice of PSG-45 | Needs BR-45.6. |
| AC-45.8 10 active service objects from shipment | later slice of PSG-45 | Needs FR-45.1. |
| AC-45.9 Phase-8 exit walkthrough | handoff | Spans waves 41, 42, 43, 44 and this wave. Owned by the phase-exit demo. |
| AC-45.10 HR stage gate `CASE-STG-001` | later slice of PSG-45 | Needs FR-45.12. |
| AC-45.11 HR case category 403 | later slice of PSG-45 | Needs FR-45.12 and BR-45.10. |
| AC-45.12 KB ranking and `KB-PUB-001` | later slice of PSG-45 | Needs FR-45.13. |
| AC-45.13 Supplier claim settlement | later slice of PSG-45 | Needs FR-45.15. |
| AC-45.14 Email threading | later slice of PSG-45 | Needs FR-45.16. |
| AC-45.15 My activities | later slice of PSG-45 | Needs FR-45.14. |
| Ticket AC1 entitlement fixtures | partial | See AC-45.1. |
| Ticket AC2 dispatch feasibility (property) | partial | Greedy only (see AC-45.4). |
| Ticket AC3 billable vs warranty posting | partial | See AC-45.6. |
| Ticket AC4 phase-8 exit | handoff | See AC-45.9. |
| Ticket AC5 every deliverable has code or a row here | met | This file. |
| Ticket AC6 vet/test, web tests, specs match, chart lints | met | Service `go vet`/`go test` pass; web module vitest and tsc pass; `api/openapi.yaml` and `events/asyncapi.yaml` match the handlers and events; chart lints (see README). |

## Fixtures and test plan (PRD 8.45.9)

| Item | Status | Reason |
|---|---|---|
| `svc-installed-base` (2,000 objects) | later slice of PSG-45 | Needs FR-45.1. |
| `svc-entitlement-50` | later slice of PSG-45 | Not yet in `testdata/`. |
| `svc-dispatch-gen` | partial | Hypothesis generator in `services/dispatch-py/tests/test_properties.py`; seeded OSRM matrix not built. |
| `svc-mobile-replay` | later slice of PSG-45 | Needs BR-45.6. |
| `svc-billing-30` | later slice of PSG-45 | Not built. |
| Reconciliation: Σ posted billable = wave-29 invoices; Σ warranty = GL; truck issues = part actions | later slice of PSG-45 (and handoff for the wave-29 and GL sides) | Needs invoice creation, truck stock and warranty sourcing. |
| PWA Playwright offline E2E | handoff | `apps/technician` not built. |
| Breach monitor performance (100k cases < 60 s) | later slice of PSG-45 | The pass is built on the partial index `service_case_sla_running` and batches with `limit`; the 100k-case timing run is not. |

## Data model and operations (PRD 8.45.4, 8.45.10, 8.45.13, 8.45.19)

| Item | Status | Reason |
|---|---|---|
| ServiceObject, Warranty, Technician, DispatchRun, ServiceWorkOrder assignment fields, geo (PostGIS point index) | later slice of PSG-45 | Not in migration 0001. |
| `Case` category fields, stages, CaseAssociation, case log | later slice of PSG-45 | FR-45.12. |
| ETag on reads and `If-Match` on PUT/PATCH/custom methods | built for work orders and cases | Work-order and case create, read and custom methods set `ETag: "<version>"`, and the custom methods require `If-Match`. Agreements and profiles have no update endpoints yet, so they carry no version. |
| Number sequences from wave-5 definitions | partial | Fixed prefixes `SA`, `CS`, `SWO` allocated by the store; per-legal-entity definitions are not read yet. |
| Parameters 8.45.13 (default SLA profile, warning threshold, dispatch time, solver limits, buffer, overtime, negative truck, reopen window, parent-party match) | partial | The business calendar and holidays per SLA profile are built. The legal entity's default SLA profile is not stored; the case request carries the fallback. `pauseOnParts` is applied by the case transitions (slice 2). The SLA warning threshold is fixed at the 75 % default (no tenant parameter yet). The rest wait for the features above. |
| `service-sla-monitor` schedule | handoff (platform scheduler) and later slice | One pass per tenant is built (`POST /v1/service/sla:scan`); running it every 60 s for every tenant needs the platform scheduler (wave-23 patterns) or a chart CronJob with a service principal per tenant, and the "monitor lag > 2 min" alert waits for that. |
| Event `dev.passage.service.workorder.completed.v1` | later slice of PSG-45 | Its trigger is the mobile complete action (technicianId, lines, durationMinutes; 8.45.5), which needs the action log (BR-45.6). The `:transition` to COMPLETED sets the completion time and first-time fix but emits no event yet. |
| Scheduled jobs (`service-dispatch-auto`, `service-truck-replenish`, `service-ib-from-shipments`, `service-agreement-visits`, `case-email-ingest`, `case-consolidate`, `supplier-claim-deadline`, `kb-reindex`) | later slice of PSG-45 | Each depends on a feature above. |
| Feature flag `service.allow-negative-truck` | later slice of PSG-45 | Needs BR-45.7. |
| Chart `passage-service` (TS API, breach monitor, mobile BFF) and `passage-dispatch` (Python worker, OSRM sidecar) | partial | Chart `services/fieldservice/charts/fieldservice` is built. The dispatch worker chart and OSRM sidecar are later. |
| Privacy: technician location retention (90 days) and works-council flag; HR `hr_pii` protection | later slice of PSG-45 | Needs live tracking and HR categories. |
| Migration from AX/SAP (PRD 8.45.11) | later slice of PSG-45 | Needs the entities above. |

## Handoffs (outside owned paths)

| Owner | Request | Why |
|---|---|---|
| Policies (`policies/`) | Define OPA duty mappings for `SvcAgreementMaintain`, `SvcCaseCreate`, `SvcCaseMaintain`, `SvcInquire`, `SvcWoMaintain`, `SvcBillingPost`, `SvcDispatch`, `SvcTechnician` | Operations declare these duties in `x-passage-duty`; enforcement is via OPA. |
| Wave 28 (serialized shipments) | Publish a shipment-confirmed event that the service can consume to create service objects | FR-45.1 |
| Wave 29 (invoicing) | Consume `dev.passage.service.workorder.posted.v1` to create invoices (billable lines only, source SWO) | FR-45.10 and the reconciliation |
| Wave 16 (locations) | TRUCK location type per technician; consumption issue API | FR-45.9, BR-45.7 |
| Wave 27 (pricing) | Price list lookup for labor and parts (BR-45.8) | BR-45.8 |
| Wave 35 (notification hub, mailboxes) | Mailbox ingestion and outbound acknowledgment | FR-45.16 |
| Wave 42 (workers and skills) | Technician entity links to workers; skills and certification expiry (BR-42.8) | BR-45.3 |
| Wave 5 (sequences) | Per-legal-entity number sequences `SA`, `CS`, `SWO`, `SWC`, `KB` | Parameters 8.45.13 |
| Web BFF (`services/web-bff-ts`) | Rewrite `/api/v1/service/...` to `/v1/service/...` and set `X-Passage-CSRF` | Web module calls |
| Deploy (`deploy/`) | Helm umbrella entry and Argo CD app for `fieldservice` | Deployment |
| Platform scheduler | Run `POST /v1/service/sla:scan` every 60 s per tenant with a service principal holding `SvcCaseMaintain` (`service-sla-monitor`) | BR-45.2, 8.45.16 |
| Notifications (wave 35) | Consume `dev.passage.service.sla.warning.v1` and `sla.breached.v1` to notify (messages `SVC-SLA-001`, `SVC-SLA-002`) | AC-45.3 "warning notification fires" |
| Platform kernel | Shared ETag helper (the service sets the header itself today) | Convention |
| Wave 8 (calendars) | Public-holiday calendars per site that SLA profiles can reference instead of their own date list | BR-45.2, 8.45.20 |
| Party hierarchy owner | Parent party lookup for the parent-party entitlement match | BR-45.1 |
