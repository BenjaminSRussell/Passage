# HR module (web, PSG-42 slices 1 and 2)

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
| `/hr/workers/:id` (Absence card) | The worker's balances for the current plan year (carry-over, accrued, used, available), their requests with cancel, and a request form whose balance and overlap refusals land on the fields; overlap warnings are shown after submit | `GET /workers/{id}/absence/balances?asOf=`, `GET /absence/requests?workerId=`, `GET /absence/plans`, `POST /absence/requests` |

The as-of date is kept in the URL (`?date=`), so it survives reloads and can be
shared (PRD 8.42.6). Every create form sends one `Idempotency-Key` per filled-in
form, so a retried submit cannot create a second record. Pages follow
`apps/web/docs/design-system.md` (shell primitives, CSS Modules, loading,
empty and error states).

The module adds no npm dependencies. Tests: `npx vitest --run src/modules/hr`
and `npx tsc --noEmit` in `apps/web`.

## Needs

- Shell owner (wave 5): nav entries carry `HcmWorkerInquire`,
  `HcmOrgInquire`, `HcmAbsenceAdmin` and `EssAbsence`; the shell filters by
  duty once duties reach the client. Until then the absence screens show the
  service's 403 as an error.
- Pickers for legal entity and department ids need the wave-6 org read
  contract; the forms take ids until then.
- A "my absence" page for employees needs the signed-in user's worker id from
  the shell. The Absence card sits on the worker page, which needs
  `HcmWorkerInquire`, so an employee with only `EssAbsence` sees their own
  requests in the queue (the service filters them) but their balances only
  once the ESS portal (`apps/ess`) or a "my absence" page exists.
- Org chart, personnel actions, absence calendars, benefits and the ESS/MSS
  portals come with the later slices listed in `services/hr/NOT-IMPLEMENTED.md`.
