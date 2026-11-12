# services/hr — Passage HR (wave 42)

Human resources core: workers, employments, positions and position assignments
with effective dating, the as-of manager query, the audited PII vault, and
absence (plans, accrual, year end and carry-over, balances, requests and
approval), and benefits (plans with CEL eligibility, a workforce simulation,
life events and enrollments), and skills (catalog, worker ratings,
certificates with expiry, position skill profiles, gap analysis and skill
search), and courses (catalog, sessions with a waitlist, registrations with
manager approval, completion into skills and certificates). Built for PSG-42,
**slices 1 to 6**. The wave's remaining
deliverables are listed in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## What slice 1 does

- **Workers** (FR-42.1): EMPLOYEE or CONTRACTOR, one wave-7 person party,
  personnel number, legal and preferred name. National ID and bank account are
  sealed with AES-256-GCM (`internal/domain/pii.go`) in `worker_pii`. Reads return
  masked values (`***-**-6789`, `****5432`). The unmasked read needs `hr.pii.read`
  and a reason of at least 10 characters, and every read writes `audit_event`
  in the same transaction.
- **Employments** (FR-42.2, FR-42.23): validity is a `[from, to)` range. The
  termination date is the last day employed, so `validTo` is the day after it;
  a termination on the hire date is a one-day employment. `status` is derived
  from the range as-of the read (PENDING, ACTIVE, TERMINATED). One PRIMARY
  employment per worker per legal entity at any instant (exclusion
  constraint). CONCURRENT and GLOBAL_HOST are allowed. GLOBAL_HOST needs a home
  entity different from the employing entity and a planned end date.
- **Positions and assignments** (FR-42.3): FTE is a decimal string from 0.05 to
  2.00, default 1.00. Assignments cannot exceed FTE unless the position allows
  overfill. An assignment must lie inside the position's and the employment's
  validity, and the employment must be the worker's, in the position's legal
  entity. A position may report to another position that exists on its start
  date.
- **As-of** (FR-42.4, BR-42.1): the worker's employments, primary position and
  manager on a date. The manager is the incumbent of the reports-to position. If
  that position is vacant, the walk goes up to `HR_MANAGER_MAX_DEPTH` levels
  (default 10), then returns none. The range end is exclusive.
- **Reads**: worker list (search, keyset paging), worker employments, position
  list and position detail with assignments.
- **Events** via the outbox, in the same transaction as the write (see
  `events/asyncapi.yaml`).
- **Web**: `apps/web/src/modules/hr` has the screens for all of the above.

## What slice 2 does (absence)

- **Plans** (FR-42.6): unit DAYS or HOURS, frequency, rate, accrual day,
  proration, max balance, carry-over max and expiry months, plan-year start
  (MM-DD; 02-29 is refused), negative balance allowed, minimum service days.
  Omitted fields take the 8.42.13 defaults. All quantities are decimal strings
  (`shopspring/decimal`, `numeric` columns); a JSON number is refused.
- **Accrual** (BR-42.3, `POST /absence/accrue`): one whole accrual period of
  the plan (calendar month, 14 days, or the plan year; `PER_HOUR_WORKED` needs
  wave-25 time and is refused) whose accrual day has come. Every worker with a
  PRIMARY employment in the period (BR-42.14) accrues
  `rate × days employed ÷ days in period`, rounded half-up to 2 places, capped
  at max balance (the excess is discarded and logged). The amount is booked to
  the plan year of the accrual day. Each worker gets one accrual line per
  (plan, period) — the `hr-accrue` key in 8.42.16 — so a rerun credits only
  workers not yet accrued: a retry is safe, and an admin whose legal-entity
  scope hides some employments leaves them for a later run. Overlapping
  periods of one plan are refused by an exclusion constraint.
- **Year end and carry-over expiry** (BR-42.3): `POST /absence/plans/{id}/year-end`
  closes a plan year once it has ended: each worker's total carries into the
  next year's CARRY_OVER bucket up to carry-over max and the rest expires (a
  negative total carries as is). Accrual into a new plan year is refused
  until the previous one is closed, and a closed year takes no more
  requests, approvals or accrual (HCM-ABS-YEAR-CLOSED). `POST /absence/plans/{id}/carry-over-expiry`
  expires unused carry-over once `carryOverExpiryMonths` have passed since the
  year started (0 means it lasts the whole year). Both are keyed per (plan,
  plan year, worker).
- **Balances**: per worker, plan, plan year and bucket (CURRENT, CARRY_OVER);
  balance = accrued − used + adjusted − expired. The read takes `asOf` and
  returns the plan year that contains it.
- **Requests** (FR-42.7, BR-42.4, BR-42.5): submitted in one call (no DRAFT).
  The quantity must fit the span (one day, or 24 hours, per calendar day) and
  the plan-year balance plus the negative allowance of −(rate × 2)
  (HCM-ABS-002). A request may not cross a plan-year start. An overlap with
  an APPROVED request is refused (HCM-ABS-001), with a SUBMITTED one it warns
  (HCM-ABS-003). Approval rechecks both, spends CARRY_OVER first and writes
  `dev.passage.hr.absence.approved.v1` in the same transaction. A SUBMITTED
  request can be cancelled. Every balance change locks the worker row first,
  so concurrent approvals, accruals and year ends cannot overspend or lose
  an update.
- **Own data**: `hr.absence.self` alone reads and requests only for the worker whose
  sign-in user (`worker.userId`) the caller is; `hr.team.approve` or
  `hr.absence.admin` read anyone's balances and requests, and
  `hr.absence.admin` may request or cancel for anyone. An approver cannot
  decide their own request (8.42.17).

## What slice 3 does (benefits)

- **Plans** (FR-42.8): code, type (MEDICAL, DENTAL, VISION, LIFE,
  RETIREMENT_401K, HSA, FSA, OTHER), provider party, currency, eligibility
  rule, open enrollment window (inclusive dates), waiting days, pre-tax flag,
  payroll deduction code and 1–20 options (coverage level, employee and
  employer rate, PER_PAY_PERIOD or MONTHLY). Rates are decimal strings with at
  most the currency's minor-unit places (`money.MinorUnits`), stored as
  `numeric(20,6)`.
- **Eligibility** (BR-42.6, `internal/domain/cel.go`): a CEL subset over
  `employment_type`, `fte` (sum of the employment's position assignments on
  the date), `service_days` (from the hire date) and `legal_entity`, with
  `&& || ! == != < <= > >= in`, list literals, numbers, strings and booleans.
  The rule is compiled when the plan is saved, so a broken rule is a 400. A
  fact the worker lacks (`fte` without an assignment, `age`, `job.type`) makes
  the rule fail with a reason rather than guess. The waiting period is checked
  first. A worker with PRIMARY employments in several legal entities is
  eligible when one of them is.
- **Simulation** (`GET /benefits/plans/{id}/simulate?asOf=`, AC-42.5): the
  same evaluation for every worker with a PRIMARY employment on the date, one
  row per worker with the reason. Nothing is written. The `hr-benefit-100`
  fixture (`testdata/`) checks 100 employees against 6 plans with expected sets
  computed by an independent reference.
- **Life events** (8.42.5): MARRIAGE, BIRTH, ADOPTION, DIVORCE,
  LOSS_OF_COVERAGE, OTHER; each opens an enrollment window from its date to 30
  days after (`windowEnd`).
- **Enrollments**: one option of one plan from an effective date, inside the
  open window or a life event's window (HCM-BEN-001), for an eligible worker
  (HCM-BEN-002), with a dependent count. One enrollment per worker and plan at
  any instant (exclusion constraint, HCM-BEN-004). Ending takes the last
  covered day and a reason; a TERMINATION keeps coverage to the end of that
  month and marks a COBRA qualifying event on health plans. Both write
  `dev.passage.hr.benefitenrollment.changed.v1` (START or END, with the rates,
  currency, pre-tax flag and deduction code payroll needs) in the same
  transaction. Enrollment and ending lock the rows they change.
- **Own data**: `hr.benefits.self` alone enrolls, records life events and reads
  benefits only for the caller's own worker; `hr.worker.maintain` acts for
  anyone and `hr.worker.inquire` reads anyone's.

## What slice 4 does (skills)

- **Catalog** (FR-42.10): a skill is a code, a name and a rating scale 1 to
  `scaleMax` (1–10, default 5). Codes are unique per tenant; a replayed
  `Idempotency-Key` returns the stored skill.
- **Ratings**: one current rating per worker and skill, on the skill's scale
  (HCM-SKL-RATING), with the assessment date (not in the future). A new
  rating replaces the old one and writes `dev.passage.hr.workerskill.changed.v1`
  with the previous rating in the same transaction. An assessment dated
  before the stored one is HCM-SKL-STALE, so a late retry cannot overwrite a
  newer rating; the same rating and date again changes nothing and publishes
  nothing. Writes for one worker are serialised by a lock on the worker row.
- **Certificates** (BR-42.8): code, title, optional issuer, issue date and an
  expiry given as a date or as validity months (the same day that many months
  later, or the month's last day when it has no such day), or none. Valid on
  `[issuedOn, expiresOn)`: a certificate is no longer valid on its expiry day.
  Recording writes `dev.passage.hr.certification.issued.v1`. The same worker,
  code and issue date twice is HCM-CERT-DUPLICATE.
- **Certifications expiring** (8.42.14): certificates valid on `asOf` that
  expire within `withinDays` (default 60, at most 366), soonest first, with
  the holder and the days left. A certificate the worker has already renewed
  (a later one of the same code that lasts longer) is left out.
- **Position skill profile** (FR-42.18): a minimum rating per skill, set,
  changed or removed. Positions follow the legal-entity scope.
- **Gap analysis** (AC-42.16): every requirement next to the worker's rating
  (`comparison`) and the unmet ones (`gaps`) as (skill, required, actual,
  delta); a skill the worker is not rated on counts as 0.
- **Skill search** (FR-42.18): `skills=WELDING:3,PLC&minRating=2&certs=OSHA10&asOf=`
  finds the workers employed on the date who meet every rating and hold every
  certificate valid on the date, by personnel number, at most 500
  (`truncated` says when there were more). `availableFrom` is refused until
  availability exists.

## What slice 5 does (courses)

- **Catalog** (FR-42.14): a course is a code, a name, an optional group, the
  skill outcomes a PASS awards (catalog skills, each at a rating on its
  scale), an optional certificate (code and name) with validity months (0:
  does not expire), and whether registration needs manager approval. A
  replayed `Idempotency-Key` returns the stored course.
- **Sessions**: start and end, a location or a virtual link (or both), an
  internal instructor (a worker) or an external one (a party id), capacity
  (1–10,000), waitlist capacity (0–100, default 5, the 8.42.13 parameter
  default) and an optional cost per attendee in a currency (decimal at the
  currency's minor units). Reads carry the confirmed, waitlisted and
  requested counts and the seats left.
- **Registration** (BR-42.9): one per worker and session; registering again
  returns it. Seats go in registration order: CONFIRMED while a seat is free,
  WAITLISTED while the waitlist has room, else HCM-CRS-001. Registration and
  the seat count run under a lock on the session row, so concurrent
  registrations cannot overfill. A course that needs manager approval
  registers as REQUESTED with the HCM-CRS-002 warning and holds no seat;
  `hr.team.approve` approves it into a seat or the waitlist (not their own:
  HCM-CRS-SELF-APPROVAL). Registration closes when the session ends. An
  employee with only `hr.learning.self` registers and cancels only themselves.
- **Cancel**: cancels or declines; a canceled CONFIRMED seat promotes the
  oldest WAITLISTED registration. A worker who registers again after a
  cancel queues at the back.
- **Completion** (FR-42.14): from the session start, ATTENDED (optionally
  PASS or FAIL, with a score) or NO_SHOW per registered worker; an ATTENDED
  line may get its result later and the same outcome again changes nothing.
  A PASS raises each skill outcome to max(existing, course rating) (a higher
  rating keeps its assessment date; a raised one writes
  `workerskill.changed`), issues the certificate from the session end date
  for the validity months (`certification.issued`; a certificate the worker
  already holds for that code and day is reused), and writes
  `dev.passage.hr.course.completed.v1`, all in one transaction. Lines apply
  all or none.

## What slice 6 does (incidents, loans, checklists, questionnaires)

- **Incident register** (FR-42.16, BR-42.11): `hr.incident.report` files an incident; the
  case number is a per-tenant counter. `osha_recordable` defaults from the facts (death,
  days away, restricted days, medical treatment beyond first aid, loss of consciousness);
  overriding it either way needs `overrideReason` (HCM-INC-001). Description, witnesses,
  treatments, root cause and corrective action are one fieldcrypt-sealed value
  (`hr_incident.details`); reads by id open it and write an audit row; events carry no
  medical text. Status goes REPORTED, UNDER_REVIEW, CLOSED; the reporter cannot close
  (HCM-INC-SELF-CLOSE). A CLOSED incident is frozen by a trigger. A correction is a new
  incident with `supersedesId`: it needs `hr.incident.admin`, cannot be filed by the
  original's reporter or closer, and hides the original from the OSHA extract only once
  the correction is itself CLOSED (so a second administrator must close it). The override justification is sealed with the details, body parts are a fixed code list, the incident's legal entity must match the worker's employment on the incident date (HCM-INC-ENTITY), and events omit worker, type and outcome for a privacy case. Rows follow the
  legal-entity scope. The OSHA extract (`/incidents/osha-extract?year=&siteId=`) returns the
  300 log with 301 detail and the 300A summary; privacy cases show "Privacy case" for the
  name and no worker id, in lists too. The extract is audited as one export row.
- **Loaned items** (FR-42.17): issue, return or lose, per worker, and the outstanding
  inquiry. `assetId` is stored, not checked (wave 44).
- **Checklists** (FR-42.19, BR-42.12): templates with tasks (owner role, due offset);
  `POST /workers/{id}/checklists` instantiates one for an effective date (the call a
  personnel action will make): due = date + offset, MANAGER = the manager as-of the date,
  WORKER = the worker, HR_PARTNER and IT unassigned. OFFBOARDING adds RETURN_LOANED_ITEMS,
  which cannot be done or skipped while an item is OUT (HCM-LOAN-001), and the checklist
  cannot complete while one is OUT.
- **Questionnaires** (FR-42.15, BR-42.10): questions as one validated document (SINGLE,
  MULTI, SCALE, NUMERIC, TEXT, DATE; weights; result groups), schedules over a CEL
  population (same subset as benefits), answers by the signed-in worker only, scoring
  per result group (weighted mean, 2 decimals), results by question, result group and
  department. An anonymous session stores no worker id and a date, not a time; a separate
  marker stops a second answer. Anonymous rules: a schedule needs at least 5 invited; with
  fewer than 5 responses the overall figures are suppressed (counts only, `suppressed`);
  a department breakdown with any group under 5 is HCM-QST-002; late answers HCM-QST-001. Results of an anonymous questionnaire are released only after it closes (HCM-QST-NOT-CLOSED), and its marker and session are written in separate transactions with a random v4 session id and a date only. Loans, checklists, tasks and answers carry the worker's legal entity and follow the legal-entity scope like incidents.

Domain rules that are not yet exposed by an endpoint, but are implemented and
tested: hierarchy cycle detection (BR-42.7) and the reveal gate (`CheckReveal`).

## What slice 7 does (job skill gaps, goals, reviews)

Job skill profiles: `job_skill_requirement` holds the minimum rating per skill
for a job, like a position's profile. `GET /v1/hr/jobs/{id}/skill-gaps?asOf=`
lists, for every worker whose primary position on the date carries the job's
code (at most 500, `truncated` says more exist), only the unmet requirements;
`GET /v1/hr/workers/{id}/skill-gap?jobId=` does the same for one worker. A
skill the worker is not rated on counts as 0. Positions the caller's
legal-entity scope hides do not contribute workers.

Goals (FR-42.11): a manager (or `hr.performance.admin`) adds a goal for a worker
(title, description, due date) and sets its status (OPEN, ACHIEVED, CANCELLED)
and progress percent; ACHIEVED without a percent sets 100. The worker reads
their goals at `/v1/hr/me/goals`. A goal or review carries the legal entity of
the worker's employment: pass `legalEntityId` when the worker has several
employments (else 422 `HCM-ENTITY-REQUIRED`); a worker with no visible employment
is refused the same way, so no row has a NULL entity. Nobody sets or changes
their own goals (403 `HCM-GOAL-SELF`), but the worker reports progress on their
own goal with `POST /v1/hr/me/goals/{id}/progress` (`hr.performance.self`): status
OPEN or ACHIEVED only, never CANCELLED, and a goal of anyone else is the same 404
as a missing one. ACHIEVED needs 100 percent and a cancelled goal is final
(`HCM-GOAL-STATE`).

Reviews (FR-42.11): one two-step template, no calibration and no rating
distribution. A manager opens a review for a worker and period (`draft`; one per
worker and period). The reviewed worker writes the self-assessment
(achievements, development) at `/v1/hr/me/reviews/{id}/self-assessment`
(`self_submitted`); the manager then signs off with comments and an optional
overall rating 1-5 (`signed_off`). Repeating a step with the same content
changes nothing; out-of-order steps and changed text are HCM-REV-STATE; nobody
signs off their own review (HCM-REV-SELF). Every step publishes
`review.changed` with ids, period and status only. A manager acts on their own
record and their direct reports; `hr.performance.admin` acts on any worker the
legal-entity scope allows. Goals and reviews carry the legal entity of the
worker's employment and follow the caller's scope. "Direct report" is read at
request time (today's reporting line), not as of the review period, so a manager
who has since moved teams loses access to a past period's review. A review is
unique per worker and period across legal entities: opening it again from a
scope that cannot see the existing one is 409 `HCM-REV-STATE`, and naming a
different `legalEntityId` than the existing review's is 422 `HCM-ENTITY-MISMATCH`.

## PHI read auditing (#606)

Health-plan elections (MEDICAL, DENTAL, VISION, HSA, FSA enrollments) and benefit life events on `GET /v1/hr/workers/{id}/benefits` are protected health information (`docs/ops/phi-field-inventory.md`). With read auditing on, a response that carries them is recorded first with `platform/go/phiaudit`: who (the verified caller, taken by the audit service from the service token), which record (`hr.worker.benefits`, the worker id) and fields, when, and `purpose` (`?purpose=`, at most 500 characters; required, except that a worker reading their own record defaults to `self-service`). A response with no phi fields (no health-plan enrollment and no life event) records nothing. If the record cannot be written, the data is not returned (503 `HCM-PHI-AUDIT-UNAVAILABLE`). Metric: `hr_phi_reads_recorded_total`.

**Break-glass.** `hr.benefits.break-glass` is emergency read access, for a role kept unassigned until it is needed. It opens `GET /v1/hr/workers/{id}/benefits`; a caller without `hr.worker.inquire` who holds it reads another worker's benefits only with a purpose carrying the `BREAK-GLASS` reason code (`?purpose=BREAK-GLASS: member in ER, HR closed`), and gets nothing beyond their own record otherwise. The read is always recorded, even when the record holds no health-plan election or life event, and the audit service queues it for a second person's review (`GET /v1/audit/reads:break-glass`, `POST /v1/audit/reads/{id}:review`, see `services/audit`). With read auditing off, break-glass reads are refused (403 `HCM-BREAK-GLASS-UNAUDITED`) so emergency access is never unlogged. The reason code is reserved: a caller whose scope it would not widen (no break-glass duty, or `hr.worker.inquire` already) gets 400 COM-0001, so the review queue holds only real emergency access.

## Self-service profile changes (#267)

An employee (`hr.profile.self`) changes their own record through
`/v1/hr/me/profile-changes`; the worker is always the one whose `user_id` is
the caller's sign-in. A preferred name applies at once (status `APPLIED`). A
home address or bank account is sensitive: the new value is sealed on the
`hr_profile_change` row under its own fieldcrypt field and waits (`PENDING`),
one pending change per kind (`HCM-ESS-003`). Responses and queues show only a
masked summary (the address's city and country, the account's last four).
The employee can withdraw a pending change.

A manager (`hr.team.approve`) decides their direct reports' address changes
through `/v1/hr/me/team/profile-changes`; anyone else's change is the uniform
404 and a bank account change is `HCM-ESS-005` (HR decides it, since it routes
pay). HR (`hr.worker.maintain`) sees and decides every change through
`/v1/hr/profile-changes`. Nobody decides a change to their own record
(`HCM-ESS-006`). Approval re-seals the value into `worker_pii`
(`home_address_sealed` or `bank_account_sealed`) with a `p3_change` audit row
in the same transaction; any decision or withdrawal drops the sealed copy on
the change row. The home address is part of the PII view: city and country
when masked, the full line on an audited reveal. `hr reseal-pii` re-seals it
with the other vault fields.

## Run

Configuration is read from the environment and validated at startup:

| Variable | Required | Meaning |
|---|---|---|
| `DATABASE_URL` | yes | Postgres as `passage_app` (no BYPASSRLS) |
| `FIELDCRYPT_KEYS` / `FIELDCRYPT_KEYS_FILE`, `FIELDCRYPT_CURRENT_KEY_ID` | yes | `id=base64(32 bytes)` pairs and the key id new P3 values use (platform `fieldcrypt`); the service refuses to start without keys. The chart mounts the keys as `FIELDCRYPT_KEYS_FILE` |
| `HR_PII_KEY` | no | legacy base64 32-byte key; only opens P3 values written before fieldcrypt. Keep it until `hr reseal-pii` reports `rewritten=0` for every tenant |
| `HR_RETENTION_PURGE_ENABLED` | no | kill switch for `POST /v1/hr/candidates/purge` and `hr retention-purge`: `false` turns both off (503 `HCM-RET-DISABLED`); anything else leaves them on |
| `COMPLIANCE_URL` | for the purge | compliance base URL; the purge asks `GET /v1/privacy/holds:check` per candidate (signed, `hr` to `compliance` grant) and skips held ones. Unset: the route answers 503 `HCM-RET-HOLDS` and erases nothing |
| `PASSAGE_PHI_READ_AUDIT` | no | records every read that returns protected health information (health-plan elections and life events on `GET /v1/hr/workers/{id}/benefits`) in the audit service before the data is returned (#606). Empty or `auto` follows `PASSAGE_CLOUD_PROFILE` (on when it includes the `hipaa` overlay); `true` forces it on, `false` off. Chart: `config.phiReadAudit` |
| `AUDIT_URL` | when auditing is on | audit base URL; the read is posted to `POST /v1/audit/reads` (signed, `hr` to `audit` grant for `audit.read-log.write`). With auditing on, the service refuses to start without `AUDIT_URL` and a service signing key. Chart: `auditUrl` |
| `HR_MANAGER_MAX_DEPTH` | no | vacant-manager walk depth, 1–20, default 10 |
| `MIGRATE_ON_START` | no | `true` applies migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | no | default `:8080` and `:9090` (`/metrics`) |

```sh
cd services/hr
go run ./cmd/hr
```

Health: `GET /healthz` (liveness), `GET /readyz` (Postgres ping, 500 ms).

### Re-sealing P3 values (`hr reseal-pii`)

`hr reseal-pii <tenant-id>...` runs with the service's own environment (same
`DATABASE_URL`, fieldcrypt keys and optional `HR_PII_KEY`) and, per tenant,
re-seals every `worker_pii` value that opens only through `HR_PII_KEY` or names
an older fieldcrypt key id under `FIELDCRYPT_CURRENT_KEY_ID`. It walks rows in
batches of 500, one transaction each, writes a `p3_change` audit row per
rewritten worker (actor `hr-reseal-pii`, value unchanged) and logs
`rewritten=<n>` per tenant. It is safe to re-run; a value that opens under
neither key stops the run and names the worker. Migration `0007` grants
`passage_app` UPDATE on the two sealed columns only.

```sh
go run ./cmd/hr reseal-pii <tenant-id> [<tenant-id>...]   # in a cluster: the hr binary with the pod's env
```

Use it after a fieldcrypt rotation (keep the old key configured until it reports
`rewritten=0`) and to retire `HR_PII_KEY`: once every tenant reports
`rewritten=0` and `hr_pii_legacy_opens_total` stays 0, remove `HR_PII_KEY`.

The chart can run it on a schedule: set `reseal.enabled=true`, `reseal.tenantIds`
(the app role cannot list tenants) and optionally `reseal.schedule` (default
`17 3 * * *`). The CronJob (`concurrencyPolicy: Forbid`) runs
`hr reseal-pii <tenant-id>...` with the same secret and the fieldcrypt key file
the Deployment mounts. Dropping `HR_PII_KEY` from the secret is a manual step
after the logs show `rewritten=0` for every tenant (see NOT-IMPLEMENTED.md).

### Scheduled jobs

`hr accrue`, `hr carryover-expiry` and `hr retention-purge` each take tenant ids,
run as an all-entity service principal (`system:hr-accrue`,
`system:hr-carryover-expiry`, `system:hr-retention-purge`), log one line per
tenant and exit 1 if any tenant failed. The chart runs each as a CronJob
(`accrue`, `carryoverExpiry`, `retentionPurge`: `enabled`, `tenantIds`,
`schedule`; off by default). `retentionPurge` also needs
`serviceIdentity.signingKeySecret` and compliance's `serviceIdentity.publicKeys`
to list `hr`.

Opens are observable on `/metrics`: `hr_pii_legacy_opens_total` counts values
that needed `HR_PII_KEY`, `hr_pii_open_failures_total` values that opened under
neither key. Each is also logged (WARN / ERROR) with the tenant, field, the key id
the envelope names and which path failed, never the value.

## Test

```sh
cd services/hr
go vet ./... && go test ./...
```

- `internal/domain`: table-driven and `rapid` property tests. They include the
  as-of manager against an independent reference model, cycle detection on a
  chain, FTE without overfill, the accrual ledger cap, the proration table,
  the BR-42.3 `US-PTO-15` scenario including the year end and carry-over
  expiry, accrual period shapes, plan-year starts, overlap and request state
  rules, and a property that the year end splits the total exactly into
  carried and expired. They run without a database.
- `internal/api`: no-database tests for duties, validation, the reveal gate and
  reason, the 404/401 paths and metrics.
- `internal/api`: also checks that `api/openapi.yaml` documents exactly the
  mounted routes and that each route enforces its documented duty.
- `internal/store`: integration tests for PII masking and audit, as-of across a
  vacancy, FTE overfill, primary-employment overlap and concurrent employment,
  the inclusive termination date, assignment range and entity checks, the
  outbox payloads, idempotent replay and key reuse, list paging, search and
  tenant separation, and absence: AC-42.3 end to end through accrual, approval,
  year end and expiry; per-worker accrual keys, overlapping and early periods;
  AC-42.4 and the overlap, span, plan-year and cancel rules with the approval
  event; own-data and self-approval refusals; and two concurrent approvals
  that cannot overspend one balance. They use `testkit.DB(t)` and **skip unless
  `PASSAGE_TEST_DATABASE_URL` is set**. `TestRowLevelSecurity` also needs
  `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS) and checks every
  table, including the legal-entity scope on employments and positions and a
  scoped accrual run followed by an all-entity rerun. Benefits: plan replay
  and duplicates, the window, life-event, eligibility, option and overlap
  rules with the outbox events, the end rules, the simulation agreeing with
  enrollment, and own-data refusals. Skills: catalog replay, duplicates and
  paging; rating scale, replace, stale and no-op rules with the event;
  AC-42.16 through the database; certificate replay, expiry and validity; the
  expiring inquiry skipping renewed certificates and paging; skill search by
  ratings, certificates and employment date. Courses: AC-42.9 end to end
  (10 + 2 of 13, promotion on cancel, FORKLIFT 3 and a 24-month certificate
  with the three events, a no-op replay, all-or-nothing lines, closed
  registration), ratings never lowered and results recorded after
  attendance, approval and self-approval, own-data refusals, catalog and
  session replay, and 16 concurrent registrations that cannot overfill.
  `grants_test.go` runs every skills and courses path as `passage_app`.

To run the store tests:

```sh
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...
```

## Endpoints

Specification: [api/openapi.yaml](api/openapi.yaml). Duties are checked by the
kernel `authz.RequireDuty` (`x-passage-duty` in the spec).

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/hr/workers` | hr.worker.maintain |
| GET | `/v1/hr/workers?q=&limit=&cursor=` | hr.worker.inquire |
| GET | `/v1/hr/workers/{id}` | hr.worker.inquire |
| GET | `/v1/hr/workers/{id}/employments` | hr.worker.inquire or hr.action.maintain |
| GET | `/v1/hr/employments/{id}` (the employment lookup) | hr.worker.inquire or hr.action.maintain |
| GET | `/v1/hr/workers/{id}/as-of?date=` | hr.worker.inquire |
| GET | `/v1/hr/workers/{id}/pii?reason=` | hr.pii.read |
| POST | `/v1/hr/workers/{id}/employments` | hr.worker.maintain |
| POST | `/v1/hr/positions` | hr.org.maintain |
| GET | `/v1/hr/positions?limit=&cursor=` | hr.org.inquire |
| GET | `/v1/hr/positions/{id}` | hr.org.inquire |
| POST | `/v1/hr/positions/{id}/assignments` | hr.action.maintain |
| POST | `/v1/hr/absence/plans` | hr.absence.admin |
| GET | `/v1/hr/absence/plans?limit=&cursor=` | hr.absence.self |
| GET | `/v1/hr/absence/plans/{id}` | hr.absence.admin |
| POST | `/v1/hr/absence/plans/{id}/year-end` | hr.absence.admin |
| POST | `/v1/hr/absence/plans/{id}/carry-over-expiry` | hr.absence.admin |
| POST | `/v1/hr/absence/accrue` | hr.absence.admin |
| POST | `/v1/hr/absence/requests` | hr.absence.self (own worker unless hr.absence.admin) |
| GET | `/v1/hr/absence/requests?status=&workerId=&limit=&cursor=` | hr.absence.self (own unless hr.team.approve or hr.absence.admin) |
| POST | `/v1/hr/absence/requests/{id}/decide` | hr.team.approve |
| POST | `/v1/hr/absence/requests/{id}/cancel` | hr.absence.self (own unless hr.absence.admin) |
| GET | `/v1/hr/workers/{id}/absence/balances?asOf=` | hr.absence.self (own unless hr.team.approve or hr.absence.admin) |
| POST | `/v1/hr/benefits/plans` | hr.setup.admin |
| GET | `/v1/hr/benefits/plans?limit=&cursor=` | hr.benefits.self |
| GET | `/v1/hr/benefits/plans/{id}` | hr.benefits.self |
| GET | `/v1/hr/benefits/plans/{id}/simulate?asOf=` | hr.setup.admin |
| POST | `/v1/hr/benefits/life-events` | hr.benefits.self (own worker unless hr.worker.maintain) |
| POST | `/v1/hr/benefits/enrollments` | hr.benefits.self (own worker unless hr.worker.maintain) |
| POST | `/v1/hr/benefits/enrollments/{id}/end` | hr.worker.maintain |
| GET | `/v1/hr/workers/{id}/benefits` | hr.benefits.self (own unless hr.worker.inquire or hr.worker.maintain) |
| POST | `/v1/hr/skills` | hr.setup.admin |
| GET | `/v1/hr/skills?limit=&cursor=` | hr.worker.inquire |
| GET | `/v1/hr/skills/search?skills=&minRating=&certs=&asOf=` | hr.org.inquire |
| POST | `/v1/hr/workers/{id}/skills` | hr.worker.maintain |
| GET | `/v1/hr/workers/{id}/skills` | hr.worker.inquire |
| POST | `/v1/hr/workers/{id}/certifications` | hr.worker.maintain |
| GET | `/v1/hr/workers/{id}/certifications?asOf=` | hr.worker.inquire |
| GET | `/v1/hr/certifications/expiring?withinDays=&asOf=&limit=&cursor=` | hr.worker.inquire |
| GET | `/v1/hr/workers/{id}/skill-gap?positionId=` or `?jobId=` (exactly one) | hr.org.inquire |
| GET | `/v1/hr/positions/{id}/skill-requirements` | hr.org.inquire |
| POST | `/v1/hr/positions/{id}/skill-requirements` | hr.org.maintain |
| DELETE | `/v1/hr/positions/{id}/skill-requirements/{skillCode}` | hr.org.maintain |
| GET | `/v1/hr/jobs/{id}/skill-requirements` | hr.org.inquire |
| POST | `/v1/hr/jobs/{id}/skill-requirements` | hr.org.maintain |
| DELETE | `/v1/hr/jobs/{id}/skill-requirements/{skillCode}` | hr.org.maintain |
| GET | `/v1/hr/jobs/{id}/skill-gaps?asOf=` | hr.org.inquire |
| POST / GET | `/v1/hr/workers/{id}/goals` | hr.performance.manage (or .admin) |
| POST | `/v1/hr/goals/{id}/progress` | hr.performance.manage (or .admin) |
| POST / GET | `/v1/hr/workers/{id}/reviews` | hr.performance.manage (or .admin) |
| GET | `/v1/hr/reviews/{id}` | hr.performance.manage (or .admin) |
| POST | `/v1/hr/reviews/{id}/sign-off` | hr.performance.manage (or .admin) |
| GET | `/v1/hr/me/goals`, `/v1/hr/me/reviews` | hr.performance.self |
| POST | `/v1/hr/me/goals/{id}/progress` | hr.performance.self |
| POST | `/v1/hr/me/reviews/{id}/self-assessment` | hr.performance.self |
| POST | `/v1/hr/courses` | hr.learning.admin |
| GET | `/v1/hr/courses?limit=&cursor=` | hr.learning.self |
| GET | `/v1/hr/courses/{id}` | hr.learning.self |
| POST | `/v1/hr/courses/{id}/sessions` | hr.learning.admin |
| GET | `/v1/hr/courses/{id}/sessions` | hr.learning.self |
| GET | `/v1/hr/courses/sessions/{id}` | hr.learning.self |
| GET | `/v1/hr/courses/sessions/{id}/registrations` | hr.learning.admin |
| POST | `/v1/hr/courses/sessions/{id}/registrations` | hr.learning.self (own worker unless hr.learning.admin or hr.team.approve) |
| POST | `/v1/hr/courses/sessions/{id}/complete` | hr.learning.admin |
| POST | `/v1/hr/courses/registrations/{id}/approve` | hr.team.approve |
| POST | `/v1/hr/courses/registrations/{id}/cancel` | hr.learning.self (own unless hr.learning.admin or hr.team.approve) |
| POST | `/v1/hr/incidents` | hr.incident.report (hr.incident.admin to correct) |
| GET | `/v1/hr/incidents?status=&limit=&cursor=` | hr.incident.admin |
| GET | `/v1/hr/incidents/osha-extract?year=&siteId=` | hr.incident.admin |
| GET | `/v1/hr/incidents/{id}` | hr.incident.admin |
| POST | `/v1/hr/incidents/{id}/status` | hr.incident.admin |
| POST | `/v1/hr/workers/{id}/loaned-items` | hr.worker.maintain |
| GET | `/v1/hr/workers/{id}/loaned-items` | hr.worker.inquire |
| GET | `/v1/hr/loaned-items/outstanding?dueBy=` | hr.worker.inquire |
| POST | `/v1/hr/loaned-items/{id}/close` | hr.worker.maintain |
| POST / GET | `/v1/hr/checklist-templates` | hr.setup.admin (GET also hr.action.maintain, so the person starting a checklist can pick the template) |
| POST / GET | `/v1/hr/workers/{id}/checklists` | hr.action.maintain / hr.worker.inquire |
| GET | `/v1/hr/checklists/{id}` | hr.worker.inquire |
| POST | `/v1/hr/checklists/{id}/complete` | hr.action.maintain |
| GET | `/v1/hr/tasks?ownerWorkerId=&status=` | hr.worker.inquire |
| POST | `/v1/hr/tasks/{id}/finish` | hr.action.maintain |
| POST / GET | `/v1/hr/questionnaires` | hr.questionnaire.admin |
| GET | `/v1/hr/questionnaires/{id}` | hr.questionnaire.admin |
| POST | `/v1/hr/questionnaires/schedules` | hr.questionnaire.admin |
| GET | `/v1/hr/questionnaires/schedules` (every schedule with its questionnaire's name) | hr.questionnaire.admin |
| GET | `/v1/hr/questionnaires/schedules/open` | hr.questionnaire.self |
| GET | `/v1/hr/questionnaires/schedules/{id}` | hr.questionnaire.self or hr.questionnaire.admin |
| POST | `/v1/hr/questionnaires/schedules/{id}/answers` | hr.questionnaire.self (own worker only) |
| GET | `/v1/hr/questionnaires/schedules/{id}/results?by=department` | hr.questionnaire.admin |
| POST / GET | `/v1/hr/me/profile-changes` | hr.profile.self (own worker only) |
| POST | `/v1/hr/me/profile-changes/{id}/cancel` | hr.profile.self (own worker only) |
| GET | `/v1/hr/me/team/profile-changes?status=&workerId=` | hr.team.approve (direct reports' address changes) |
| POST | `/v1/hr/me/team/profile-changes/{id}/decide` | hr.team.approve (direct reports' address changes) |
| GET | `/v1/hr/profile-changes?status=&workerId=` | hr.worker.maintain |
| POST | `/v1/hr/profile-changes/{id}/decide` | hr.worker.maintain |

## Error codes

Responses are RFC 9457 problems (`application/problem+json`) with `code`. The
type is `https://passage.dev/problems/<code>`.

| Code | Where | Meaning |
|---|---|---|
| `COM-0001` | 400 | Request failed validation; `errors` names each field |
| `HCM-PII-001` | 422 | Reveal reason missing or shorter than 10 characters |
| `HCM-EMP-001` | 409 | Worker already has an ACTIVE PRIMARY employment in that entity on that date |
| `HCM-EMP-002` | 422 | Termination date is before the hire date |
| `HCM-POS-001` | 422 | Position FTE would be exceeded (overfill not allowed) |
| `HCM-WRK-NOT-FOUND` | 404 | Worker does not exist for this tenant |
| `HCM-WRK-DUPLICATE` | 409 | Personnel number already used in this tenant |
| `HCM-POS-NOT-FOUND` | 404 | Position does not exist for this tenant |
| `HCM-POS-DUPLICATE` | 409 | Position number already used in this tenant |
| `HCM-POS-PARENT-INVALID` | 422 | Reports-to position does not exist on the new position's start date |
| `HCM-ASG-001` | 409 | Assignment overlaps a primary assignment of the same worker |
| `HCM-ASG-002` | 422 | Employment does not belong to the worker, or is in another legal entity than the position |
| `HCM-ASG-003` | 422 | Assignment dates are outside the position's or the employment's validity |
| `HCM-ABS-001` | 422 | Request overlaps an approved absence (at submission or approval) |
| `HCM-ABS-002` | 422 | Requested quantity exceeds the balance plus the negative allowance |
| `HCM-ABS-003` | warning | Request overlaps a submitted request; returned in `warnings` with a 201 |
| `HCM-ABS-PLAN-NOT-FOUND` | 404 | Absence plan does not exist for this tenant |
| `HCM-ABS-PLAN-DUPLICATE` | 409 | Absence plan code already used in this tenant |
| `HCM-ABS-REQ-NOT-FOUND` | 404 | Absence request does not exist for this tenant |
| `HCM-ABS-PERIOD` | 422 | Not one accrual period of the plan, not a plan-year start, or a `PER_HOUR_WORKED` plan |
| `HCM-ABS-NOT-DUE` | 422 | The period's accrual day, the plan year's end or the carry-over expiry has not come yet |
| `HCM-ABS-RUN-OVERLAP` | 409 | Another accrual period of the plan already covers some of these days |
| `HCM-ABS-YEAR-OPEN` | 409 | The previous plan year has balances that are not closed yet |
| `HCM-ABS-YEAR-CLOSED` | 409 | The plan year has had its year end; requests, approvals and accrual into it are refused |
| `HCM-ABS-NO-EXPIRY` | 422 | The plan's carry-over lasts the whole year (0 expiry months) |
| `HCM-ABS-YEAR-SPAN` | 422 | The request crosses a plan-year start; split it |
| `HCM-ABS-STATE` | 422 | The request's status does not allow this decision or cancellation |
| `HCM-ABS-SELF-APPROVAL` | 403 | An approver tried to decide their own request |
| `HCM-ABS-NOT-OWN` | 403 | An employee-only caller acted on another worker's absence |
| `HCM-CMP-010` | 409 | The compensation cycle is applied before an `hr.compensation.approve` user approved it |
| `HCM-CMP-SOD` | 403 | The cycle approver created the cycle, submitted one of its worksheets, or is the worker or proposing manager on a submitted row |
| `HCM-OFR-004` | 422 | A hire from an offer has neither `workerId` nor `newWorker`, or the candidate was erased and `newWorker.legalName` is missing |
| `HCM-BEN-001` | 422 | Enrollment outside the open window or the life event's 30 days |
| `HCM-PHI-AUDIT-UNAVAILABLE` | 503 | PHI read auditing is on and the read could not be recorded in the audit service; the data is not returned |
| `HCM-BREAK-GLASS-UNAUDITED` | 403 | a break-glass read (purpose `BREAK-GLASS`, duty `hr.benefits.break-glass`) while PHI read auditing is off |
| `HCM-BEN-002` | 422 | Worker not eligible for the plan on the effective date; the detail gives the reason |
| `HCM-BEN-003` | 422 | The option is not offered by the plan |
| `HCM-BEN-004` | 409 | The worker already has an enrollment in the plan that overlaps the effective date |
| `HCM-BEN-005` | 422 | The last covered day is before the enrollment start |
| `HCM-BEN-ENDED` | 409 | The enrollment has already ended with another date or reason |
| `HCM-BEN-PLAN-NOT-FOUND` | 404 | Benefit plan does not exist for this tenant |
| `HCM-BEN-PLAN-DUPLICATE` | 409 | Benefit plan code already used in this tenant |
| `HCM-BEN-ENR-NOT-FOUND` | 404 | Benefit enrollment does not exist for this tenant |
| `HCM-BEN-LE-NOT-FOUND` | 422 | The life event does not exist or is another worker's |
| `HCM-BEN-NOT-OWN` | 403 | An employee-only caller acted on another worker's benefits |
| `HCM-SKL-RATING` | 422 | Rating or minimum outside the skill's scale |
| `HCM-SKL-NOT-FOUND` | 404 | No skill with that code in the catalog |
| `HCM-SKL-DUPLICATE` | 409 | Skill code already used in this tenant |
| `HCM-SKL-STALE` | 409 | The worker has a newer assessment of the skill |
| `HCM-SKL-REQ-NOT-FOUND` | 404 | The position or job does not require that skill |
| `HCM-GOAL-NOT-FOUND` / `HCM-REV-NOT-FOUND` | 404 | Unknown goal or review, or one outside the caller's team or legal-entity scope |
| `HCM-REV-STATE` | 409 | Review step out of order (sign-off before the self-assessment, or a changed text after submit) |
| `HCM-GOAL-STATE` / `HCM-GOAL-SELF` | 409 or 422 / 403 | Achieved below 100 percent or a change to a cancelled goal / setting your own goal |
| `HCM-REV-SELF` | 403 | A worker cannot sign off their own review |
| `HCM-CERT-DUPLICATE` | 409 | The worker already has a certificate with that code and issue date |
| `HCM-CRS-001` | 422 | The session and its waitlist are full |
| `HCM-CRS-002` | warning | The course needs manager approval; returned in `warnings` with the REQUESTED registration |
| `HCM-CRS-STATE` | 409 | The session has ended (registration) or not started (completion), or the registration's status does not allow the step |
| `HCM-CRS-SELF-APPROVAL` | 403 | An approver tried to approve their own registration |
| `HCM-CRS-NOT-OWN` | 403 | An employee-only caller acted on another worker's registration |
| `HCM-CRS-NOT-FOUND` | 404 | Course does not exist for this tenant |
| `HCM-CRS-DUPLICATE` | 409 | Course code already used in this tenant |
| `HCM-CRS-SESSION-NOT-FOUND` | 404 | Session does not exist for this tenant |
| `HCM-CRS-REG-NOT-FOUND` | 404 | Registration does not exist, or the worker is not registered for the session |
| `HCM-INC-001` | 422 | Overriding the recordable default needs a justification |
| `HCM-INC-STATE` | 422 | Wrong status step, or correcting an incident that is not CLOSED |
| `HCM-INC-SELF-CLOSE` | 403 | The reporter tried to close their own incident |
| `HCM-INC-CORRECTION-DUTY` | 403 | A correction needs hr.incident.admin |
| `HCM-INC-SELF-CORRECT` | 403 | The reporter or closer tried to correct their own incident |
| `HCM-INC-ENTITY` | 422 | The worker had no employment in that legal entity on the incident date |
| `HCM-ENTITY-REQUIRED` / `HCM-ENTITY-MISMATCH` | 422 | A loan or checklist for a worker employed in several entities needs a valid `legalEntityId` |
| `HCM-QST-NOT-CLOSED` | 409 | Anonymous results are released after the schedule closes |
| `HCM-INC-CORRECTED` | 409 | The incident already has a correction |
| `HCM-LOAN-001` | 422 | Items are still out for the worker (task or checklist) |
| `HCM-LOAN-STATE` / `HCM-LOAN-DATE` | 409 / 422 | Item already closed another way / dates out of order |
| `HCM-CHK-OPEN-TASKS`, `HCM-CHK-STATE`, `HCM-CHK-EVIDENCE` | 422 / 409 / 422 | Checklist and task rules |
| `HCM-QST-001` | 422 | Questionnaire closed |
| `HCM-QST-002` | 422 | Anonymous breakdown or population under 5 |
| `HCM-QST-NOT-OPEN`, `HCM-QST-NOT-OWN`, `HCM-QST-NOT-INVITED`, `HCM-QST-ANSWERED` | 422 / 403 / 403 / 409 | Answering rules |
| `HCM-ESS-001`, `HCM-ESS-002` | 404 / 409 | No worker, or more than one, is linked to the caller's sign-in |
| `HCM-ESS-003` | 409 | A profile change of that kind is already pending |
| `HCM-ESS-004` | 409 | The profile change was already decided or withdrawn the other way |
| `HCM-ESS-005` | 403 | A manager tried to decide a bank account change |
| `HCM-ESS-006` | 403 | An approver tried to decide a change to their own record |
| `HCM-ESS-NOT-FOUND` | 404 | Profile change does not exist, or is outside the caller's team or own record |
| `idempotency_key_reuse` | 422 | Same key used with a different request body |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry |
| `AUTH-0003` | 403 | Caller lacks the duty (kernel `authz`) |
| `AUTH-0001` | 401 | No or malformed tenant on the request (kernel) |

The other catalog codes in PRD 8.42.18 belong to later slices.

## Layout

```
cmd/hr/            entrypoint: config, dbx.Connect, api.Register, httpx.Serve
internal/domain/   pure rules: effective dating, hierarchy, accrual, PII sealing, benefit eligibility (CEL subset)
internal/store/    pgx repositories, RLS-scoped transactions, outbox writes
internal/api/      HTTP handlers, DTOs, validation, duties
internal/events/   event type constants and payloads
migrations/        0001_hr_core.sql, 0002_absence.sql, 0003_benefits.sql, 0004_grants.sql, 0005_skills.sql, 0006_courses.sql, 0007_pii_reseal_grant.sql, 0008_incidents_loans_checklists.sql, 0009_questionnaires.sql, 0010_jobs_compensation_actions.sql, 0011_recruitment_comp_cycles.sql, 0012_performance.sql, 0013_comp_cycle_approval.sql (applied in order by dbx.Migrate)
testdata/          hr-benefit-100.json (AC-42.5 fixture)
api/openapi.yaml   OpenAPI 3.1 for every endpoint above
events/asyncapi.yaml  AsyncAPI 3 for the eight events published
charts/hr/         Helm chart passage-hr (lint: helm lint charts/hr)
```

## Dependencies

Only the platform module and what it already uses (chi, pgx/v5,
shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
