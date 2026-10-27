# HR module (web, PSG-42 slice 1)

The HR workspace for the slice-1 service (`services/hr/api/openapi.yaml`).
Routes and nav are in `module.ts`; the shell discovers it through the registry.

| Route | Page | Service calls |
|---|---|---|
| `/hr`, `/hr/workers` | Workers list with search and paging | `GET /workers?q=&cursor=` |
| `/hr/workers/new` | New worker (national ID and bank account go to the PII vault) | `POST /workers` |
| `/hr/workers/:id?date=` | Worker detail: identity, masked PII with an audited reveal (reason of at least 10 characters), employments with a start-employment form, and the as-of view (primary position, FTE share, manager, employments on the date) | `GET /workers/{id}`, `GET /workers/{id}/pii?reason=`, `GET`/`POST /workers/{id}/employments`, `GET /workers/{id}/as-of?date=` |
| `/hr/positions` | Positions list with FTE and reports-to | `GET /positions?cursor=` |
| `/hr/positions/new` | New position, reports-to chosen from existing positions | `POST /positions` |
| `/hr/positions/:id` | Position detail with its assignments and an assign-worker form (only the worker's current employments in the position's legal entity are offered) | `GET /positions/{id}`, `POST /positions/{id}/assignments` |

The as-of date is kept in the URL (`?date=`), so it survives reloads and can be
shared (PRD 8.42.6). Every create form sends one `Idempotency-Key` per filled-in
form, so a retried submit cannot create a second record. Pages follow
`apps/web/docs/design-system.md` (shell primitives, CSS Modules, loading,
empty and error states).

The module adds no npm dependencies. Tests: `npx vitest --run src/modules/hr`
and `npx tsc --noEmit` in `apps/web`.

## Needs

- Shell owner (wave 5): nav entries carry `HcmWorkerInquire` and
  `HcmOrgInquire`; the shell filters by duty once duties reach the client.
- Pickers for legal entity and department ids need the wave-6 org read
  contract; the forms take ids until then.
- Org chart, personnel actions, absence, benefits, ESS and MSS screens come
  with the later slices listed in `services/hr/NOT-IMPLEMENTED.md`.
