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
  masked values (`***-**-6789`, `****5432`). The unmasked read needs `HcmPIIRead`
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
- **Own data**: `EssAbsence` alone reads and requests only for the worker whose
  sign-in user (`worker.userId`) the caller is; `MssApprove` or
  `HcmAbsenceAdmin` read anyone's balances and requests, and
  `HcmAbsenceAdmin` may request or cancel for anyone. An approver cannot
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
- **Own data**: `EssBenefits` alone enrolls, records life events and reads
  benefits only for the caller's own worker; `HcmWorkerMaintain` acts for
  anyone and `HcmWorkerInquire` reads anyone's.

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
  `MssApprove` approves it into a seat or the waitlist (not their own:
  HCM-CRS-SELF-APPROVAL). Registration closes when the session ends. An
  employee with only `EssLearning` registers and cancels only themselves.
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

- **Incident register** (FR-42.16, BR-42.11): `HcmIncidentReport` files an incident; the
  case number is a per-tenant counter. `osha_recordable` defaults from the facts (death,
  days away, restricted days, medical treatment beyond first aid, loss of consciousness);
  overriding it either way needs `overrideReason` (HCM-INC-001). Description, witnesses,
  treatments, root cause and corrective action are one fieldcrypt-sealed value
  (`hr_incident.details`); reads by id open it and write an audit row; events carry no
  medical text. Status goes REPORTED, UNDER_REVIEW, CLOSED; the reporter cannot close
  (HCM-INC-SELF-CLOSE). A CLOSED incident is frozen by a trigger. A correction is a new
  incident with `supersedesId`: it needs `HcmIncidentAdmin`, cannot be filed by the
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

## Run

Configuration is read from the environment and validated at startup:

| Variable | Required | Meaning |
|---|---|---|
| `DATABASE_URL` | yes | Postgres as `passage_app` (no BYPASSRLS) |
| `FIELDCRYPT_KEYS` / `FIELDCRYPT_KEYS_FILE`, `FIELDCRYPT_CURRENT_KEY_ID` | yes | `id=base64(32 bytes)` pairs and the key id new P3 values use (platform `fieldcrypt`); the service refuses to start without keys. The chart mounts the keys as `FIELDCRYPT_KEYS_FILE` |
| `HR_PII_KEY` | no | legacy base64 32-byte key; only opens P3 values written before fieldcrypt. Keep it until `hr reseal-pii` reports `rewritten=0` for every tenant |
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
| POST | `/v1/hr/workers` | HcmWorkerMaintain |
| GET | `/v1/hr/workers?q=&limit=&cursor=` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/employments` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/as-of?date=` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/pii?reason=` | HcmPIIRead |
| POST | `/v1/hr/workers/{id}/employments` | HcmWorkerMaintain |
| POST | `/v1/hr/positions` | HcmOrgMaintain |
| GET | `/v1/hr/positions?limit=&cursor=` | HcmOrgInquire |
| GET | `/v1/hr/positions/{id}` | HcmOrgInquire |
| POST | `/v1/hr/positions/{id}/assignments` | HcmActionMaintain |
| POST | `/v1/hr/absence/plans` | HcmAbsenceAdmin |
| GET | `/v1/hr/absence/plans?limit=&cursor=` | EssAbsence |
| GET | `/v1/hr/absence/plans/{id}` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/plans/{id}/year-end` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/plans/{id}/carry-over-expiry` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/accrue` | HcmAbsenceAdmin |
| POST | `/v1/hr/absence/requests` | EssAbsence (own worker unless HcmAbsenceAdmin) |
| GET | `/v1/hr/absence/requests?status=&workerId=&limit=&cursor=` | EssAbsence (own unless MssApprove or HcmAbsenceAdmin) |
| POST | `/v1/hr/absence/requests/{id}/decide` | MssApprove |
| POST | `/v1/hr/absence/requests/{id}/cancel` | EssAbsence (own unless HcmAbsenceAdmin) |
| GET | `/v1/hr/workers/{id}/absence/balances?asOf=` | EssAbsence (own unless MssApprove or HcmAbsenceAdmin) |
| POST | `/v1/hr/benefits/plans` | HcmSetupAdmin |
| GET | `/v1/hr/benefits/plans?limit=&cursor=` | EssBenefits |
| GET | `/v1/hr/benefits/plans/{id}` | EssBenefits |
| GET | `/v1/hr/benefits/plans/{id}/simulate?asOf=` | HcmSetupAdmin |
| POST | `/v1/hr/benefits/life-events` | EssBenefits (own worker unless HcmWorkerMaintain) |
| POST | `/v1/hr/benefits/enrollments` | EssBenefits (own worker unless HcmWorkerMaintain) |
| POST | `/v1/hr/benefits/enrollments/{id}/end` | HcmWorkerMaintain |
| GET | `/v1/hr/workers/{id}/benefits` | EssBenefits (own unless HcmWorkerInquire or HcmWorkerMaintain) |
| POST | `/v1/hr/skills` | HcmSetupAdmin |
| GET | `/v1/hr/skills?limit=&cursor=` | HcmWorkerInquire |
| GET | `/v1/hr/skills/search?skills=&minRating=&certs=&asOf=` | HcmOrgInquire |
| POST | `/v1/hr/workers/{id}/skills` | HcmWorkerMaintain |
| GET | `/v1/hr/workers/{id}/skills` | HcmWorkerInquire |
| POST | `/v1/hr/workers/{id}/certifications` | HcmWorkerMaintain |
| GET | `/v1/hr/workers/{id}/certifications?asOf=` | HcmWorkerInquire |
| GET | `/v1/hr/certifications/expiring?withinDays=&asOf=&limit=&cursor=` | HcmWorkerInquire |
| GET | `/v1/hr/workers/{id}/skill-gap?positionId=` | HcmOrgInquire |
| GET | `/v1/hr/positions/{id}/skill-requirements` | HcmOrgInquire |
| POST | `/v1/hr/positions/{id}/skill-requirements` | HcmOrgMaintain |
| DELETE | `/v1/hr/positions/{id}/skill-requirements/{skillCode}` | HcmOrgMaintain |
| POST | `/v1/hr/courses` | HcmLearningAdmin |
| GET | `/v1/hr/courses?limit=&cursor=` | EssLearning |
| GET | `/v1/hr/courses/{id}` | EssLearning |
| POST | `/v1/hr/courses/{id}/sessions` | HcmLearningAdmin |
| GET | `/v1/hr/courses/{id}/sessions` | EssLearning |
| GET | `/v1/hr/courses/sessions/{id}` | EssLearning |
| GET | `/v1/hr/courses/sessions/{id}/registrations` | HcmLearningAdmin |
| POST | `/v1/hr/courses/sessions/{id}/registrations` | EssLearning (own worker unless HcmLearningAdmin or MssApprove) |
| POST | `/v1/hr/courses/sessions/{id}/complete` | HcmLearningAdmin |
| POST | `/v1/hr/courses/registrations/{id}/approve` | MssApprove |
| POST | `/v1/hr/courses/registrations/{id}/cancel` | EssLearning (own unless HcmLearningAdmin or MssApprove) |
| POST | `/v1/hr/incidents` | HcmIncidentReport (HcmIncidentAdmin to correct) |
| GET | `/v1/hr/incidents?status=&limit=&cursor=` | HcmIncidentAdmin |
| GET | `/v1/hr/incidents/osha-extract?year=&siteId=` | HcmIncidentAdmin |
| GET | `/v1/hr/incidents/{id}` | HcmIncidentAdmin |
| POST | `/v1/hr/incidents/{id}/status` | HcmIncidentAdmin |
| POST | `/v1/hr/workers/{id}/loaned-items` | HcmWorkerMaintain |
| GET | `/v1/hr/workers/{id}/loaned-items` | HcmWorkerInquire |
| GET | `/v1/hr/loaned-items/outstanding?dueBy=` | HcmWorkerInquire |
| POST | `/v1/hr/loaned-items/{id}/close` | HcmWorkerMaintain |
| POST / GET | `/v1/hr/checklist-templates` | HcmSetupAdmin |
| POST / GET | `/v1/hr/workers/{id}/checklists` | HcmActionMaintain / HcmWorkerInquire |
| GET | `/v1/hr/checklists/{id}` | HcmWorkerInquire |
| POST | `/v1/hr/checklists/{id}/complete` | HcmActionMaintain |
| GET | `/v1/hr/tasks?ownerWorkerId=&status=` | HcmWorkerInquire |
| POST | `/v1/hr/tasks/{id}/finish` | HcmActionMaintain |
| POST / GET | `/v1/hr/questionnaires` | HcmQuestionnaireAdmin |
| GET | `/v1/hr/questionnaires/{id}` | HcmQuestionnaireAdmin |
| POST | `/v1/hr/questionnaires/schedules` | HcmQuestionnaireAdmin |
| GET | `/v1/hr/questionnaires/schedules/open` | EssQuestionnaire |
| GET | `/v1/hr/questionnaires/schedules/{id}` | EssQuestionnaire |
| POST | `/v1/hr/questionnaires/schedules/{id}/answers` | EssQuestionnaire (own worker only) |
| GET | `/v1/hr/questionnaires/schedules/{id}/results?by=department` | HcmQuestionnaireAdmin |

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
| `HCM-BEN-001` | 422 | Enrollment outside the open window or the life event's 30 days |
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
| `HCM-SKL-REQ-NOT-FOUND` | 404 | The position does not require that skill |
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
| `HCM-INC-CORRECTION-DUTY` | 403 | A correction needs HcmIncidentAdmin |
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
migrations/        0001_hr_core.sql, 0002_absence.sql, 0003_benefits.sql, 0004_grants.sql, 0005_skills.sql, 0006_courses.sql, 0007_pii_reseal_grant.sql, 0008_incidents_loans_checklists.sql, 0009_questionnaires.sql (applied in order by dbx.Migrate)
testdata/          hr-benefit-100.json (AC-42.5 fixture)
api/openapi.yaml   OpenAPI 3.1 for every endpoint above
events/asyncapi.yaml  AsyncAPI 3 for the eight events published
charts/hr/         Helm chart passage-hr (lint: helm lint charts/hr)
```

## Dependencies

Only the platform module and what it already uses (chi, pgx/v5,
shopspring/decimal, google/uuid, pgregory.net/rapid for tests).
