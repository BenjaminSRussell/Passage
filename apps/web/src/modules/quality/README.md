# Quality web module (wave 33, PSG-33 slice 1)

Auto-discovered by the shell through `module.ts`. Pages use the shell primitives from
`src/shell/ui` and `pages.module.css`; every list has loading, empty and error states.

| Route | Page | Service calls | Duty |
|---|---|---|---|
| `/quality`, `/quality/orders` | `OrdersPage`: quality order worklist, filter by status and site | `GET /quality-orders` | QM_VIEW |
| `/quality/orders/new` | `NewOrderPage`: manual order (test group + sampling plan) or a trigger matched to an association | `GET /test-groups`, `POST /quality-orders` | QM_ORDER_MAINTAIN |
| `/quality/orders/:id` | `OrderDetailPage`: lot, sample, Ac/Re, evaluation, results; `ResultsEntry` (one sample, a row per test) and `UsageDecisionForm` (split lines that sum to the lot, `If-Match`) | `GET /quality-orders/{id}`, `GET /instruments`, `POST …/results`, `POST …:usage-decision` | QM_VIEW, QM_RESULTS_RECORD, QM_USAGE_DECISION |
| `/quality/test-groups`, `/new` | Test group versions; create the next version with variable and attribute tests | `GET`/`POST /test-groups` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/instruments`, `/new` | Instruments with calibration state, "Calibrated today"; register | `GET`/`POST /instruments`, `POST /instruments/{id}:calibrate` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/associations`, `/new` | Quality associations with scope, test group and sampling | `GET`/`POST /associations` | QM_VIEW, QM_SETUP_MAINTAIN |
| `/quality/sampling` | Sampling calculator (Z1.4 level II, fixed, percent) | `POST /sampling-plans:size` | QM_VIEW |

`api/client.ts` is the typed `fetch` client for `services/quality/api/openapi.yaml`.
Decimals are strings, never numbers; decision totals are summed exactly. Failures throw
`QualityApiError` with the stable problem `code` and field errors. Forms keep one
`Idempotency-Key` per attempt so a retried submit cannot post twice.

## Test

```
cd apps/web && npx vitest --run src/modules/quality && npx tsc --noEmit
```

## Needs (for the shell owner, wave 5)

- The BFF must rewrite `/api/v1/quality/*` to the quality service (`/v1/quality/*`).
- No npm dependencies were added.

## Not yet in this module

Nonconformance and CAPA, certificate receipt, CoA preview, SPC and scorecard views.
See `services/quality/NOT-IMPLEMENTED.md`.
