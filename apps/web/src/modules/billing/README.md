# billing (web module)

Billing and revenue recognition screens for wave 29 (PSG-29), slices 1 to 4. Built from the shell primitives in
`src/shell/ui` and CSS Modules (`pages.module.css`), per `apps/web/docs/design-system.md`.

| Route | Page | Service call | Duty |
|---|---|---|---|
| `/billing` | Invoices list with a status filter and Load more over the service cursor (loading, empty, error + retry) | `GET /api/v1/billing/invoices` | `billing.invoice.view` |
| `/billing/free-text-invoices/new` | Free-text invoice form: header, repeatable lines with revenue account and tax code, optional installment plan (shares must total 100%) | `POST /api/v1/billing/free-text-invoices` (Idempotency-Key) | `billing.invoice.maintain` |
| `/billing/invoices/:id` | Invoice or credit note detail: totals, lines, installments, post action (If-Match on the version shown; BIL-0001/0002/0005/0103 explained). A posted invoice shows credited and left-to-credit amounts, credited quantity per line, its credit notes and a Credit invoice action; a credit note shows its original and reason and posts through the credit-note endpoint | `GET /api/v1/billing/invoices/{id}`, `GET …/invoices?type=CREDIT_NOTE&originalInvoiceId=`, `POST …/invoices/{id}:post`, `POST …/credit-notes/{id}:post` | `billing.invoice.view` / `billing.invoice.post` / `billing.creditnote.post` |
| `/billing/credit-notes` | Credit notes list with a status filter and Load more: original invoice, credit date, gross (loading, empty, error + retry) | `GET /api/v1/billing/invoices?type=CREDIT_NOTE` | `billing.invoice.view` |
| `/billing/credit-notes/new?invoice=:id` | Credit note form: whole remainder or chosen line quantities (left-to-credit shown per line), charges, credit date and reason; BIL-0005 explained | `GET /api/v1/billing/invoices/{id}`, `POST /api/v1/billing/credit-notes` (Idempotency-Key) | `billing.invoice.maintain` |
| `/billing/revenue-allocation` | Revenue allocation preview: SSP allocation per obligation and ratable schedules on the daily or monthly basis (BR-29.1, FR-29.8) | `POST /api/v1/revrec/allocations:preview` | `revrec.view` |
| `/billing/revenue-contracts` | Revenue contracts list with status, obligation-method and customer filters and Load more: price, recognized, remaining (loading, empty, error + retry) | `GET /api/v1/revrec/contracts` | `revrec.view` |
| `/billing/revenue-contracts/new` | Revenue contract form: header with residual rule and ratable basis, repeatable obligations (product, SSP and source, method, transfer date or term) | `POST /api/v1/revrec/contracts` (Idempotency-Key) | `revrec.maintain` |
| `/billing/revenue-contracts/:id` | Contract detail: allocation per obligation, ratable basis, BIL-0009 warning, the contract's own deferred revenue roll-forward by month (FR-29.9 drill-down; loading and error + retry), schedules, satisfy-milestone action (If-Match on the version shown; REV-0004/0006/0103 explained) | `GET /api/v1/revrec/contracts/{id}`, `GET /api/v1/revrec/roll-forward?contractId=&period=`, `POST /api/v1/revrec/milestones/{id}:satisfy` | `revrec.view` / `revrec.maintain` |
| `/billing/deferred-revenue` | Deferred revenue roll-forward tiles for an entity, currency and month; run recognition through the month end (REV-0007 explained); runs list | `GET /api/v1/revrec/roll-forward`, `POST` and `GET /api/v1/revrec/recognition-runs` | `revrec.view` / `revrec.post` |

The API client (`api/client.ts`) follows the two OpenAPI files; the BFF rewrites
`/api/v1/...` to the services' `/v1/...`.

Tests: `npx vitest --run src/modules/billing` and `npx tsc --noEmit` in `apps/web`.
`testServer.tsx` stubs `fetch` and mounts the module's routes; pages are checked with axe.
`usePaged.ts` and `MoreRows.tsx` give the lists their "Load more" over the services' `nextCursor`.

Not yet here (see `services/billing/NOT-IMPLEMENTED.md`): billing workspace tiles, due
list and shipment proposals, subscriptions, document delivery, credit-note documents (PDF), and the
roll-forward's tie to GL 2400.

## Needs

None. React, react-router and the shell primitives only; no new npm dependencies.
