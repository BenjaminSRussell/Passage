# Campaign route check (PSG-50, FR-50.1)

Record of `load/tools/verify_paths.py` run on 2026-10-29 against main at `8e452b55` (re-run in wave 50 slice 11;
first recorded in slice 10 at `ba93dd3e`). It is a snapshot: `load/tools/test_verify_paths.py` re-runs the
service check on every test run, so a renamed or newly published route fails the tests before this file goes
stale. Re-run both commands below before a campaign and
replace the tables with their output.

```
python3 load/tools/verify_paths.py                          # services: exit 0
python3 load/tools/verify_paths.py --gateway deploy/gateway # services and edge: exit 1
```

Rule: each `http.<method>` call in `load/k6/scenarios.js` is resolved to a method and a gateway path. The
service check strips `/api` and looks the method and path up in `services/*/api/openapi.yaml`. The edge check
(`--gateway`) looks for an HTTPRoute `PathPrefix` under `/api/v1/` that forwards the path.

## Result

| `scenarios.js` key | Request | Service route | Gateway route |
|---|---|---|---|
| `PATHS.journalEntries` | `POST /api/v1/ledger/journal-entries` | ledger `POST /v1/ledger/journal-entries` | forwarded |
| `PATHS.pricingResolve` | `POST /api/v1/pricing/resolve` | pricing `POST /v1/pricing/resolve` | forwarded |
| `PATHS.atpCheck` | `POST /api/v1/sales/atp:check` | sales `POST /v1/sales/atp:check` | forwarded |
| `PATHS.salesOrders` | `POST /api/v1/sales/orders` | sales `POST /v1/sales/orders` | forwarded |
| `PATHS.generatedData` | `GET /api/v1/data/{entity}` | metadata `GET /v1/data/{entity}` | forwarded |
| `PATHS.parties` | `POST /api/v1/party/parties` | party `POST /v1/party/parties` | forwarded |
| `PATHS.dashboards` | `GET /api/v1/analytics/dashboards/{id}` | analytics `GET /v1/analytics/dashboards/{id}` | forwarded |
| `PATHS.analyticsQuery` | `POST /api/v1/analytics/query` | analytics `POST /v1/analytics/query` | forwarded |
| `PATHS.planRun` | `POST /api/v1/planning/plans/{planCode}:run` (`Prefer: respond-async`) | planning `POST /v1/planning/plans/{planCode}:run` | forwarded |
| S5 poll of the run's `Location` | `GET /api/v1/planning/plan-runs/{id}` | planning `GET /v1/planning/plan-runs/{id}` | forwarded |
| `PATHS.allocationRuns` | `POST /api/v1/controlling/allocation-runs` | costacct `POST /v1/controlling/allocation-runs` | forwarded |
| `UNVERIFIED_PATHS.scans` | `POST /api/v1/warehouse/scans` | none (expected) | not checked |
| `UNVERIFIED_PATHS.inventoryClose` | `POST /api/v1/inventory/close` | none (expected) | not checked |
| `UNVERIFIED_PATHS.fxRevaluation` | `POST /api/v1/ledger/fx-revaluations` | none (expected) | not checked |
| `UNVERIFIED_PATHS.freshness` | `GET /api/v1/analytics/freshness` | none (expected) | not checked |

Service check: all 14 requests pass (10 resolve, 4 still unpublished as `UNVERIFIED_PATHS` expects), against
1518 service operations (1507 at `ba93dd3e`; the routes the campaign calls did not change).

Edge check: 10 of 10 `PATHS` requests are forwarded since wave 50 (50-1). `deploy/gateway/httproutes.yaml` now also routes
ledger, pricing, sales, party, analytics, planning, `data` (Service metadata, port 80) and `controlling` (Service
costacct). `test_verify_paths.py` and `deploy/scripts/test_gateway.py` keep it so.

## Findings that need action

1. **Gateway routes** (resolved in wave 50, 50-1): HTTPRoutes added for the campaign's contexts. `data` and `controlling`
   are named after their prefix and point at the `metadata` and `costacct` Services (`BACKEND_OF_ROUTE` in
   `deploy/scripts/test_gateway.py`).
2. ~~Two public prefixes differ from the service name~~: covered by the aliases above.
3. **Four campaign operations have no service route** (`UNVERIFIED_PATHS`): warehouse scans, inventory close,
   FX revaluation and lakehouse freshness. They are handoffs to the owning waves, already listed in
   `docs/ops/NOT-IMPLEMENTED.md`.
