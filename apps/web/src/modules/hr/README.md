# HR module (web, PSG-42 slices 1 to 5)

The HR workspace for the HR service (`services/hr/api/openapi.yaml`).
Routes and nav are in `module.ts`; the shell discovers it through the registry.

| Route | Page | Service calls |
|---|---|---|
| `/hr`, `/hr/workers` | Workers list with search and paging | `GET /workers?q=&cursor=` |
| `/hr/workers/new` | New worker (national ID and bank account go to the PII vault) | `POST /workers` |
| `/hr/workers/:id?date=` | Worker detail: identity, masked PII with an audited reveal (reason of at least 10 characters), employments with a start-employment form, and the as-of view (primary position, FTE share, manager, employments on the date) | `GET /workers/{id}`, `GET /workers/{id}/pii?reason=`, `GET`/`POST /workers/{id}/employments`, `GET /workers/{id}/as-of?date=` |
| `/hr/positions` | Positions list with FTE and reports-to | `GET /positions?cursor=` |
| `/hr/positions/new` | New position, reports-to chosen from existing positions | `POST /positions` |
| `/hr/positions/:id` | Position detail with its assignments and an assign-worker form (only the worker's current employments in the position's legal entity are offered) | `GET /positions/{id}`, `POST /positions/{id}/assignments` |
| `/hr/absence/plans` | Absence plans with rate, caps and plan-year start (absence admin) | `GET /absence/plans?cursor=` |
| `/hr/absence/plans/new` | New absence plan; decimals stay strings, defaults are PRD 8.42.13 | `POST /absence/plans` |
| `/hr/absence/plans/:id` | Plan detail: the rule, a run-accrual form (suggests the last whole period), the accrual periods, and the plan-year close and carry-over expiry with their history | `GET /absence/plans/{id}`, `POST /absence/accrue`, `POST /absence/plans/{id}/year-end`, `POST /absence/plans/{id}/carry-over-expiry` |
| `/hr/absence/requests?status=` | Request queue, oldest first, filtered by status (default submitted), with approve, reject and cancel. An employee sees their own requests, a manager or absence admin the tenant's | `GET /absence/requests?status=&cursor=`, `POST /absence/requests/{id}/decide`, `POST /absence/requests/{id}/cancel` |
| `/hr/benefits/plans` | Benefit plans with type, open enrollment window, waiting days and options (setup admin) | `GET /benefits/plans?cursor=` |
| `/hr/benefits/plans/new` | New benefit plan: options with employee and employer rates (decimals stay strings, places per currency), eligibility rule, window, waiting days | `POST /benefits/plans` |
| `/hr/benefits/plans/:id` | Plan detail: terms, coverage options, and the eligibility simulation as of a date (totals, each worker with the reason, eligible-only filter) | `GET /benefits/plans/{id}`, `GET /benefits/plans/{id}/simulate?asOf=` |
| `/hr/workers/:id` (Benefits card) | The worker's enrollments with the employee cost per period, life events with their window, an enroll form with the cost preview (window, eligibility and overlap refusals land on the fields or show the eligibility reason), a record-life-event form and an end-coverage form (termination keeps coverage to the month end) | `GET /workers/{id}/benefits`, `GET /benefits/plans`, `POST /benefits/enrollments`, `POST /benefits/life-events`, `POST /benefits/enrollments/{id}/end` |
| `/hr/workers/:id` (Absence card) | The worker's balances for the current plan year (carry-over, accrued, used, available), their requests with cancel, and a request form whose balance and overlap refusals land on the fields; overlap warnings are shown after submit | `GET /workers/{id}/absence/balances?asOf=`, `GET /absence/requests?workerId=`, `GET /absence/plans`, `POST /absence/requests` |
| `/hr/skills` | Skill catalog with each rating scale, paged | `GET /skills?cursor=` |
| `/hr/skills/new` | New skill: code, name, rating scale 1 to 10 | `POST /skills` |
| `/hr/skills/search?skills=&minRating=&certs=&asOf=` | Find workers by skill: CODE or CODE:rating terms, certificates valid on the date, each match with its ratings against the minimums; warns when the 500-row limit cut the list | `GET /skills/search` |
| `/hr/certifications/expiring?withinDays=&asOf=` | Certificates expiring within 60, 30 or 7 days, soonest first, with the days left and a link to the worker | `GET /certifications/expiring` |
| `/hr/workers/:id` (Skills card) | The worker's ratings, a rate form whose ratings follow the chosen skill's scale (a stale assessment lands on the date), and the gap against a chosen position (AC-42.16) | `GET`/`POST /workers/{id}/skills`, `GET /skills`, `GET /positions`, `GET /workers/{id}/skill-gap?positionId=` |
| `/hr/workers/:id` (Certifications card) | The worker's certificates with their validity today and a record form (expiry as months, a date or never) | `GET`/`POST /workers/{id}/certifications` |
| `/hr/positions/:id` (Skill profile card) | The skills the position requires with set, change and remove | `GET`/`POST /positions/{id}/skill-requirements`, `DELETE /positions/{id}/skill-requirements/{skillCode}` |
| `/hr/courses` | Course catalog with the skills and certificate a pass awards and whether registration needs approval, paged | `GET /courses?cursor=` |
| `/hr/courses/new` | New course: skill outcomes picked from the catalog with ratings on each skill's scale, the certificate and its validity, manager approval | `GET /skills`, `POST /courses` |
| `/hr/courses/:id` | Course detail: what a pass awards, the sessions with seats, waitlist and cost, and a schedule-session form (times in UTC) | `GET /courses/{id}`, `GET`/`POST /courses/{id}/sessions` |
| `/hr/courses/sessions/:id` | Session: seat tiles, the attendance list in registration order with approve, decline and cancel, a register-worker form until the session ends (full and approval warnings shown), and attendance with pass or fail from the session start | `GET /courses/sessions/{id}`, `GET`/`POST /courses/sessions/{id}/registrations`, `POST /courses/registrations/{id}/approve`, `POST /courses/registrations/{id}/cancel`, `POST /courses/sessions/{id}/complete`, `GET /workers` |

The as-of date is kept in the URL (`?date=`), so it survives reloads and can be
shared (PRD 8.42.6). Every create form sends one `Idempotency-Key` per filled-in
form, so a retried submit cannot create a second record. Pages follow
`apps/web/docs/design-system.md` (shell primitives, CSS Modules, loading,
empty and error states).

The module adds no npm dependencies. Tests: `npx vitest --run src/modules/hr`
and `npx tsc --noEmit` in `apps/web`.

## Needs

- Shell owner (wave 5): nav entries carry `HcmWorkerInquire`,
  `HcmOrgInquire`, `HcmAbsenceAdmin`, `EssAbsence`, `HcmSetupAdmin` and `HcmLearningAdmin`; the shell filters by
  duty once duties reach the client. Until then the absence screens show the
  service's 403 as an error.
- Pickers for legal entity and department ids need the wave-6 org read
  contract; the forms take ids until then.
- A "my absence" page for employees needs the signed-in user's worker id from
  the shell. The Absence card sits on the worker page, which needs
  `HcmWorkerInquire`, so an employee with only `EssAbsence` sees their own
  requests in the queue (the service filters them) but their balances only
  once the ESS portal (`apps/ess`) or a "my absence" page exists.
- The Benefits card sits on the worker page too, so an employee with only
  `EssBenefits` enrolls through the API until a "my benefits" page or the
  ESS portal exists (same need as "my absence").
- The course screens are the learning administrator's. Employees browsing
  the catalog and registering themselves (`EssLearning`), and a manager's
  approval queue (`MssApprove`), need the signed-in user's worker id and the
  ESS/MSS pages; the session page's registrations list needs
  `HcmLearningAdmin`.
- Org chart, personnel actions, absence calendars and the ESS/MSS portals
  come with the later slices listed in `services/hr/NOT-IMPLEMENTED.md`.
