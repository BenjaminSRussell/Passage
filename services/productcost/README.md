# productcost

Wave 39 (PSG-39): product costing, cost roll-up and variance analysis.
Bounded context `costing`, Postgres schema `costing` (per-context database).

## Slice 1 (this build)

The service is **stateless** in slice 1. The caller sends the structure, prices and
actuals in the request; the service computes and returns:

- **Cost roll-up** (`POST /v1/costing/calculations`): one item rolled from its BOM and
  routing. Returns the unit cost and the cost component split (own lines plus the split
  carried up from exploded children). Only the item's reachable subgraph is rolled.
- **Mass roll** (`POST /v1/costing/rolls`): every item in the request, rolled by
  topological level with per-item diagnostics. Cycles, depth overruns and missing
  prices fail only the affected items; the run continues.
- **Variance decomposition** (`POST /v1/costing/variances/compute`): an ended order's
  total variance split into lot size, quantity, price and substitution, per component.
  The four types always sum to the total in the currency's minor unit; each component's
  rounding residual is assigned to its quantity variance, and a residual above two minor
  units fails with `CST-0720`.
- **Activation preview** (`POST /v1/costing/activations:preview`): the on-hand
  revaluation per line, the total and the balanced journal. Nothing is posted.

The variance compute and the activation preview take an optional ISO 4217 `currency`;
amounts round to its minor unit (`platform/money`), two decimals when it is omitted.

## Duties

| Route | Duty (PRD 7.39.17) |
|---|---|
| `POST /v1/costing/calculations`, `POST /v1/costing/rolls` | `CostingCalculate` |
| `POST /v1/costing/variances/compute` | `CostingSystem` |
| `POST /v1/costing/activations:preview` | `CostingActivate` |

## Web

`apps/web/src/modules/productcost/`: the costing workspace (`/productcost`), cost roll-up
(`/productcost/roll`), production variances (`/productcost/variances`) and activation
preview (`/productcost/activation-preview`).

The worked example of FR-39.4 is an exact test: FG-100 = 104.32 with split
DM 21.20 / DL 54.00 / IND-MAT 2.12 / IND-LAB 27.00.

Costs are decimal strings (never floats). Unit costs are stored and returned at six
decimals; splits always sum to the unit cost.

Explosion modes in slice 1: `Multilevel` (default) and `SingleLevel`. Costing sheet nodes:
`Surcharge` (percentage of named components) and `Rate` (per routing hour or per unit);
`Basis` and `Total` nodes are refused until the persisted sheet arrives.

## Layout

```
cmd/productcost/        entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> register -> serve
internal/domain/        roll-up, planner, mass roll, variances, pricing helpers (pure, no I/O)
internal/api/           HTTP handlers, DTOs, validation
migrations/             costing schema (costing_version, cost_calculation, cost_component_split,
                        cost_component, variance_entry) with RLS; not yet written to by slice 1;
                        migrations_test.go applies it and checks RLS when a database is set
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 (no channels in slice 1)
charts/productcost/     Helm chart
```

## Run

```
go run ./cmd/productcost            # requires DATABASE_URL for dbx.Connect
go test ./...                       # runs without a database; DB tests skip
PASSAGE_TEST_DATABASE_URL=... go test ./...   # enables testkit-backed tests
go vet ./...
helm lint charts/productcost
```

Configuration (12-factor): `COSTING_MAX_LEVELS` (default 20, 1-40),
`COSTING_ROLL_WORKERS` (default 4, 1-32), plus the platform variables
`PASSAGE_ENV`, `HTTP_ADDR`, `ADMIN_ADDR`, `DATABASE_URL`.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid, pgregory.net/rapid for tests). No new dependencies.

## Error codes

Problem `type` is `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| AUTH-0001 | 401 | missing or malformed tenant |
| AUTH-0003 | 403 | the caller lacks the route's duty |
| COM-0001 | 400 | request failed validation (`errors` maps each field path to its message) |
| CST-0701 | item error | cycle detected in the BOM (path listed) |
| CST-0702 | item error | no price for the item in this version (fallback None, or no active standard) |
| CST-0703 | warning | fallback price used from the active standard |
| CST-0704 | item error | BOM depth exceeds `maxLevels` |
| CST-0706 | item error | component is not in the catalog |
| CST-0707 | item error | blocked: a component is in error (cascade, the rest of the run continues) |
| CST-0720 | 422 | variance identity failed for the order (residual above the rounding bound) |

Problem codes for the mass roll and the activation preview are not in the catalog;
their failures are per-item diagnostics or request validation (COM-0001).

## Wave reference

PSG-39 (Wave 39), phase 7. Depends on PSG-38. Full list of deferred deliverables in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).
