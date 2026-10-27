# billing (web module)

Billing and revenue recognition screens for wave 29 (PSG-29), slices 1 and 2. Built from the shell primitives in
`src/shell/ui` and CSS Modules (`pages.module.css`), per `apps/web/docs/design-system.md`.

| Route | Page | Service call | Duty |
|---|---|---|---|
| `/billing` | Invoices list with a status filter (loading, empty, error + retry) | `GET /api/v1/billing/invoices` | `billing.invoice.view` |
| `/billing/free-text-invoices/new` | Free-text invoice form: header and repeatable lines with revenue account and tax code | `POST /api/v1/billing/free-text-invoices` (Idempotency-Key) | `billing.invoice.maintain` |
| `/billing/invoices/:id` | Invoice detail: totals, lines, post action (If-Match on the version shown), BIL-0001/0002/0103 explained | `GET /api/v1/billing/invoices/{id}`, `POST …/{id}:post` | `billing.invoice.view` / `billing.invoice.post` |
| `/billing/revenue-allocation` | Revenue allocation preview: SSP allocation per obligation and ratable schedules (BR-29.1) | `POST /api/v1/revrec/allocations:preview` | `revrec.view` |
| `/billing/revenue-contracts` | Revenue contracts list with status and customer filters: price, recognized, remaining (loading, empty, error + retry) | `GET /api/v1/revrec/contracts` | `revrec.view` |
| `/billing/revenue-contracts/new` | Revenue contract form: header and repeatable obligations (product, SSP and source, method, transfer date or term) | `POST /api/v1/revrec/contracts` (Idempotency-Key) | `revrec.maintain` |
| `/billing/revenue-contracts/:id` | Contract detail: allocation per obligation, BIL-0009 warning, schedules, satisfy-milestone action (If-Match on the version shown; REV-0004/0006/0103 explained) | `GET /api/v1/revrec/contracts/{id}`, `POST /api/v1/revrec/milestones/{id}:satisfy` | `revrec.view` / `revrec.maintain` |
| `/billing/deferred-revenue` | Deferred revenue roll-forward tiles for an entity, currency and month; run recognition through the month end (REV-0007 explained); runs list | `GET /api/v1/revrec/roll-forward`, `POST` and `GET /api/v1/revrec/recognition-runs` | `revrec.view` / `revrec.post` |

The API client (`api/client.ts`) follows the two OpenAPI files; the BFF rewrites
`/api/v1/...` to the services' `/v1/...`.

Tests: `npx vitest --run src/modules/billing` and `npx tsc --noEmit` in `apps/web`.
`testServer.tsx` stubs `fetch` and mounts the module's routes; pages are checked with axe.

Not yet here (see `services/billing/NOT-IMPLEMENTED.md`): billing workspace tiles, due
list and shipment proposals, credit notes, subscriptions, document delivery, the
roll-forward drill-down per contract and its tie to GL 2400.

## Needs

None. React, react-router and the shell primitives only; no new npm dependencies.
