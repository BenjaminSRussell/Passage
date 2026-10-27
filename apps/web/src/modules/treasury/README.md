# treasury (web module)

Treasury screens for wave 36 (PSG-36) slice 1: bank accounts, camt.053 statement import and
the cash flow forecast. Built from the shell primitives in `src/shell/ui` and CSS Modules
(`pages.module.css`), per `apps/web/docs/design-system.md`.

| Route | Page | Service call | Duty |
|---|---|---|---|
| `/treasury` | Bank accounts of a legal entity (id kept in the URL): code, masked IBAN, BIC, currency, status (choose-entity, loading, empty, error + retry) | `GET /api/v1/treasury/bank-accounts?legalEntityId=` | `treasury.view` |
| `/treasury/bank-accounts/new` | Create a Draft account; IBAN mod-97, BIC, code and currency checked in the form; TRS-0401/0404 explained | `POST /api/v1/treasury/bank-accounts` (Idempotency-Key) | `treasury.bank-account.write` |
| `/treasury/bank-accounts/:id` | Account details, lifecycle actions for the current status (activate, deactivate, reactivate, close; If-Match on the version shown, 412 explained and reloaded), and the account's statements | `GET …/bank-accounts/{id}`, `POST …/bank-accounts/{id}:activate\|:deactivate\|:close`, `GET …/bank-accounts/{id}/statements` | `treasury.view` / `treasury.bank-account.approve` |
| `/treasury/statements/import` | Upload one camt.053 file (multipart); a re-upload is shown as already imported (TRS-0405), sequence gaps as warnings (TRS-0407), parse errors with the failing entry | `POST /api/v1/treasury/statements/import` | `treasury.statement.import` |
| `/treasury/statements/:id` | Statement balances and its entries with Load more over `nextAfterLineNo` | `GET …/statements/{id}`, `GET …/statements/{id}/lines` | `treasury.view` |
| `/treasury/cash-forecast` | Forecast buckets (daily, weekly, monthly) per currency with inflow, outflow, net and cumulative; add a manual or source entry | `GET /api/v1/treasury/cash-forecast`, `POST /api/v1/treasury/cash-forecast-entries` (Idempotency-Key) | `treasury.view` / `treasury.forecast.write` |

The API client (`api/client.ts`) follows `services/treasury/api/openapi.yaml`; the BFF rewrites
`/api/v1/...` to the service's `/v1/...`. Amounts stay decimal strings.

Tests: `npx vitest --run src/modules/treasury` and `npx tsc --noEmit` in `apps/web`.
`testServer.tsx` stubs `fetch` and mounts the module's routes; pages are checked with axe.

Not yet here (see `services/treasury/NOT-IMPLEMENTED.md`): reconciliation workspace, cash position,
checks, facilities and instruments, bank connections, and a legal-entity picker (the org module's
switcher is not shared with other modules yet, so the legal entity id is typed).
