# NOT-IMPLEMENTED — PSG-45 (wave 45), slices 1 to 6

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
follow-up action and a KPI dashboard.

Slice 3 (PSG-4503) delivers: the installed base (`service_object`) registered by hand
or from a confirmed serialized shipment (`:receive-shipment`, one ACTIVE object per
serial, idempotent per item and serial) with parent/child structure, installation and
a warranty term per object (parts, labor and travel flags, months from SHIP, INSTALL
or REGISTRATION); a case on a service object of its customer bills the covered lines
of its work orders to warranty when posted (FR-45.2, BR-45.4, AC-45.6); activities
(TASK, APPOINTMENT, CALL_NOTE, EMAIL_NOTE) on any record with "My activities",
complete, cancel, reschedule and an iCalendar export (FR-45.14, AC-45.15); the
knowledge base with versioned per-language articles, the DRAFT → IN_REVIEW →
PUBLISHED → RETIRED lifecycle, reviewer ≠ author (`KB-PUB-001`), publishing retiring
the previous version (`KB-PUB-002`) with `dev.passage.knowledge.article.published.v1`
through the outbox, helpfulness votes and ranked suggestions (BR-45.11, AC-45.12);
and in the web module the installed-base explorer, "My activities", the knowledge
base screens, and activities and suggested articles on the case detail.

Slice 4 (PSG-4504) delivers generic case management (FR-45.12): case processes with
ordered stages and the activities each stage requires; the case category tree (type,
parent, process, SLA profile, default priority, reopen window, resolution codes and an
access duty); cases in any non-service category (`POST /v1/cases`, starting at the
first stage, the category's SLA profile setting the dues, COLLECTIONS requiring a
customer) with associations to any registry record and a case log; the stage gate
(`CASE-STG-001` until the stage's required activities are DONE on the case, back one
stage with a reason) emitting `dev.passage.case.stage.changed.v1` through the outbox;
resolve with a category resolution code, close, cancel and reopen (in place within
the window, else a new case linked by `reopenedFrom`) (BR-45.9, AC-45.10); category
security by access duty and ownership on open, read, list and every change (BR-45.10,
AC-45.11 403); and in the web module the case list, open case, case detail and case
setup screens.

Slice 5 (PSG-4505) delivers supplier warranty claims (FR-45.15, BR-45.12): claims
created from the WARRANTY lines of POSTED service work orders (one line per order with
the warranty part cost, the warranty labor hours, the service object's item and serial)
or with given lines for EAM work orders, each line claiming part + hours × the
vendor's labor rate; one claim per work order; submission inside the vendor's claim
window (`SVC-SWC-001`) with the failed-part return when the vendor requires it
(`SVC-SWC-002`); the vendor decision ACCEPTED, PARTIALLY_ACCEPTED or REJECTED with
accepted amounts per line (`SVC-SWC-003`); settlement by matching the credit note's
vendor reference and amount, which writes `dev.passage.service.supplierclaim.settled.v1`
through the outbox; close (AC-45.13). And email-to-case intake (FR-45.16, BR-45.13):
case mailboxes with a default category; inbound messages threaded by `[CS-n]` token
or In-Reply-To onto an open case and logged there (`CASE-MAIL-001`), a new case linked
by `reopenedFrom` when that case is CLOSED, otherwise a new case in the mailbox's
default category; the acknowledgment flag once per new case, suppressed for
auto-replies; Message-ID redelivery answered from the stored message; attachment
metadata (AC-45.14). In the web module: the supplier claim list, create and detail
with every action, and the case mailbox screen. Everything below is not built yet.

Slice 6 (PSG-4506) delivers the service agreement lifecycle and included-visit
consumption (FR-45.3): `POST /agreements/{id}:transition` (If-Match, optional
Idempotency-Key) activates a DRAFT, suspends and resumes (SUSPENDED entitles nothing),
and cancels, with a required reason for suspend and cancel, activation refused once
`validTo` is behind today (`SVC-AGR-006`) and every move written to a status history
and to `dev.passage.service.agreement.changed.v1` through the outbox; the expiry pass
(`POST /agreements:expire`, ACTIVE and SUSPENDED past `validTo` become EXPIRED, once,
`SKIP LOCKED`); visits (`POST /agreements/{id}/visits`) that consume a line's
`includedVisits` (ACTIVE agreement, inside the term, a line with a visit left, a work
order of the agreement consuming at most one; the agreement row is locked) and write
`dev.passage.service.agreementvisit.recorded.v1`; visit and history lists; the agreement
version and ETag on read and create; and in the web module the status actions with a
reason form, the visits table with per-line usage, the record-visit form, the status
history and an "Expire ended agreements" action. The AsyncAPI doc gate is at zero FAIL
lines (summaries on `CaseOpened` and `SLAWarning`); the OpenAPI gate already was.
The slice-6 review stores what is left on the line with each visit (a replay and the
visit list answer like the record call), adds the customer and billing model to both
agreement events for wave 29, trims reasons and notes, makes an agreement whose term
ended before a work order's completion day cover nothing even while it is still
ACTIVE (so a late expiry pass changes no billing), and reloads the detail page after
a version conflict that follows an earlier move.

Status values: **later slice of PSG-45** (planned in this wave) or **handoff** (needs
another owner, or lives outside my owned paths).

## Ticket deliverables

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Installed base / service objects; warranty terms; serial tracking from shipments | partial | Built (slice 3): service objects with customer, site address, parent/child, ship and install dates, status and a warranty term each; `POST /service-objects:receive-shipment` creates them from a confirmed serialized shipment; posting sources warranty cover from the case's service object. Not built: the wave-28 shipment-confirmed event consumer (handoff; the endpoint stands in for it, and the per-legal-entity "create service objects" parameter waits for it), status changes after create (INACTIVE, RETURNED), warranty exclusions and warranty templates (the term lives on the object), the item-group default start trigger, and the shipment line id (the shipment id is stored). |
| 2 | Service agreements (entitlements, SLAs, pricing); cases to service work orders; repair orders | partial | Agreements, SLA profiles, case open and work orders from cases are built. Case status transitions with SLA pause/resume are built (slice 2, as the custom method `POST /cases/{id}:transition` rather than the PRD's `/status`). The agreement lifecycle (activate a DRAFT, suspend, resume, cancel, expire) and included-visit consumption are built (slice 6). Repair orders, the preventive-visit generator (`service-agreement-visits`) and pricing from price lists (wave 27) are later slices. |
| 3 | Dispatch board and scheduling with OR-Tools; mobile technician app | partial | Feasibility checker and greedy solver in `services/dispatch-py` are built. CP-SAT model, dispatch-run API (`/dispatch/runs`, publish), board UI, travel matrix client (OSRM) and `service-dispatch-auto` are later slices. The PWA is a handoff (see `apps/technician/README.md`). |
| 4 | Parts consumption from truck stock; billing to customer or warranty | partial | Posting to billable, warranty and agreement coverage is built. Truck consumption (TRUCK locations, BR-45.7) and mobile action log are later slices. Invoice creation is a handoff to wave 29 (consumes `workorder.posted.v1`). |
| 5 | Service KPIs: SLA attainment, first-time fix | partial | `GET /v1/service/kpis` (window and territory) computes response and resolution attainment, mean time to respond and resolve, and the first-time-fix rate live from the case and work-order tables, and the web module has a KPI dashboard. Not built: technician utilization and travel ratio (need dispatch assignments), filters by technician, category and priority (8.45.14), and the `svc_sla` / `svc_kpi` read projections (the analytics wave consumes the events instead). |
| 6 | Generic case management (categories, processes, associations, category security) consolidating wave 9/30 cases | partial | Built (slice 4): see FR-45.12, BR-45.9 and BR-45.10 below. Not built: the `case-consolidate` migration of the wave-30 disputes and wave-9 product change requests into COLLECTIONS and PRODUCT_CHANGE cases (handoff: those waves expose no case export yet; the job is a later slice once they do), and categories on service cases (SERVICE and CUSTOMER_SERVICE cases stay in `service_case`, see FR-45.12). |
| 7 | Knowledge base; activities on cases, projects and records | partial | Built (slice 3): see FR-45.13 and FR-45.14 below. Activities accept a project (wave 41) as a record type, but no record id is checked against its owning service. |
| 8 | Supplier warranty claims with vendor recovery; email-to-case intake | partial | Built (slice 5): see FR-45.15, FR-45.16, BR-45.12 and BR-45.13 below. Not built: the consumer of `eam.warranty.claimable.v1` (EAM_WO claims are created by hand with their lines until wave 44 publishes it), the claim PDF/email/EDI to the vendor (wave-35 hub), the vendor return and credit note documents themselves (waves 30/17 and 18: the claim stores their references), the GL warranty recovery posting (consumer of the settled event), and the mailbox poll or push, outbound replies and acknowledgment sending (wave-35 hub). |
| Deploy | Service workspace and technician mobile app; demo service objects from shipped serials | partial | The web service module has list, create and detail screens for cases, agreements, work orders and SLA profiles. Slice 2 adds the case status actions, the follow-up action and the KPI dashboard. Slice 3 adds the installed-base explorer, "My activities" and the knowledge base. Slice 4 adds the case list across categories, open case, the case detail (stages, required activities, resolve, reopen, associations, log, activities) and case setup (categories and processes). Slice 5 adds supplier warranty claims (list, create from posted work orders or EAM lines, detail with submit, decision, credit-note match and close) and the case mailbox (mailboxes, set up a mailbox, inbound email with the case each landed on). Not built in the web module for slice 5: "attach to case" and "create case" on unmatched email (every message lands on a case under BR-45.13, so nothing stays unmatched; moving a message to another case is not built), a match suggestion, and creating a claim from the work-order detail (the claim form takes the work order ids). Not built in the web module for slice 4: a tree view of the category tree (setup lists categories with their parent), the case list's SLA-state filter and "My cases" shortcut (the API takes `ownerId`), and links from "My activities" to a generic case (an activity on a case links to the service case screen, which cannot show a generic case). The workspace tiles (SLA at risk, unscheduled work orders, completion rate) and the dispatch board (PRD 8.45.6) wait for their features. The PWA is a handoff. Demo service objects (a seeded shipment) are not built. |

## Functional requirements (PRD 8.45.3)

| FR | Status | Reason |
|---|---|---|
| FR-45.1 Installed base | partial | See deliverable 1. |
| FR-45.2 Warranties | partial | Parts, labor and travel flags and the duration from SHIP, INSTALL (starts when the installation is recorded) or REGISTRATION (the day the object is registered) are stored per object, and coverage is evaluated per work-order line on the completion day. The term ends on the same day of the month, clamped to the end of a short month (31 Aug + 6 months = 28 Feb, `TestWarrantyEnd_ClampsToTheEndOfAShortMonth`). Exclusions and warranty templates are not built. |
| FR-45.3 Service agreements | partial | Agreements and lines are stored and listed with consumed included hours. Slice 6 builds the status lifecycle (activate, suspend, resume, cancel, and EXPIRED through the expiry pass), the status history and the included-visit consumption per line (`billing_model` PER_VISIT agreements are billed by wave 29 from `agreementvisit.recorded.v1`). Not built: scheduled preventive visits (`service-agreement-visits`), visit cancellation or correction (visits are append-only), overage visits beyond `includedVisits`, `price_list_id`/`discount_pct`, the subscription hand-off to wave 29 (it consumes the events), and the daily schedule for the expiry pass (a user with `SvcAgreementMaintain` runs it from the agreement list). A missing schedule only delays the EXPIRED status and its event: case entitlement and visits check the term, and posting ignores an agreement whose term does not hold the completion day. Posting against an agreement that is no longer ACTIVE bills the lines rather than refusing the post. The expiry pass, the activation check and the posting term check use the UTC day; the legal entity's time zone is not read. |
| FR-45.4 SLA profiles | partial | Profiles, targets, Monday-to-Friday calendars with holiday dates, 24x7 and `pauseOnParts` are stored; calendars and holidays drive the clock arithmetic. Per-weekday hours, holidays from wave-8 calendars (handoff) and the warning threshold parameter (75 %) are not built. |
| FR-45.5 Cases | partial | Open is built with entitlement and SLA clocks; channel is recorded. Status transitions, pause/resume, the monitor pass and the warning and breach events are built (slice 2). Not built: intake from portal and IoT sources (wave 44/25 events) and the case log/timeline. |
| FR-45.6 Service work orders | partial | Create (from a case, inheriting its agreement, or directly), list, read, transition and lines are built. Follow-up orders (`follow_up_of`) and the completion time are built (slice 2). Required skills, service object, address, geo, time window and assignment are not stored yet. The 8.45.5 validations "complete requires a signature" (`SVC-WO-001`) and "post requires all parts lines consumed" (`SVC-WO-002`) wait for the mobile action log and truck stock. |
| FR-45.7 Dispatch optimization | partial | Greedy and checker only (`services/dispatch-py`); no CP-SAT, no API, no re-optimization. |
| FR-45.8 Technician PWA | handoff | `apps/technician/README.md`. |
| FR-45.9 Truck stock | later slice of PSG-45 | Needs wave-16 TRUCK locations (handoff) and BR-45.7 consumption. |
| FR-45.10 Billing | partial | Work order posting is built and emits `workorder.posted.v1`. Invoice creation is a wave-29 handoff. |
| FR-45.11 Service KPIs | partial | SLA attainment, first-time fix and mean time to respond and resolve are built; utilization and travel ratio are not. See deliverable 5. |
| FR-45.12 Generic case management | partial | Built: processes (1 to 20 ordered stages with required activities), the category tree with the nine category types, process, SLA profile, default priority, reopen window, resolution codes and access duty; cases in the non-service categories in `service_generic_case` with the customer optional except for COLLECTIONS, associations (SUBJECT or RELATED) to the registry record types of `domain.AssociableTypes`, the case log (created, stage, status, association, reopen), resolution codes, and response and resolution dues from the category's SLA profile at open; list by category, status, stage, owner and associated record. Not built: SERVICE and CUSTOMER_SERVICE categories on service cases (`service_case` has no category, stage or resolution code yet, so `POST /v1/cases` refuses those categories with `CASE-CAT-002` and the service case screens stay as they are; generalizing `service_case` instead of a second table is the open design choice for the PRD owner); inheritance of process, SLA profile or access from a parent category (each node carries its own); category and process updates (create and list only); SLA pause, monitor and breach events on generic-case dues (computed once at open); case owner reassignment and the "bulk assign" action of the case list (the owner is the user who opened the case); free-text notes in the case log; a check that an associated record exists in its owning service (wave-3 registry, handoff); the `case_stats` report (8.45.14). |
| FR-45.13 Knowledge base | partial | Built: articles (title, symptom, cause, solution, applicability to items, item groups, incident types, asset types and categories, language), versions with the lifecycle and a reject back to DRAFT, full-text search over the PUBLISHED versions with the item, incident type and asset type ranking, helpfulness votes. Not built: attachments; structured search by item group or category (stored, not filtered); the suggestions returned in the case-create response (the case detail calls the search with the case description and its object's item instead); linking articles to cases with an outcome (`CaseArticleLink`); view counts and the `kb_stats` usage report; a translation of an article into another language under the same number (a new version keeps its language; a new language is a new article); the English fallback. |
| FR-45.14 Activities | partial | Built: the four kinds with owner, due, start, end, status, reminder minutes and associations to any registry record type; "My activities" by owner and status, the activities of one record, complete (DONE with a timestamp), cancel, reschedule and the iCalendar export with reminders as VALARM. Not built: reminder notifications (wave-35 hub, handoff), checking that an associated record exists in its owning service, a calendar view in the web module (the export opens in any calendar), and the "case activities" report. |
| FR-45.15 Supplier warranty claims | partial | Built: `SupplierWarrantyClaim` with the SWC number, vendor, source SERVICE_WO or EAM_WO, source ids, lines (item, serial, failure code, part amount, labor hours, labor rate, claimed and accepted amounts), the statuses DRAFT to CLOSED, submission date, vendor reference, return order and credit note references and the recovered amount; claims from WARRANTY-covered service work order lines; the vendor decision; settlement by credit note match. Not built: claims from `eam.warranty.claimable.v1` events (handoff, wave 44; EAM_WO claims take their lines from the caller), the submission document (PDF, email or EDI, wave 35), the vendor return itself (waves 30/17; the claim records the return order id), reading the credit note from wave 18 (the caller passes its id, reference and amount), the vendor warranty agreement as the source of the labor rate, the claim window and `return_required` (the claim carries them; vendor-level parameters wait for a vendor warranty agreement entity), and one failure code per line for SERVICE_WO claims (the claim-level failure code is copied to each line). |
| FR-45.16 Email-to-case intake | partial | Built: case mailboxes per address with a legal entity and default category; `POST /v1/inbound-messages` (the integration hub's hand-over) threading by subject token or In-Reply-To onto generic cases, storing the message with its body and attachment metadata, the sender's customer when the hub matched it (`customerPartyId`), and the acknowledgment decision; the inbound list. Not built: the wave-35 mailbox poll or push (`case-email-ingest`, handoff), matching the sender to a party contact by email inside this service (the party contact directory is not here; the hub passes the party), sending the acknowledgment and logging outbound replies (wave 35; outbound Message-IDs are therefore not in the reply chain yet), attachment storage (the hub keeps the bytes and passes object references), threading onto SERVICE and CUSTOMER_SERVICE cases (`service_case` has no log; a token naming a service case does not match and the mail opens a generic case), and quarantining parse failures. |

## Business rules (PRD 8.45.7)

| BR | Status | Reason |
|---|---|---|
| BR-45.1 Entitlement resolution | partial | Resolver in `internal/domain/entitlement.go`, unit-tested; candidates are restricted to the case's legal entity. Not built: the parent-party match (needs the party hierarchy), the object-category match from a service object's category (no installed base yet), and the legal entity's default SLA profile parameter; the caller passes `fallbackSlaProfileId` instead. |
| BR-45.2 SLA clock rules | partial | Business-minute calendar with holidays and due arithmetic are built and property-tested. Slice 2 builds the response stamp (leaving NEW or the first DISPATCHED work order), the pauses (WAITING_CUSTOMER; WAITING_PARTS with `pauseOnParts`) with the due recomputed from the paused business minutes, the warning at 75 % and the breach, each emitted once, and BREACHED staying a miss when resolved late. Not built: the tenant warning-threshold parameter (fixed at the 75 % default) and holiday calendars from wave 8 (handoff). The response clock does not pause on WAITING_CUSTOMER, because a case can only wait after it was answered. |
| BR-45.3 Dispatch constraints | partial | Hard constraints are in the checker; objective and CP-SAT are later. Certification expiry (BR-42.8) waits for wave 42. |
| BR-45.4 Coverage per line | built | Warranty (from the case's service object, active on the completion day), then agreement, then billable per line; FIFO hours (`TestStore_WarrantyFromServiceObjectCoversPart`). |
| BR-45.5 Work order state machine | built | Transitions tested; completion sets `first_time_fix` and `completed_at`, and a follow-up of a COMPLETED or POSTED order links by `follow_up_of` and clears the parent's `first_time_fix` (slice 2). |
| BR-45.6 Offline merge | later slice of PSG-45 | Needs the action log endpoint. |
| BR-45.7 Truck stock | later slice of PSG-45 | See FR-45.9. |
| BR-45.8 Pricing | partial | Half-even rounding per line is built. Price lists and wave-27 pricing are a handoff. |
| BR-45.9 Case process stages | partial | Built: start at the first stage; the next stage only when every required activity of the current stage is DONE on the case (`CASE-STG-001`), back one stage with a reason, other moves refused (`CASE-STG-002`); RESOLVED requires a resolution code (from the category's list when it has one); CANCELED final; reopen of a RESOLVED or CLOSED case in place within the category's window (days since resolution, default 14, 0 disables it), else a new case linked by `reopenedFrom` (`TestStore_GenericCaseStageGate`, `TestStore_GenericCaseResolveCloseAndReopen`). Not built: entry and exit criteria as CEL expressions over the case, associations and activities (only the required activities gate a move; needs a CEL evaluator in the platform kernel, handoff); required activity templates as their own entity (a template is matched by the activity subject). The PRD's "CLOSED is final ... unless the category allows reopen within N days" is read as: a CLOSED or RESOLVED case reopens itself inside the window and is otherwise reopened as a new linked case. |
| BR-45.10 Case category security | partial | Built: a category's access duty (standing in for its OPA policy reference) plus the case owner decide open, read, list and every change; others get `CASE-SEC-001` (403) and the list leaves the case out; a category without one is open to every CaseMaintain holder (`TestStore_GenericCaseCategorySecurity`, `TestAPI_GenericCaseFlow`). Not built: members of the category's owning team (no team entity), OPA policy evaluation per category type (the duty stands in; policies handoff), the HR subject's manager chain as of today (BR-42.1, wave 42 handoff), the collector of a COLLECTIONS case's customer (wave 30 handoff), associations shown as "restricted record" when the caller cannot read the record (needs the registry's read check), and the `hr_pii` protected column set for HR case free text. |
| BR-45.11 Knowledge lifecycle | partial | Built: one PUBLISHED version per article and language (partial unique index), publishing retires the previous one (`KB-PUB-002` warning), reviewer ≠ author (`KB-PUB-001`, also a table check), the ranking ts_rank × 0.6 (normalized to the best candidate; any word of the description matches) + item 0.25 + incident type 0.10 + asset type 0.05, top 5 by default, and the helpful share from 10 votes. Not built: the tenant parameters "Knowledge article review required" (always required) and "Suggestion count and weights" (fixed at the defaults; the count can be passed as `limit`). |
| BR-45.12 Supplier claims | partial | Built: claimable = part cost + labor hours × vendor rate (0 when labor is not covered), rounded half-even to the currency; the window (default 30 days after completion, `SVC-SWC-001`); accepted ≤ claimed per line (`SVC-SWC-003`); settlement matches the credit note by vendor reference and requires it to credit the accepted total; rejected claims close with a reason and no event; the return order before SUBMITTED when `returnRequired` (`SVC-SWC-002`) (`TestStore_SupplierClaimSettlesAC45_13`, `TestStore_SupplierClaimSourcesWindowAndRejection`). Part cost is the posted warranty cost (quantity × unit cost on the line), not a wave-16 issue cost (handoff with truck stock). The credit to the warranty expense account is the GL consumer's (handoff). |
| BR-45.13 Email threading | built | `[CS-X]` token (case-insensitive, padding optional) on a case that is not CLOSED, else In-Reply-To matching a message already on the case; a new case in the mailbox's default category otherwise; messages to a CLOSED (or CANCELED) case open a new case linked by `reopened_from`; the acknowledgment once per new case, suppressed for `Auto-Submitted` other than `no` and for `X-Autoreply` (`TestThread_BR45_13`, `TestStore_EmailIntakeThreadsAC45_14`). The sender match by contact email is the hub's (see FR-45.16). |

## Acceptance criteria (PRD 8.45.8 and ticket)

| AC | Status | Reason |
|---|---|---|
| AC-45.1 Entitlement fixtures (50) | partial | `svc-entitlement-50` (`internal/domain/testdata/`, answers from the independent `reference_entitlement.py`): 50/50 resolve to the reference agreement, specificity, SLA profile and four clock instants in `TestResolve_SvcEntitlement50`, and the 46 that case open can express do so through `OpenCase` as passage_app in `TestStore_SvcEntitlement50OpensCases` (overlapping agreements, every status, validity edges, other customers and legal entities, fallback and refusals, holidays, US and EU DST ends, 24x7, a midnight close, Tokyo). Not covered: parent-party agreements (BR-45.1 parent match needs the party hierarchy, handoff), and category lines through case open (service objects carry no category; the four category and id-tie cases run in the domain test only). Validity is checked on the UTC day of `openedAt` (E17, E18); the PRD does not say which time zone a validity date is in. |
| AC-45.2 Pause 120 min to 2026-10-27T12:00-05:00 | partial | The pause and resume are built and tested against Postgres (`TestStore_CaseTransitionPausesAndResumes`). Open question for the PRD owner: under the BR-45.2 example calendar (Mon–Fri 08:00–18:00) 480 + 120 business minutes from Fri 2026-10-23 16:00 end Mon 2026-10-26 16:00, not Tue 12:00 (Tue 12:00 needs 960 minutes). `sla_test.go` and the store test assert Mon 16:00; the AC text should be corrected or the example calendar stated. |
| AC-45.3 Warning at 75 % and single breach event | built | Domain tests (`TestDueAlerts*`) and `TestStore_SLAMonitorWarnsOnceThenBreachesOnce` (one warning, then one breach, nothing on later passes). The 60 s schedule is not built (see scheduled jobs). |
| AC-45.4 1,000 territory-days, p95 < 30 s | partial | Hypothesis property (300 examples) proves greedy feasibility. CP-SAT and the timing run are not built. |
| AC-45.5 Pinned assignment unchanged | partial (greedy) | Verified by property test in the greedy solver. The CP-SAT re-optimization path is not built. |
| AC-45.6 Warranty and agreement billing split | built | `TestStore_WarrantyFromServiceObjectCoversPart`: the part is zero-priced under the service object's warranty with its cost recorded, 2 of 3 labor hours are billable and 1 included hour is consumed. The invoice itself is wave 29 (handoff). |
| AC-45.7 Offline replay equals device state | later slice of PSG-45 | Needs BR-45.6. |
| AC-45.8 10 active service objects from shipment | partial | `TestStore_ShipmentCreatesObjectsAndInstallStartsWarranty`: 10 serials give 10 ACTIVE objects with the warranty starting on the ship date, and a re-delivery creates none. The trigger is the receive endpoint until wave 28 publishes the shipment event (handoff). |
| AC-45.9 Phase-8 exit walkthrough | handoff | Spans waves 41, 42, 43, 44 and this wave. Owned by the phase-exit demo. |
| AC-45.10 HR stage gate `CASE-STG-001` | built | `TestStore_GenericCaseStageGate` (as passage_app): INTAKE → INVESTIGATION, RESOLUTION refused with `CASE-STG-001` while the "Interview" activity on the case is OPEN (a DONE "Interview" on another record does not count), then accepted once it is DONE; `TestAPI_GenericCaseFlow` checks the code over HTTP. |
| AC-45.11 HR case category 403 | partial | `TestStore_GenericCaseCategorySecurity` and `TestAPI_GenericCaseFlow`: a collector without the HR category's access duty gets 403 `CASE-SEC-001` on read and write and no list row, while duty holders and the owner can read it. Not built: the subject's manager reading the case (needs the wave-42 manager chain; see BR-45.10). |
| AC-45.12 KB ranking and `KB-PUB-001` | built | `TestStore_KnowledgeLifecycleAndSuggestions`: the article that applies to the case item ranks first for a case description, and the author cannot publish (`KB-PUB-001`). |
| AC-45.13 Supplier claim settlement | partial | `TestStore_SupplierClaimSettlesAC45_13` (as passage_app): a posted work order with a WARRANTY part of 180.00 and 1.5 WARRANTY labor hours claims 270.00 at a vendor rate of 60.00; the vendor accepts 250.00; the matched credit note makes the claim SETTLED with recovered 250.00 and writes the settled event with recoveredAmount 250.00. The credit of 250.00 to the warranty expense account is posted by the GL consumer of that event (handoff). |
| AC-45.14 Email threading | built | `TestStore_EmailIntakeThreadsAC45_14` and `TestAPI_ClaimAndEmailIntakeFlow`: an email with no token from an unknown sender opens a case in the mailbox's default category with the acknowledgment; "Re: [CS-n] pump noise" on the open case is logged on it with no new case and no acknowledgment (200 with `CASE-MAIL-001`). |
| AC-45.15 My activities | built | `TestStore_ActivitiesListCompleteAndReschedule`: a project task and a case task of one owner both appear with their records, and completing one sets DONE with its timestamp. The project is referenced by id; wave 41 is not called. |
| Ticket AC1 entitlement fixtures | met (customer agreements) | See AC-45.1; parent-party fixtures wait for the party hierarchy. |
| Ticket AC2 dispatch feasibility (property) | met for the greedy solver | `services/dispatch-py/tests/test_feasibility_proof.py` (derandomized Hypothesis, run in CI): greedy plans pass an oracle written from BR-45.3 on the original and an adversarial generator, `plan_violations` agrees with that oracle rule for rule on mutated plans, and nothing is left unassigned that an idle technician could serve. The CP-SAT model and the AC-45.4 scale and timing run are not built (see AC-45.4). |
| Ticket AC3 billable vs warranty posting | met | `svc-billing-30` (`internal/domain/testdata/`, answers from the independent `reference_billing.py`): 30/30 orders post the reference lines and totals in `TestPost_SvcBilling30` and as real work orders through `PostWorkOrder` as passage_app in `TestStore_SvcBilling30PostsWorkOrders` (warranty term edges, warranty before agreement, FIFO hours across lines and across three orders, half-even per line in USD, JPY and KWD, the posted event's totals and billable lines). Invoices from the posted event are wave 29 (handoff). |
| Ticket AC4 phase-8 exit | handoff | See AC-45.9. |
| Ticket AC5 every deliverable has code or a row here | met | This file. |
| Ticket AC6 vet/test, web tests, specs match, chart lints | met (slice 5 included) | Service `go vet`/`go test` pass; web module vitest and tsc pass; `api/openapi.yaml` and `events/asyncapi.yaml` match the handlers and events; chart lints (see README). |

## Fixtures and test plan (PRD 8.45.9)

| Item | Status | Reason |
|---|---|---|
| `svc-installed-base` (2,000 objects) | later slice of PSG-45 | The installed base is built; the 2,000-object fixture is not. |
| `svc-entitlement-50` | built | `internal/domain/testdata/svc-entitlement-50` (see AC-45.1). |
| `svc-dispatch-gen` | partial | Hypothesis generator in `services/dispatch-py/tests/test_properties.py`; seeded OSRM matrix not built. |
| `svc-mobile-replay` | later slice of PSG-45 | Needs BR-45.6. |
| `svc-billing-30` | built (domain and store) | `internal/domain/testdata/svc-billing-30` (see Ticket AC3). The PRD's "contract against the invoicing API" waits for wave 29. |
| Reconciliation: Σ posted billable = wave-29 invoices; Σ warranty = GL; truck issues = part actions | later slice of PSG-45 (and handoff for the wave-29 and GL sides) | Needs invoice creation, truck stock and warranty sourcing. |
| PWA Playwright offline E2E | handoff | `apps/technician` not built. |
| Breach monitor performance (100k cases < 60 s) | later slice of PSG-45 | The pass is built on the partial index `service_case_sla_running` and batches with `limit`; the 100k-case timing run is not. |

## Data model and operations (PRD 8.45.4, 8.45.10, 8.45.13, 8.45.19)

| Item | Status | Reason |
|---|---|---|
| ServiceObject, Warranty | partial | `service_object` (migration 0005) carries the warranty term inline; there is no separate Warranty table, `warranty_id` or `shipment_line_id`. |
| KnowledgeArticle, Activity | partial | `service_kb_article` (one row per version, `revision` as the ETag, generated `tsvector` with a GIN index), `service_kb_vote` (one vote per user and version) and `service_activity` (associations as jsonb with a GIN index) in migration 0005. The tsvector uses the `simple` configuration for every language; per-language dictionaries and the title-only fallback index (8.45.18) are not built. `CaseArticleLink` and `views` are not built. |
| Technician, DispatchRun, ServiceWorkOrder assignment fields, geo (PostGIS point index) | later slice of PSG-45 | Not in the migrations yet. |
| SupplierWarrantyClaim, InboundMessage | built (migration 0007) | `service_supplier_claim` (lines as jsonb, legal-entity scope policy) with `service_supplier_claim_source` (one row per claimed work order), `service_case_mailbox` and `service_inbound_message` (Message-ID unique per tenant, legal-entity scope policy), with tenant RLS and passage_app grants. InboundMessage carries the route, the continued case and the acknowledgment flag beside the PRD fields. |
| `Case` category fields, stages, CaseAssociation, case log | partial | `service_case_process`, `service_case_category`, `service_generic_case`, `service_case_association` and `service_case_log` (migration 0006, with tenant RLS, the legal-entity policy on cases and passage_app grants). `CaseCategory.opa_policy_ref` is `access_duty`; `CaseProcess` stages are jsonb without entry/exit criteria; `service_case` itself gained no category fields (see FR-45.12). |
| ETag on reads and `If-Match` on PUT/PATCH/custom methods | built for work orders, cases, service objects, activities, articles and agreements | Their create, read and custom methods set `ETag` (articles: the revision), and the updates require `If-Match`. Publishing takes If-Match but no Idempotency-Key (the PRD lists "key + ETag"): a replay fails the If-Match check instead of returning the stored result. Agreement status changes take If-Match; agreements have no field update endpoint yet. SLA profiles have no update endpoint, so they carry no version. |
| Number sequences from wave-5 definitions | partial | Fixed prefixes `SA`, `CS`, `SWO`, `KB` allocated by the store; per-legal-entity definitions are not read yet. |
| Parameters 8.45.13 (default SLA profile, warning threshold, dispatch time, solver limits, buffer, overtime, negative truck, reopen window, parent-party match) | partial | The business calendar and holidays per SLA profile are built. The legal entity's default SLA profile is not stored; the case request carries the fallback. `pauseOnParts` is applied by the case transitions (slice 2). The SLA warning threshold is fixed at the 75 % default (no tenant parameter yet). The rest wait for the features above. |
| `service-sla-monitor` schedule | handoff (platform scheduler) and later slice | One pass per tenant is built (`POST /v1/service/sla:scan`); running it every 60 s for every tenant needs the platform scheduler (wave-23 patterns) or a chart CronJob with a service principal per tenant, and the "monitor lag > 2 min" alert waits for that. |
| Event `dev.passage.service.workorder.completed.v1` | later slice of PSG-45 | Its trigger is the mobile complete action (technicianId, lines, durationMinutes; 8.45.5), which needs the action log (BR-45.6). The `:transition` to COMPLETED sets the completion time and first-time fix but emits no event yet. |
| Scheduled jobs (`service-dispatch-auto`, `service-truck-replenish`, `service-ib-from-shipments`, `service-agreement-visits`, `case-email-ingest`, `case-consolidate`, `supplier-claim-deadline`, `kb-reindex`) | later slice of PSG-45 (and handoff) | Each depends on a feature above. `case-email-ingest` is the wave-35 hub calling `POST /v1/inbound-messages` (handoff); `supplier-claim-deadline` (drafts within 5 days of the window end) needs the platform scheduler: the claims carry `windowEndsOn` for it. |
| Feature flag `service.allow-negative-truck` | later slice of PSG-45 | Needs BR-45.7. |
| Chart `passage-service` (TS API, breach monitor, mobile BFF) and `passage-dispatch` (Python worker, OSRM sidecar) | partial | Chart `services/fieldservice/charts/fieldservice` is built. The dispatch worker chart and OSRM sidecar are later. |
| Privacy: technician location retention (90 days) and works-council flag; HR `hr_pii` protection; DSR purge of inbound email bodies | later slice of PSG-45 | Needs live tracking; HR categories exist (slice 4) but their free text is not yet in a protected column set. Inbound email bodies are stored (slice 5) but the data-subject purge with the case is not built. |
| Migration from AX/SAP (PRD 8.45.11) | later slice of PSG-45 | Needs the entities above. |

## Handoffs (outside owned paths)

| Owner | Request | Why |
|---|---|---|
| Policies (`policies/`) | Define OPA duty mappings for `SvcAgreementMaintain`, `SvcCaseCreate`, `SvcCaseMaintain`, `SvcInquire`, `SvcWoMaintain`, `SvcBillingPost`, `SvcDispatch`, `SvcTechnician`, `SvcObjectMaintain`, `SvcClaimMaintain`, `ActivityMaintain`, `KbRead`, `KbAuthor`, `KbPublish`, `CaseCreate`, `CaseMaintain`, `CaseSetupAdmin`, and the per-category access duties set up in case categories (for example an HR partner duty) | Operations declare these duties in `x-passage-duty`; enforcement is via OPA. The PRD duty table (8.45.17) names no installed-base duty, so `SvcObjectMaintain` is this service's proposal. A per-category OPA policy (BR-45.10) would replace the access duty. |
| Policies (`policies/authz/data.json`), authz seed for slice 6 | No new duty and no service principal. Add to `SvcAgreementMaintain`: `{"resource": "/v1/service/agreements/*:transition", "methods": ["POST"]}`, `{"resource": "/v1/service/agreements:expire", "methods": ["POST"]}`, `{"resource": "/v1/service/agreements/*/visits", "methods": ["POST"]}`. Add to `SvcInquire`: `{"resource": "/v1/service/agreements/*/visits", "methods": ["GET"]}`, `{"resource": "/v1/service/agreements/*/history", "methods": ["GET"]}` (the existing `/v1/service/agreements/*` glob stops at `/` and `:`, so it covers none of these) | FR-45.3 |
| Wave 29 (billing, subscriptions) | Consume `dev.passage.service.agreement.changed.v1` (start the subscription of a SUBSCRIPTION agreement on ACTIVE, stop it on SUSPENDED, CANCELED or EXPIRED) and `dev.passage.service.agreementvisit.recorded.v1` (bill PER_VISIT agreements) | FR-45.3 |
| Platform scheduler | Run the agreement expiry pass daily for every tenant, with the `service-sla-monitor` schedule. A scheduler that calls `POST /v1/service/agreements:expire` over HTTP needs a service principal holding `SvcAgreementMaintain` with every legal entity in scope (seed it then); a chart CronJob running the pass in the service binary against its own database (the costacct `gl-reconcile` pattern) needs none | FR-45.3 |
| Wave 28 (serialized shipments) | Publish a shipment-confirmed event (shipment, ship date, customer, legal entity, site, item lines with serials) that the service can consume to create service objects; until then a caller posts it to `:receive-shipment` | FR-45.1, AC-45.8 |
| Wave 29 (invoicing) | Consume `dev.passage.service.workorder.posted.v1` to create invoices (billable lines only, source SWO) | FR-45.10 and the reconciliation |
| Wave 16 (locations) | TRUCK location type per technician; consumption issue API | FR-45.9, BR-45.7 |
| Wave 27 (pricing) | Price list lookup for labor and parts (BR-45.8) | BR-45.8 |
| Wave 35 (notification hub, mailboxes) | Poll or receive each case mailbox and call `POST /v1/inbound-messages` (Message-ID, In-Reply-To, Auto-Submitted, X-Autoreply, body, attachments stored in the object store) with a principal holding CaseCreate and the categories' access duties; match the sender to a party contact and pass `customerPartyId`; send the acknowledgment when the answer says `acknowledge` (with the `[CS-n]` token in the subject); send outbound replies and the supplier claim document (PDF, email or EDI); activity reminders (`reminderMinutes` before the start or due time) | FR-45.16, FR-45.15, FR-45.14 |
| Wave 44 (EAM) | Publish `dev.passage.eam.warranty.claimable.v1` for the service to create EAM_WO claims from | FR-45.15 |
| Wave 18 (AP credit notes) | Credit note with the vendor's claim reference, to call `:settle` (or an event this service consumes) | BR-45.12 |
| Waves 30/17 (vendor returns) | Vendor return of the failed part; its id goes on the claim before submission | BR-45.12 |
| GL (warranty recovery) | Consume `dev.passage.service.supplierclaim.settled.v1` and credit the warranty expense account of the legal entity with `recoveredAmount` | BR-45.12, AC-45.13 |
| Event catalog owner | The PRD names `dev.passage.service.supplier-claim.settled.v1`; the kernel's event type rule allows no hyphen, so it is published as `supplierclaim` | PRD 8.45.5 |
| Wave 47 (copilot) and search | Consume `dev.passage.knowledge.article.published.v1` (its `retiredArticleId` drops the version it replaced) | FR-45.13 |
| Wave 42 (workers and skills) | Technician entity links to workers; skills and certification expiry (BR-42.8) | BR-45.3 |
| Wave 5 (sequences) | Per-legal-entity number sequences `SA`, `CS`, `SWO`, `SWC`, `KB` | Parameters 8.45.13 |
| Web BFF (`services/web-bff-ts`) and the dev proxy (`apps/web/src/shell/devProxy.ts`) | Rewrite `/api/v1/service/...` to `/v1/service/...`, and route `/api/v1/cases`, `/api/v1/case-categories`, `/api/v1/case-processes`, `/api/v1/case-mailboxes` and `/api/v1/inbound-messages` to this service as `/v1/...`; set `X-Passage-CSRF` | Web module calls; the generic case routes follow the PRD paths (8.45.5) outside `/service` |
| Wave 42 (workers) | Manager chain as of a date for a worker (BR-42.1) | BR-45.10: the HR case subject's manager may read the case (AC-45.11) |
| Wave 30 (disputes) and wave 9 (product change requests) | An export or event of their open minimal cases with ids, so `case-consolidate` can create COLLECTIONS and PRODUCT_CHANGE cases keeping the ids as associations; the collector of a customer (BR-45.10) | FR-45.12 |
| Wave 3 (metadata registry) | Read check and existence lookup by registry entity type and id | Case associations: "restricted record" display and checking an associated record exists (FR-45.12, BR-45.10) |
| Platform kernel | CEL evaluator over a record and its associations | BR-45.9 stage entry and exit criteria |
| Workflow and notifications | Consume `dev.passage.case.stage.changed.v1` | PRD 8.45.5 consumers (workflow, notifications, HR, collections, audit owners) |
| Deploy (`deploy/`) | Helm umbrella entry and Argo CD app for `fieldservice` | Deployment |
| Platform scheduler | Run `POST /v1/service/sla:scan` every 60 s per tenant with a service principal holding `SvcCaseMaintain` (`service-sla-monitor`) | BR-45.2, 8.45.16 |
| Notifications (wave 35) | Consume `dev.passage.service.sla.warning.v1` and `sla.breached.v1` to notify (messages `SVC-SLA-001`, `SVC-SLA-002`) | AC-45.3 "warning notification fires" |
| Platform kernel | Shared ETag helper (the service sets the header itself today) | Convention |
| Wave 8 (calendars) | Public-holiday calendars per site that SLA profiles can reference instead of their own date list | BR-45.2, 8.45.20 |
| Party hierarchy owner | Parent party lookup for the parent-party entitlement match | BR-45.1 |
