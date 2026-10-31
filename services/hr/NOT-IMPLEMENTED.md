# NOT-IMPLEMENTED — services/hr (PSG-42, wave 42)

Slice 1 builds workers, employments, positions, position assignments, the
as-of manager query, the PII vault with audit, the outbox events, list and
detail reads for workers, employments and positions, and the HR web screens
for all of them. Slice 2 builds absence: plans, accrual runs keyed per
(plan, period, worker), year end with carry-over and carry-over expiry,
balances per plan year, requests with the balance, span and overlap checks,
approval with the `absence.approved` event, rejection and cancellation of
submitted requests, and the web screens for plans, the request queue and the
worker's absence. Slice 3 builds benefits: plans with options and rates,
the CEL eligibility rule with the waiting period, the whole-workforce
simulation, life events, enrollment inside the open window or a life event's
window, ending an enrollment (termination to the end of the month), the
`benefitenrollment.changed` event, and the web screens for plans, the
simulation and the worker's benefits. Slice 4 builds skills: the skill
catalog with rating scales, worker ratings with the stale-assessment guard and
the `workerskill.changed` event, certificates with expiry (given as a date or
as validity months) and the `certification.issued` event, the
certifications-expiring inquiry, position skill requirements, gap analysis
(AC-42.16) and the skill search by ratings and valid certificates, with the
web screens for all of them. Slice 5 builds courses: the catalog with skill
outcomes and a certificate, sessions with capacity, waitlist, instructor and
cost, registrations seated in order with the waitlist and HCM-CRS-001,
manager approval with HCM-CRS-002, cancellation with waitlist promotion,
completion that writes skills (never lowered) and certificates with the
`course.completed` event (AC-42.9), and the web screens for courses,
sessions, registrations and attendance. Everything below is either a later slice of PSG-42 or a handoff to another wave or owner.
Statuses: **later slice of PSG-42** (same wave, next slice) or **handoff** (needs
another owner).

## Deliverables (ticket items 1–8)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| 1 | Jobs (title, type, function, pay range) | later slice of PSG-42 | Slice 1 references `jobCode` as text; the `Job` entity and its pay range (EU pay transparency) come with compensation |
| 1 | Compensation plans and `FixedCompensation` (effective-dated) | later slice of PSG-42 | Compensation and the compa-ratio rules come with the cycle work (FR-42.22) |
| 1 | Org assignments to wave-6 org units (department as a real org unit) | handoff (wave 6 org) | `departmentId` is stored as a uuid and not validated against the org hierarchy; needs an org read contract |
| 1 | Position hierarchy edits after creation (reports-to change) with cycle check | later slice of PSG-42 | Domain cycle check (BR-42.7) is implemented and tested; no edit endpoint yet |
| 1 | Termination as an employment change (end the validity range) | later slice of PSG-42 | An employment can be created with a termination date (inclusive last day; `validTo` is the next day). Ending an existing employment arrives with personnel actions (FR-42.5) |
| 1 | Employment fields `seniority_date`, `termination_reason`, `pay_group_id`, `work_calendar_id` | later slice of PSG-42 and handoff (waves 8, 43) | Not stored. They need the termination-reason code table, the wave-8 work calendars and the wave-43 pay groups |
| 1 | Employment status `ON_LEAVE` | later slice of PSG-42 | `status` is derived from the range (PENDING, ACTIVE, TERMINATED). ON_LEAVE needs leave-of-absence actions |
| 1 | Invariant: every ACTIVE employment has one primary assignment or an `unassigned` flag | later slice of PSG-42 | Not enforced; it needs the hire action to create both rows together |
| 1 | Position cost dimensions (`dimension_set_id`) | handoff (wave 11 dimensions) | Not stored |
| 1 | Personnel and position numbers from sequences `WRK` and `POS` | handoff (wave 5 numbering) | Slice 1 takes caller-supplied numbers and enforces uniqueness per tenant |
| 1 | Chain of command and direct reports (`GET /positions/{id}/reports?asOf=&depth=`, FR-42.4) | later slice of PSG-42 | The as-of manager is built; the reports endpoint and the org chart screen are not |
| 2 | Absence plans, accrual runs, year end, carry-over expiry, balances, requests, approve and reject | **built** | See the service README. Accrual and the year-end jobs are endpoints an absence admin runs; see the scheduler row below |
| 2 | Absence calendars (team calendar in MSS, absence calendar report 8.42.14) | later slice of PSG-42 | Needs a worker × day projection and the department from the wave-6 org read contract |
| 2 | Schedulers `hr-accrue` (per plan frequency) and `hr-carryover-expiry` | later slice of PSG-42 | The endpoints are idempotent per worker and safe to schedule; the scheduled jobs (CronJob or Temporal) are not built |
| 2 | Balance adjustments (HcmAbsenceAdmin "adjust") and opening balances from AX/SAP | later slice of PSG-42 | The `adjusted` column exists and enters the balance, but no endpoint writes it |
| 2 | Cancelling an APPROVED request (reverse the usage if it starts in the future, or within the 30-day window with manager approval) | later slice of PSG-42 | Only SUBMITTED requests can be cancelled (HCM-ABS-STATE otherwise). Reversal needs the carry/current split of the usage stored on the request and a `absence.canceled` event for time and payroll |
| 2 | DRAFT requests | later slice of PSG-42 | A request is validated and submitted in one call |
| 2 | Blackout periods (FR-42.7) | later slice of PSG-42 | Needs a blackout calendar per plan or legal entity |
| 2 | Routing a request to the worker's manager; manager sees only the team | handoff (platform OPA, wave 4 workflow) | `MssApprove` decides any request in the tenant and sees the tenant's queue; only self-approval is blocked. Team scoping by the position hierarchy belongs to the OPA policy, routing to wave-4 workflow |
| 2 | Plan eligibility (which workers a plan covers) and per-employment plans (BR-42.14) | later slice of PSG-42 | Every plan accrues for every worker with a PRIMARY employment in the period |
| 2 | `PER_HOUR_WORKED` accrual and the PAYDATE accrual day | handoff (waves 25 and 43) | `PER_HOUR_WORKED` plans can be defined but a run is refused (HCM-ABS-PERIOD) until wave-25 hours exist. PAYDATE is treated as the period's last day until wave-43 pay calendars exist |
| 2 | Request unit rounding (0.5 day / 0.25 h, 8.42.13) | later slice of PSG-42 | Quantities take up to two decimals and at most one day (or 24 h) per calendar day; no rounding parameter yet |
| 2 | Negative balance at termination raises a payroll deduction suggestion (BR-42.4) | later slice of PSG-42 and handoff (wave 43) | Needs termination actions (FR-42.5) and the payroll suggestion contract |
| 2 | Employment status ON_LEAVE from approved absence | later slice of PSG-42 | Comes with leave-of-absence personnel actions |
| 2 | Time integration (wave 25 absence registrations) and the reconciliation Σ usage = approved = time registrations | handoff (wave 25) | `absence.approved` is published; the reconciliation test needs wave 25's registrations |
| 2 | `absence.approved` carries hours | handoff (wave 8 work calendars) | The event carries `quantity` and `unit`; a DAYS plan can't be turned into hours without the worker's work calendar |
| 2 | Accrual balance events to ESS | later slice of PSG-42 | Depends on the ESS portal; balances are read through the API |
| 3 | Benefit plans, options, eligibility (CEL), simulation, open enrollment, life events, enrollments and ending them | **built** | See the service README. The rule language is a CEL subset implemented in `internal/domain/cel.go` (no new dependency); it has no arithmetic, macros or functions, and `!` applies to the whole comparison after it |
| 3 | Eligibility facts `age` and `job.type` (BR-42.6) | later slice of PSG-42 | Not available to rules yet: the date of birth is not collected (ESS personal data) and the `Job` entity comes with compensation. A rule that reads them is not eligible with the reason "unknown identifier" |
| 3 | Dependents as records (`dependents[]`: name, relationship, birth date) | later slice of PSG-42 | Only `dependentCount` is stored. Dependent names and birth dates are PII and need the vault (FR-42.1) and ESS personal data |
| 3 | Per-dependent or coverage-tier pricing, per-pay-period cost from a pay calendar | handoff (wave 43 payroll) | The enrollment returns the option's employee and employer rate with its basis (PER_PAY_PERIOD or MONTHLY) as the cost preview; converting MONTHLY to a pay period needs the wave-43 pay calendar |
| 3 | Ending enrollments automatically when an employment terminates (BR-42.6) | later slice of PSG-42 | Termination actions (FR-42.5) are not built. `POST /benefits/enrollments/{id}/end` with `TERMINATION` applies the end-of-month rule and is what the action will call |
| 3 | Changing the option of an open enrollment in place | later slice of PSG-42 | A change is end (LIFE_EVENT) then enroll again; no single "change option" call |
| 3 | Life-event window per legal entity (8.42.13, default 30, 0–90) | later slice of PSG-42 | The window is the 30-day default for every entity; the parameter store is not built |
| 3 | Life-event evidence and HR verification | later slice of PSG-42 | An employee records their own life event and it opens the window at once; no evidence document or approval step |
| 3 | COBRA notices and premium billing | handoff (wave 48 US pack) | `cobra_qualifying_event` is set on a TERMINATION end of a MEDICAL, DENTAL, VISION or FSA enrollment, whatever the country; wave 48 decides where COBRA applies and sends notices |
| 3 | Plan provider checked against the wave-7 party service; plan end and retirement of options | handoff (wave 7) and later slice | `providerPartyId` is stored as a uuid; plans and options cannot be edited or retired yet (no update endpoint, so no ETag) |
| 3 | Benefits enrollment wizard in ESS and "My benefits" for employees | later slice of PSG-42 (ESS) and handoff (shell) | The web module has the HR screens. An employee with only `EssBenefits` can call the API for their own worker but has no page until the shell gives the signed-in user's worker id or `apps/ess` exists |
| 3 | Employee self-service and manager self-service | later slice of PSG-42 (ESS) and handoff (shell) | See `apps/ess/README.md`; MSS needs the approval flows |
| 4 | Skills and certifications (FR-42.10, BR-42.8) | **built** | See the service README. Certificate codes are free text checked for shape; there is no certificate-type catalog (issuer body, default validity) yet |
| 4 | Certificate expiry notifications at 60/30/7 days (BR-42.8) | handoff (wave 5) and later slice | The expiring inquiry (`GET /certifications/expiring`, 8.42.14) and its screen are built; sending notifications needs the FR-42.20 reminder rules on the wave-5 alert engine and the `hr-cert-expiry` schedule |
| 4 | Expired certificate removes the skill match in wave-45 dispatch | handoff (wave 45) | `certification.issued` carries `expires_on`; dispatch must stop matching on that day. HR's own skill search already ignores expired certificates |
| 4 | Rating history | later slice of PSG-42 | Only the current rating per worker and skill is stored; the history is in the `workerskill.changed` events |
| 4 | Goals, review templates, review cycle with sign-off (FR-42.11) | later slice of PSG-42 | Not started |
| 4 | Recruitment pipeline basics | later slice of PSG-42 | Not started (FR-42.21) |
| 5 | Consent records, retention policies per data class, country field slots | later slice of PSG-42 | Not started; retention purge job `hr-retention-purge` not built |
| 5 | PII vault: encrypted columns, masked reads, audited unmasked reads | **built** for national ID and bank account | `worker_pii` sealed with AES-256-GCM, masked by default, reveal needs `HcmPIIRead` and a reason, audit row in the same transaction. Date of birth and home address (FR-42.1) are not collected yet; they come with ESS personal data |
| 5 | Separate `hr_pii` schema, its DB role and grants | handoff (deployment-cicd) | Slice 1 keeps `worker_pii` in the `hr` schema. A separate schema and role need deployment grants (no BYPASSRLS for the app role) and a schema move in a later migration |
| 5 | Tenant PII key from Vault; key rotation job `hr-pii-rekey` | handoff (deployment-cicd) and later slice | P3 values are sealed with platform `fieldcrypt` (#285; keys from `FIELDCRYPT_KEYS`, tenant and field bound); `HR_PII_KEY` only opens legacy values, and no job re-seals them yet; Vault and External Secrets wiring and the re-encrypt job are not built |
| 6 | Courses, sessions, registrations, completion to skills and certifications | **built** | See the service README (FR-42.14, BR-42.9, AC-42.9) |
| 6 | Course types, course groups as a table, agenda templates and multi-day agenda lines | later slice of PSG-42 | `course_group` is free text; there is no `course_type_id` or `agenda_template_id`, and a session is one start-end range |
| 6 | Locations, classrooms and instructors as catalog entities (HRMCourseLocation/Classroom, instructor table) | later slice of PSG-42 and handoff (wave 7 party) | A session's location is free text; an internal instructor is checked as a worker; `instructorPartyId` is stored as a uuid and not checked against the wave-7 party service |
| 6 | Certificate type on the course (`course.certification_id`) | later slice of PSG-42 | There is no certificate-type catalog yet (see deliverable 4), so the course names the certificate code and title it issues |
| 6 | Session cost posting to the attendee's department dimension (BR-42.9, parameter "Post course costs to GL") | handoff (wave 11 posting API, wave 6 departments) | The cost per attendee and currency are stored and shown; nothing is posted and the parameter does not exist |
| 6 | Tenant parameter "Course waitlist default size" (8.42.13) | later slice of PSG-42 | The default is the parameter's default 5 for every tenant; a session can state its own 0-100 |
| 6 | Course confirmation letter, attendance list and course certificate documents (8.42.15) | handoff (document rendering) and later slice | The attendance list is the registrations read and its screen; no PDF, email or iCalendar output |
| 6 | Course attendance report (8.42.14: attendance %, pass %, cost by course and date range) | later slice of PSG-42 | Per-session counts are on the session read; no cross-session report |
| 6 | Job `hr-course-completion-transfer` (8.42.16) | built differently | The transfer to skills and certificates runs in the completion transaction, so there is nothing left for a sweep to find |
| 6 | Course browsing and self-registration in ESS, and the worker's own registrations ("My courses") | later slice of PSG-42 (ESS) and handoff (shell) | The API lets an `EssLearning` employee register and cancel for themselves; the web screens are the learning-admin ones until the shell gives the signed-in user's worker id. There is no per-worker registrations read |
| 6 | Managers register and approve only their team | handoff (platform OPA, wave 4 workflow) | `MssApprove` registers, approves and cancels for any worker in the tenant; only self-approval is blocked |
| 6 | Course evaluation questionnaires | later slice of PSG-42 | Comes with the questionnaire engine (FR-42.15) |
| 6 | Paging of a course's sessions and a session's registrations | later slice of PSG-42 | Each read returns at most 500 rows in order; the catalog itself pages by code |
| 6 | Questionnaire engine (design, schedules, answers, results) | later slice of PSG-42 | Not started (FR-42.15, BR-42.10) |
| 7 | Injury and illness register, OSHA 300/300A/301 extracts | later slice of PSG-42 | Not started (FR-42.16, BR-42.11); the form layouts and ITA submission are in the wave-48 US pack |
| 7 | Loaned items | later slice of PSG-42 | Not started (FR-42.17); the link to wave-44 assets is a handoff to wave 44 |
| 7 | Skill mapping and gap analysis (FR-42.18) | **built** for workers and positions | Search by skills with minimum ratings and by certificates valid on the date, over workers employed on that date, at most 500 results. Gap analysis compares a worker with a position's skill profile as (skill, required, actual, delta) |
| 7 | Skill search `availableFrom` (availability) | later slice of PSG-42 and handoff (waves 25, 45) | Refused with a field error until availability exists (absence calendars, wave-25 time, wave-45 schedules) |
| 7 | Skill search over FR-42.21 candidates | later slice of PSG-42 | Candidates are not built |
| 7 | Job skill profiles (gap against a job, not only a position) | later slice of PSG-42 | The `Job` entity does not exist yet; positions carry the profile |
| 7 | Manager sees only their subtree in skill search and gap (8.42.17, OPA) | handoff (platform OPA) | `HcmOrgInquire` searches the tenant; positions follow the legal-entity scope |
| 7 | GIN index on WorkerSkill (8.42.19) | built differently | A btree on (tenant, skill, rating) answers "at least n on skill s" as a range scan; the 500/day budget is not load-tested |
| 8 | Onboarding and offboarding checklists, worker tasks | later slice of PSG-42 | Not started (FR-42.19, BR-42.12) |
| 8 | HR reminders on the wave-5 alert rules | handoff (wave 5) | The reminder rule model needs the alert rule engine; not started |
| 8 | Requisitions, candidates, offers | later slice of PSG-42 | Not started (FR-42.21, BR-42.15) |
| 8 | Compensation planning cycles and worksheets | later slice of PSG-42 | Not started (FR-42.22, BR-42.13) |
| 8 | Concurrent and global assignments | **partly built** | Employment `assignmentKind` (PRIMARY, CONCURRENT, GLOBAL_HOST), the per-entity exclusion and GLOBAL_HOST validation are built. Per-employment pay groups for payroll (wave 43) and absence balances on PRIMARY only are not |

Deploy items: **HR workspace** is built as the web module
`apps/web/src/modules/hr` (workers, positions, worker detail with PII reveal,
employments, the as-of view, the worker's absence and benefits; position
detail with assignments; absence plans with accrual and year end; the absence
request queue; benefit plans with the eligibility simulation; the skill
catalog, skill search and expiring certifications, the worker's skills,
certificates and gap, and the position's skill profile; the course catalog,
course detail with sessions, and the session's registrations with approval,
cancellation and attendance). The HR workspace
tiles (pending actions, expiring certifications, open enrollment), the org chart, personnel actions, absence calendars and the
self-service portal screens are not. The **ESS
portal** is not built here (apps/ess README). **Demo workforce loaded** is not
built (needs the `hr-workforce-1200` generator under `testdata/`).

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC-42.1 as-of manager, 1,200 workers, 50,000 dates, p95 < 50 ms | partly built | The algorithm is checked against an independent model with 100-plus random hierarchies (`properties_test.go`). The database check at 1,200 workers and 50,000 dates and the p95 measurement are not built |
| AC-42.2 overlapping assignment rejected unless overfill | **built** (store test) | Overfill check is conservative: it sums every assignment that overlaps the new range, not per instant. The DB test needs `PASSAGE_TEST_DATABASE_URL` |
| AC-42.3 accrual on US-PTO-15 | **built** (domain and store tests) | `TestUSPTO15AccrualYearEndAndCarryOver` runs 0.65, 11.90, 7.90, 5.00 carried, 2.90 expired and 6.25 through the database. The DB test needs `PASSAGE_TEST_DATABASE_URL`. The 36-case fixture is the `hr-accrual-cases` row below |
| AC-42.4 negative balance beyond request | **built** (domain and store tests) | A 3.00 request on a 2.00 balance is HCM-ABS-002 at submission and again at approval |
| AC-42.5 eligibility on 100 fixtures | **built** (domain and store tests) | `TestEligibilityFixtureHundred` runs the PRD rule and 5 more over the 100 employees of `testdata/hr-benefit-100.json`, whose expected sets come from an independent reference. `TestSimulateMatchesEnrollment` checks the simulation against enrollment through the database (needs `PASSAGE_TEST_DATABASE_URL`) |
| AC-42.6 future-dated transfer on its date | later slice of PSG-42 | Personnel actions and the Temporal timer not started |
| AC-42.7 masked national ID and audited read | **built** (store and API tests) | DB test needs `PASSAGE_TEST_DATABASE_URL`; the no-DB API test covers the duty and reason gate |
| AC-42.8 SCIM user within 60 s | handoff (platform / Keycloak) and later slice | SCIM worker not built; needs Keycloak admin access and the personnel-action events |
| AC-42.9 course waitlist | **built** (domain and store tests) | `TestCourseWaitlistAndCompletionAC429` runs capacity 10 and waitlist 2 with 13 registrations, the promotion on cancel, and the PASS that rates FORKLIFT 3 and issues a certificate expiring 2028-06-01 for a 2026-06-01 session, through the database (needs `PASSAGE_TEST_DATABASE_URL`) |
| AC-42.10 anonymous questionnaire threshold | later slice of PSG-42 | Questionnaires not started |
| AC-42.11 OSHA 300 extract | later slice of PSG-42 | Incidents not started |
| AC-42.12 onboarding task date | later slice of PSG-42 | Checklists not started |
| AC-42.13 compensation budget block | later slice of PSG-42 | Cycles not started |
| AC-42.14 concurrent and global as-of with pay groups | partly built | Both employments appear in `as-of` (entities are separate rows). Payroll pay-group memberships are wave 43 |
| AC-42.15 loaned item blocks offboarding | later slice of PSG-42 | Loaned items and offboarding not started |
| AC-42.16 skill gap | **built** (domain, store and web tests) | `TestSkillGapAC4216` runs POS-200 (WELDING ≥ 3, PLC ≥ 2) against WELDING 4, PLC 1 through the database and gets only (PLC, 2, 1, −1). The DB test needs `PASSAGE_TEST_DATABASE_URL` |

## Platform and API conventions not yet used

| Item | Status | Reason |
|---|---|---|
| Kernel `httpx` idempotency middleware (replay headers, 60 s lease, 24 h expiry) | handoff (platform) and later slice | Slice 1 stores the `Idempotency-Key` on the worker, employment, position and assignment rows (the org pattern): a replay with the same body returns the stored row, another body is `idempotency_key_reuse`, a concurrent duplicate is `idempotency_key_in_flight`. Keys do not expire and there is no replay header |
| `ETag` on reads and `If-Match` on PUT/PATCH/DELETE | later slice of PSG-42 | Slices 1 to 3 have no update endpoints; `ETag` is not set on reads yet, so the OpenAPI does not declare it. Absence decisions and benefit enrollment ends are guarded by row locks and the row state instead. Slice 4's rating and requirement writes are set-to-value upserts (a retry is harmless): a rating is guarded by its assessment date (an older one is HCM-SKL-STALE) under a worker row lock, and a requirement is last-writer-wins |
| `Idempotency-Key` on absence decide, accrue and close, on ending an enrollment, and on course registration, approval, cancel and completion | built differently | Accrual, year end and expiry are keyed per (plan, period or year, worker) in the database, so retries are safe without a key. A decision or cancel replayed on a request that already has that outcome returns it unchanged; a different outcome is HCM-ABS-STATE. Ending an enrollment again with the same last day and reason returns it unchanged; otherwise HCM-BEN-ENDED. A course registration is unique per (worker, session), so registering again returns it; the same attendance outcome again changes nothing; cancel again is a no-op |
| OPA decision and `legalEntityScopes` | handoff (platform / edge-identity) | Duties are checked locally by `authz.RequireDuty`; legal-entity scoping is not enforced yet |
| Effective-date granularity TIMESTAMP and tenant-local midnight | later slice of PSG-42 | Slice 1 uses UTC days (DAY granularity) |
| Future-dated actions (up to 365 days) and workflow approval | later slice of PSG-42 | Depends on personnel actions and wave-4 workflow |
| `party_person_id` checked against the wave-7 party service | handoff (wave 7 party) | Stored as a uuid; existence is not checked |
| Outbox events `position.changed`, `employment.changed` for terminations, personnel action events | later slice of PSG-42 | Slice 2 adds `absence.approved`; slice 3 adds `benefitenrollment.changed` (the PRD's `benefit.enrollment.changed`, collapsed like `positionassignment`); slice 4 adds `workerskill.changed` and `certification.issued` (not in the PRD event table; wave-45 dispatch keeps its technician skills from them); slice 5 adds `course.completed`. Slice 1 emits `worker.created`, `employment.changed` (action HIRE, with the exclusive end when a termination date is given) and `positionassignment.changed` (with `manager_position_id`). The PRD name `position.assignment.changed` is written as `positionassignment` to fit the kernel event-type rule (see asyncapi) |

## Reference fixtures (PRD 8.42.9)

| Fixture | Status | Reason |
|---|---|---|
| `hr-workforce-1200` (generated workforce, 3 entities, 5 years) | later slice of PSG-42 | Needs the workforce generator and the actions to replay history |
| `hr-accrual-cases` (36 hire-date × plan cases) | **built** (synthetic, not accountant-reviewed) | `testdata/hr-accrual-cases.csv`: 6 hire dates × 3 plans (US-PTO-15, FLAT-20 without proration, CAP-12 with a binding cap and no carry-over) × 2 year-end usages. Expected balances are hand-computed from BR-42.3 (arithmetic in the last column), not generated by the code; `TestAccrualFixture36` checks accrued, discarded, 2026-12-31 balance, carried, expired and the 2027-01-31 balance. The 11 proration cases stay inline in `accrual_cases_test.go`. A payroll or leave accountant has not reviewed the expectations |
| `hr-benefit-100` | **built** | `testdata/hr-benefit-100.json`: 100 employees (12 hand-picked boundaries), 6 plans, expected eligible sets. It is evaluated in the domain test; loading it into the database is not needed for the check |
| `hr-pii-contract` (role × field matrix) | later slice of PSG-42 | The API tests cover the duty gate for `HcmPIIRead` and the reason rule. The full role matrix needs the OPA role model (handoff, platform) |

## Operations

| Item | Status | Reason |
|---|---|---|
| Schedules `hr-apply-actions`, `hr-accrue`, `hr-carryover-expiry`, `hr-cert-expiry`, `hr-reminders` | later slice of PSG-42 | Accrual, year end and carry-over expiry exist as idempotent endpoints; the schedules themselves, and the jobs for actions and reminders, are not built |
| Alert "accrual missing for active workers" (8.42.16) | later slice of PSG-42 | Needs the scheduler; `hr_absence_accrual_runs_total` is exported |
| Temporal workers (actions, SCIM) and the `passage-hr` worker deployments | later slice of PSG-42 | Not started; the chart runs only the API |
| Image digest pinning and Argo CD app for the chart | handoff (deployment-cicd) | `deploy/` is outside this wave's ownership |
| Alerts (action failures, PII decrypt errors, SCIM lag) | later slice of PSG-42 | Depends on the jobs above |
| Retention and legal holds | later slice of PSG-42 | Retention classes come from the wave-48 country packs |
