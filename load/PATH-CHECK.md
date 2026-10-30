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
| `PATHS.journalEntries` | `POST /api/v1/ledger/journal-entries` | ledger `POST /v1/ledger/journal-entries` | none |
| `PATHS.pricingResolve` | `POST /api/v1/pricing/resolve` | pricing `POST /v1/pricing/resolve` | none |
| `PATHS.atpCheck` | `POST /api/v1/sales/atp:check` | sales `POST /v1/sales/atp:check` | none |
| `PATHS.salesOrders` | `POST /api/v1/sales/orders` | sales `POST /v1/sales/orders` | none |
| `PATHS.generatedData` | `GET /api/v1/data/{entity}` | metadata `GET /v1/data/{entity}` | none |
| `PATHS.parties` | `POST /api/v1/party/parties` | party `POST /v1/party/parties` | none |
| `PATHS.dashboards` | `GET /api/v1/analytics/dashboards/{id}` | analytics `GET /v1/analytics/dashboards/{id}` | none |
| `PATHS.analyticsQuery` | `POST /api/v1/analytics/query` | analytics `POST /v1/analytics/query` | none |
| `PATHS.planRun` | `POST /api/v1/planning/plans/{planCode}:run` | planning `POST /v1/planning/plans/{planCode}:run` | none |
| `PATHS.allocationRuns` | `POST /api/v1/controlling/allocation-runs` | costacct `POST /v1/controlling/allocation-runs` | none |
| `UNVERIFIED_PATHS.scans` | `POST /api/v1/warehouse/scans` | none (expected) | not checked |
| `UNVERIFIED_PATHS.inventoryClose` | `POST /api/v1/inventory/close` | none (expected) | not checked |
| `UNVERIFIED_PATHS.fxRevaluation` | `POST /api/v1/ledger/fx-revaluations` | none (expected) | not checked |
| `UNVERIFIED_PATHS.freshness` | `GET /api/v1/analytics/freshness` | none (expected) | not checked |

Service check: all 14 requests pass (10 resolve, 4 still unpublished as `UNVERIFIED_PATHS` expects), against
1518 service operations (1507 at `ba93dd3e`; the routes the campaign calls did not change).

Edge check: 0 of 10 `PATHS` requests are forwarded. `deploy/gateway/httproutes.yaml` routes only `/api/v1/org`,
`/api/v1/fx` and `/api/v1/receiving`, so every campaign request would get a 404 at the gateway.

## Findings that need action

1. **No gateway routes for the campaign's contexts.** The edge needs HTTPRoutes for ledger, pricing, sales,
   party, analytics and planning before a campaign can pass. `deploy/gateway` belongs to wave 1, so this is a
   handoff (see `docs/ops/NOT-IMPLEMENTED.md`).
2. **Two public prefixes differ from the service name.** metadata serves `/v1/data` and costacct serves
   `/v1/controlling`. The gateway's convention (`deploy/scripts/test_gateway.py`) names each HTTPRoute and its
   backend after the prefix, so these two need either a backend alias or an exception to that convention. Same
   handoff.
3. **Four campaign operations have no service route** (`UNVERIFIED_PATHS`): warehouse scans, inventory close,
   FX revaluation and lakehouse freshness. They are handoffs to the owning waves, already listed in
   `docs/ops/NOT-IMPLEMENTED.md`.
