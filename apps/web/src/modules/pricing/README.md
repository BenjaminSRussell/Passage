# Pricing web module (PSG-26, wave 26)

Routes, all on the pricing service API (`services/pricing/api/openapi.yaml`):

| Route | Page | Calls |
|---|---|---|
| `/pricing` | Workspace: active, draft, expiring-in-30-days and journals-to-post tiles, expiring list | `GET /condition-records?status=`, `GET /journals?status=` |
| `/pricing/agreements` | Trade agreements grid with status, type, customer, product and as-of filters | `GET /condition-records` |
| `/pricing/agreements/new` | Create a DRAFT agreement; `?copy=<id>` starts from an existing one | `POST /condition-records` |
| `/pricing/agreements/:id` | Detail with activate (overlap 409 shown) and withdraw | `GET`, `:activate`, `:withdraw` |
| `/pricing/journals` | Price journal log with a status filter | `GET /journals` |
| `/pricing/journals/new` | Create an OPEN journal from pasted `UPDATE, id, version, percent` / `DELETE, id, version` rows | `POST /journals` |
| `/pricing/journals/:id` | Lines with before and after values; validate, post (PRC-0005 shown) and reverse | `GET`, `:validate`, `:post`, `:reverse` |
| `/pricing/rebates` | Rebate agreements with basis, period and tiers | `GET /rebates/agreements` |
| `/pricing/rebates/new` | Create a tiered agreement for a customer or customer group | `POST /rebates/agreements` |
| `/pricing/rebates/:id` | Tiers, posting accounts, accruals by period with an exact total (retroactive rows marked), and the retroactive recompute from pasted posted invoice lines | `GET /rebates/agreements/{id}`, `GET /rebates/accruals`, `POST /rebates/agreements/{id}:retro-recompute` |
| `/pricing/commission/sales-groups` | Sales groups with their reps and shares | `GET /commission/sales-groups` |
| `/pricing/commission/sales-groups/new` | Create a sales group; reps with shares that add up to 100 | `POST /commission/sales-groups` |
| `/pricing/commission/sales-groups/:id` | Reps, commission rules in search order, add a rule (409 PRC-0038 shown) | `GET /commission/sales-groups/{id}`, `GET`/`POST /commission/rules` |
| `/pricing/commission/transactions` | Commission inquiry by sales group, rep, month, customer and invoice line, with an exact total | `GET /commission/transactions` |
| `/pricing/funds` | Trade funds with budget per period, overspend and open/closed status | `GET /funds` |
| `/pricing/funds/new` | Create a fund with its budget per period and overspend flag | `POST /funds` |
| `/pricing/funds/:id` | Fund usage inquiry (per period: budget, used, remaining; per source), attribute usage (PRC-0011 / PRC-0012 shown), revise the budget, close and reopen | `GET /funds/{id}/usage`, `:attribute`, `:revise`, `:close`, `:reopen` |
| `/pricing/promotions` | Promotions with event, window, baseline, forecast lift and actual lift | `GET /promotions` |
| `/pricing/promotions/new` | Plan a promotion: event category and type, fund, product, window, 13 baseline weeks, lift and spend | `POST /promotions` |
| `/pricing/promotions/:id` | Plan, baseline weeks, actuals and effectiveness; record actuals | `GET /promotions/{id}`, `:actuals` |
| `/pricing/simulate` | Simulation / explain: price lines and read the access-by-access trace | `POST /resolve?explain=true` |

- `api/client.ts` calls `/api/v1/pricing/...` (the BFF rewrites to `/v1/pricing/...`). Problem
  details surface as `PricingApiError` with the stable code and field errors. Commands send an
  `Idempotency-Key`; a retry after a network or 5xx failure reuses it.
- Pages use the shell primitives (`shell/ui`) and `pages.module.css` tokens; every list has
  loading, empty and error states.
- Tests: `npx vitest --run src/modules/pricing` and `npx tsc --noEmit` from `apps/web`. `testServer.tsx`
  holds the fetch stub and router used by `journals.test.tsx`, `rebates.test.tsx`, `commission.test.tsx`, `funds.test.tsx` and
  `promotions.test.tsx`.

Needs: nothing from the shell beyond the existing `shell/ui` components. No npm
dependencies were added.
