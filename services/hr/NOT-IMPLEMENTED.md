# NOT-IMPLEMENTED — services/hr (PSG-42, wave 42)

Slice 1 builds workers, employments, positions, position assignments, the
as-of manager query, the PII vault with audit, the outbox events, list and
detail reads for workers, employments and positions, and the HR web screens
for all of them. Everything
below is either a later slice of PSG-42 or a handoff to another wave or owner.
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
| 2 | Absence plans, accrual runs, balances, requests, approvals, calendars | later slice of PSG-42 | Accrual formula, carry-over, expiry and negative floor are implemented in the domain (BR-42.3, BR-42.4) with tests; persistence, scheduler `hr-accrue`, `hr-carryover-expiry`, endpoints and calendars are not |
| 2 | Time integration (wave 25 absence registrations) | handoff (wave 25) | Needs wave 25's time registration contract |
| 2 | Accrual balance events to ESS | later slice of PSG-42 | Depends on the balance projection |
| 3 | Benefit plans, options, eligibility (CEL), open enrollment, life events | later slice of PSG-42 | Not started; eligibility fixture (`hr-benefit-100`) comes with it |
| 3 | Employee self-service and manager self-service | later slice of PSG-42 (ESS) and handoff (shell) | See `apps/ess/README.md`; MSS needs the approval flows |
| 4 | Skills, certifications, goals, performance reviews | later slice of PSG-42 | Not started |
| 4 | Recruitment pipeline basics | later slice of PSG-42 | Not started (FR-42.21) |
| 5 | Consent records, retention policies per data class, country field slots | later slice of PSG-42 | Not started; retention purge job `hr-retention-purge` not built |
| 5 | PII vault: encrypted columns, masked reads, audited unmasked reads | **built** for national ID and bank account | `worker_pii` sealed with AES-256-GCM, masked by default, reveal needs `HcmPIIRead` and a reason, audit row in the same transaction. Date of birth and home address (FR-42.1) are not collected yet; they come with ESS personal data |
| 5 | Separate `hr_pii` schema, its DB role and grants | handoff (deployment-cicd) | Slice 1 keeps `worker_pii` in the `hr` schema. A separate schema and role need deployment grants (no BYPASSRLS for the app role) and a schema move in a later migration |
| 5 | Tenant PII key from Vault; key rotation job `hr-pii-rekey` | handoff (deployment-cicd) and later slice | Slice 1 reads `HR_PII_KEY` from a Kubernetes secret; Vault and External Secrets wiring and the re-encrypt job are not built |
| 6 | Courses, sessions, registrations, completion to skills and certifications | later slice of PSG-42 | Not started (FR-42.14, BR-42.9) |
| 6 | Questionnaire engine (design, schedules, answers, results) | later slice of PSG-42 | Not started (FR-42.15, BR-42.10) |
| 7 | Injury and illness register, OSHA 300/300A/301 extracts | later slice of PSG-42 | Not started (FR-42.16, BR-42.11); the form layouts and ITA submission are in the wave-48 US pack |
| 7 | Loaned items | later slice of PSG-42 | Not started (FR-42.17); the link to wave-44 assets is a handoff to wave 44 |
| 7 | Skill mapping and gap analysis | later slice of PSG-42 | Not started (FR-42.18) |
| 8 | Onboarding and offboarding checklists, worker tasks | later slice of PSG-42 | Not started (FR-42.19, BR-42.12) |
| 8 | HR reminders on the wave-5 alert rules | handoff (wave 5) | The reminder rule model needs the alert rule engine; not started |
| 8 | Requisitions, candidates, offers | later slice of PSG-42 | Not started (FR-42.21, BR-42.15) |
| 8 | Compensation planning cycles and worksheets | later slice of PSG-42 | Not started (FR-42.22, BR-42.13) |
| 8 | Concurrent and global assignments | **partly built** | Employment `assignmentKind` (PRIMARY, CONCURRENT, GLOBAL_HOST), the per-entity exclusion and GLOBAL_HOST validation are built. Per-employment pay groups for payroll (wave 43) and absence balances on PRIMARY only are not |

Deploy items: **HR workspace** is built as the web module
`apps/web/src/modules/hr` (workers, positions, worker detail with PII reveal,
employments and the as-of view; position detail with assignments). The org
chart, personnel actions, absence admin and the self-service screens are not. The **ESS
portal** is not built here (apps/ess README). **Demo workforce loaded** is not
built (needs the `hr-workforce-1200` generator under `testdata/`).

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC-42.1 as-of manager, 1,200 workers, 50,000 dates, p95 < 50 ms | partly built | The algorithm is checked against an independent model with 100-plus random hierarchies (`properties_test.go`). The database check at 1,200 workers and 50,000 dates and the p95 measurement are not built |
| AC-42.2 overlapping assignment rejected unless overfill | **built** (store test) | Overfill check is conservative: it sums every assignment that overlaps the new range, not per instant. The DB test needs `PASSAGE_TEST_DATABASE_URL` |
| AC-42.3 accrual on US-PTO-15 | partly built | Domain scenario and 11 proration cases match the balances exactly (`accrual_cases_test.go`). Persistence and the 36-case fixture set are not built |
| AC-42.4 negative balance beyond request | partly built | Domain `Consume` returns HCM-ABS-002 (`accrual_cases_test.go`). No request endpoint yet |
| AC-42.5 eligibility on 100 fixtures | later slice of PSG-42 | Eligibility (CEL) not started |
| AC-42.6 future-dated transfer on its date | later slice of PSG-42 | Personnel actions and the Temporal timer not started |
| AC-42.7 masked national ID and audited read | **built** (store and API tests) | DB test needs `PASSAGE_TEST_DATABASE_URL`; the no-DB API test covers the duty and reason gate |
| AC-42.8 SCIM user within 60 s | handoff (platform / Keycloak) and later slice | SCIM worker not built; needs Keycloak admin access and the personnel-action events |
| AC-42.9 course waitlist | later slice of PSG-42 | Courses not started |
| AC-42.10 anonymous questionnaire threshold | later slice of PSG-42 | Questionnaires not started |
| AC-42.11 OSHA 300 extract | later slice of PSG-42 | Incidents not started |
| AC-42.12 onboarding task date | later slice of PSG-42 | Checklists not started |
| AC-42.13 compensation budget block | later slice of PSG-42 | Cycles not started |
| AC-42.14 concurrent and global as-of with pay groups | partly built | Both employments appear in `as-of` (entities are separate rows). Payroll pay-group memberships are wave 43 |
| AC-42.15 loaned item blocks offboarding | later slice of PSG-42 | Loaned items and offboarding not started |
| AC-42.16 skill gap | later slice of PSG-42 | Skill profiles not started |

## Platform and API conventions not yet used

| Item | Status | Reason |
|---|---|---|
| Kernel `httpx` idempotency middleware (replay headers, 60 s lease, 24 h expiry) | handoff (platform) and later slice | Slice 1 stores the `Idempotency-Key` on the worker, employment, position and assignment rows (the org pattern): a replay with the same body returns the stored row, another body is `idempotency_key_reuse`, a concurrent duplicate is `idempotency_key_in_flight`. Keys do not expire and there is no replay header |
| `ETag` on reads and `If-Match` on PUT/PATCH/DELETE | later slice of PSG-42 | Slice 1 has no update endpoints; `ETag` is not set on reads yet, so the OpenAPI does not declare it |
| OPA decision and `legalEntityScopes` | handoff (platform / edge-identity) | Duties are checked locally by `authz.RequireDuty`; legal-entity scoping is not enforced yet |
| Effective-date granularity TIMESTAMP and tenant-local midnight | later slice of PSG-42 | Slice 1 uses UTC days (DAY granularity) |
| Future-dated actions (up to 365 days) and workflow approval | later slice of PSG-42 | Depends on personnel actions and wave-4 workflow |
| `party_person_id` checked against the wave-7 party service | handoff (wave 7 party) | Stored as a uuid; existence is not checked |
| Outbox events `position.changed`, `employment.changed` for terminations, personnel action events | later slice of PSG-42 | Slice 1 emits `worker.created`, `employment.changed` (action HIRE, with the exclusive end when a termination date is given) and `positionassignment.changed` (with `manager_position_id`). The PRD name `position.assignment.changed` is written as `positionassignment` to fit the kernel event-type rule (see asyncapi) |

## Reference fixtures (PRD 8.42.9)

| Fixture | Status | Reason |
|---|---|---|
| `hr-workforce-1200` (generated workforce, 3 entities, 5 years) | later slice of PSG-42 | Needs the workforce generator and the actions to replay history |
| `hr-accrual-cases` (36 hire-date × plan cases) | partly built | 11 proration cases and the US-PTO-15 scenario are inline in `accrual_cases_test.go`. The other cases come with the accrual store |
| `hr-benefit-100` | later slice of PSG-42 | Benefits not started |
| `hr-pii-contract` (role × field matrix) | later slice of PSG-42 | The API tests cover the duty gate for `HcmPIIRead` and the reason rule. The full role matrix needs the OPA role model (handoff, platform) |

## Operations

| Item | Status | Reason |
|---|---|---|
| Schedules `hr-apply-actions`, `hr-accrue`, `hr-cert-expiry`, `hr-reminders` | later slice of PSG-42 | Jobs depend on personnel actions, absence and reminders |
| Temporal workers (actions, SCIM) and the `passage-hr` worker deployments | later slice of PSG-42 | Not started; the chart runs only the API |
| Image digest pinning and Argo CD app for the chart | handoff (deployment-cicd) | `deploy/` is outside this wave's ownership |
| Alerts (action failures, PII decrypt errors, SCIM lag) | later slice of PSG-42 | Depends on the jobs above |
| Retention and legal holds | later slice of PSG-42 | Retention classes come from the wave-48 country packs |
