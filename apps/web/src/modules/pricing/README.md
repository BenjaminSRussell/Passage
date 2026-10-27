# Pricing web module (PSG-26, wave 26)

Routes, all on the pricing service API (`services/pricing/api/openapi.yaml`):

| Route | Page | Calls |
|---|---|---|
| `/pricing` | Workspace: active, draft and expiring-in-30-days tiles, expiring list | `GET /condition-records?status=` |
| `/pricing/agreements` | Trade agreements grid with status, type, customer, product and as-of filters | `GET /condition-records` |
| `/pricing/agreements/new` | Create a DRAFT agreement; `?copy=<id>` starts from an existing one | `POST /condition-records` |
| `/pricing/agreements/:id` | Detail with activate (overlap 409 shown) and withdraw | `GET`, `:activate`, `:withdraw` |
| `/pricing/simulate` | Simulation / explain: price lines and read the access-by-access trace | `POST /resolve?explain=true` |

- `api/client.ts` calls `/api/v1/pricing/...` (the BFF rewrites to `/v1/pricing/...`). Problem
  details surface as `PricingApiError` with the stable code and field errors. Commands send an
  `Idempotency-Key`; a retry after a network or 5xx failure reuses it.
- Pages use the shell primitives (`shell/ui`) and `pages.module.css` tokens; every list has
  loading, empty and error states.
- Tests: `npx vitest --run src/modules/pricing` and `npx tsc --noEmit` from `apps/web`.

Needs: nothing from the shell beyond the existing `shell/ui` components. No npm
dependencies were added.
