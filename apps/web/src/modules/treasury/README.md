# treasury (web module)

Treasury screens for wave 36 (PSG-36) slices 1 and 3: bank accounts, camt.053 statement import,
the cash flow forecast, checks, bank facilities with letters of credit and guarantees, and loan
contracts. Built from the shell primitives in `src/shell/ui` and CSS Modules
(`pages.module.css`), per `apps/web/docs/design-system.md`.

| Route | Page | Service call | Duty |
|---|---|---|---|
| `/treasury` | Bank accounts of a legal entity (id kept in the URL): code, masked IBAN, BIC, currency, status (choose-entity, loading, empty, error + retry) | `GET /api/v1/treasury/bank-accounts?legalEntityId=` | `treasury.view` |
| `/treasury/bank-accounts/new` | Create a Draft account; IBAN mod-97, BIC, code and currency checked in the form; TRS-0401/0404 explained | `POST /api/v1/treasury/bank-accounts` (Idempotency-Key) | `treasury.bank-account.write` |
| `/treasury/bank-accounts/:id` | Account details, lifecycle actions for the current status (activate, deactivate, reactivate, close; If-Match on the version shown, 412 explained and reloaded), and the account's statements | `GET …/bank-accounts/{id}`, `POST …/bank-accounts/{id}:activate\|:deactivate\|:close`, `GET …/bank-accounts/{id}/statements` | `treasury.view` / `treasury.bank-account.approve` |
| `/treasury/statements/import` | Upload one camt.053 file (multipart); a re-upload is shown as already imported (TRS-0405), sequence gaps as warnings (TRS-0407), parse errors with the failing entry | `POST /api/v1/treasury/statements/import` | `treasury.statement.import` |
| `/treasury/statements/:id` | Statement balances and its entries with Load more over `nextAfterLineNo` | `GET …/statements/{id}`, `GET …/statements/{id}/lines` | `treasury.view` |
| `/treasury/checks` | Check register of a bank account (id kept in the URL): number, payee, amount, status (choose-account, loading, empty, error + retry) | `GET /api/v1/treasury/checks?bankAccountId=` | `treasury.view` |
| `/treasury/checks/new` | Issue a check on the account's next number; payee, amount places per currency and currency checked in the form; TRS-0412/0426 explained | `POST /api/v1/treasury/checks` (Idempotency-Key) | `treasury.check.write` |
| `/treasury/checks/:id` | Check details and the actions its status allows (mark printed, sent, cleared; stop; void and reprint with a reason; If-Match, 412 explained and reloaded); a reprint opens the new check | `GET …/checks/{id}`, `POST …/checks/{id}:print\|:send\|:clear\|:stop\|:void\|:reprint` | `treasury.view` / `treasury.check.write` |
| `/treasury/facilities` | Facilities of a legal entity with limit, utilization, headroom and expiry | `GET /api/v1/treasury/facilities?legalEntityId=` | `treasury.view` |
| `/treasury/facilities/new` | Register a facility; TRS-0427 duplicate code shown on the field | `POST /api/v1/treasury/facilities` (Idempotency-Key) | `treasury.facility.write` |
| `/treasury/facilities/:id` | Utilization tiles, instruments with close, and booking an LC or guarantee; a limit breach (TRS-0415) or a missing override duty (AUTH-0003) is explained | `GET …/facilities/{id}`, `GET\|POST …/facilities/{id}/instruments`, `POST …/facility-instruments/{id}:close` | `treasury.view` / `treasury.facility.write` (+ `treasury.facility.override`) |
| `/treasury/loans` | Loan contracts of a legal entity: counterparty, role, principal, rate, schedule, dates | `GET /api/v1/treasury/loans?legalEntityId=` | `treasury.view` |
| `/treasury/loans/new` | Record a fixed-rate loan; the rate is typed in percent and sent as a fraction without float arithmetic; TRS-0446 shown on the maturity date | `POST /api/v1/treasury/loans` (Idempotency-Key) | `treasury.loan.write` |
| `/treasury/loans/:id` | Loan terms and the generated schedule (period, days, opening, interest, repayment, closing) | `GET …/loans/{id}` | `treasury.view` |
| `/treasury/cash-forecast` | Forecast buckets (daily, weekly, monthly) per currency with inflow, outflow, net and cumulative; add a manual or source entry | `GET /api/v1/treasury/cash-forecast`, `POST /api/v1/treasury/cash-forecast-entries` (Idempotency-Key) | `treasury.view` / `treasury.forecast.write` |

The API client (`api/client.ts`) follows `services/treasury/api/openapi.yaml`; the BFF rewrites
`/api/v1/...` to the service's `/v1/...`. Amounts stay decimal strings.

Tests: `npx vitest --run src/modules/treasury` and `npx tsc --noEmit` in `apps/web`.
`testServer.tsx` stubs `fetch` and mounts the module's routes; pages are checked with axe.

Not yet here (see `services/treasury/NOT-IMPLEMENTED.md`): reconciliation workspace, cash position,
instrument amendments, bills of exchange and postdated checks, bank connections, and a legal-entity picker (the org module's
switcher is not shared with other modules yet, so the legal entity id is typed).
