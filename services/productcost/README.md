# productcost

Wave 39 (PSG-39): product costing, cost roll-up and variance analysis.
Bounded context `costing`, Postgres schema `costing` (per-context database).

## Slices 1 and 2

The service is **stateless** in slices 1 and 2. The caller sends the structure, prices and
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

Slice 2 adds:

- **Mixed cost** (`POST /v1/costing/mixed-costs`, FR-39.14): each procurement alternative
  with a non-zero ratio is rolled with its own structure; the item cost and split are the
  ratio-weighted mix (AC-39.11). Ratios must total 100.00 (`CST-0741`, 422); a 0.00 ratio
  is reported as `Excluded` and left out (BR-39.12). An alternative that fails to roll fails
  the item unless fallback applies. Sources are unique per item. `DeriveRatios` (domain only)
  turns receipt quantities per source into ratios at 0.01%, the shortfall going to the
  largest remainders.
- **COGS posting preview** (`POST /v1/costing/postings:cogs`, FR-39.15): Dr `COGS-<component>`
  per component of the unit split, Cr `Inventory` for the total. Components sum to the
  total; the rounding residual goes to DM (BR-39.13).
- **Price difference posting preview** (`POST /v1/costing/postings:price-difference`,
  FR-39.7, FR-39.15): the PPV `(receipt unit cost − standard) × quantity` split over the
  standard's components by their share, residual to the largest component; Dr (unfavourable)
  or Cr (favourable) `PPV-<component>`, offset to `PriceDifferenceClearing`.
- **Sales order estimate** (`POST /v1/costing/sales-order-estimates`, FR-39.16): rolls the
  configured or make-to-order item at the order quantity (the order is the root's costing
  lot) and returns calculation type `SalesOrder`, the cost, its split (summing to the cost,
  residual to DM), margin = net − cost and margin % on the net amount (AC-39.13). Nothing is
  stored, so freezing at confirmation is not built (see NOT-IMPLEMENTED D16).
- **Comparison** (`POST /v1/costing/comparisons`, FR-39.10): a baseline run and a candidate
  (what-if) run item by item, with delta, delta % and component deltas. Items on one side
  only are reported; items in error on either side carry no delta.
- **Standard reconciliation** (`POST /v1/costing/reconciliations`, FR-39.11, AC-39.9):
  Σ on-hand × standard per site against the GL inventory balance; sites with a non-zero
  difference are counted in `differences` and in the
  `productcost_reconciliation_differences_total` metric (alert: difference ≠ 0).

Every route that posts money takes an optional ISO 4217 `currency`; amounts round to its
minor unit (`platform/money`), two decimals when it is omitted (BR-39.1).

## Roll-up rules

The worked example of FR-39.4 is an exact test: FG-100 = 104.32 with split
DM 21.20 / DL 54.00 / IND-MAT 2.12 / IND-LAB 27.00.

Costs are decimal strings (never floats). Unit costs are stored and returned at six
decimals; splits always sum to the unit cost.

Explosion modes (`options.mode`, FR-39.4 step 3):

| Mode | Purchased child | Manufactured child | Phantom line |
|---|---|---|---|
| `Multilevel` (default) | version price (fallback per BR-39.4) | rolled in this run | in place |
| `SingleLevel` | active standard (`activeSplit` or `activePrice`) | active standard (`activeSplit`) | in place |
| `MakeToOrder` | version price | rolled when the line is `makeToOrder`, else active standard | in place |
| `ByLineType` | version price | active standard | in place |

A child without the active standard a mode needs fails the parent with `CST-0702`.

A **phantom** line (`phantom: true`, BR-39.3) must name a manufactured item. It is exploded
in place in every mode: the phantom's direct costs (its purchased lines, operations and any
phantoms below it) and its routing hours join the parent's own level, its rolled
sub-assemblies stay rolled, and the parent's costing sheet applies to them; the phantom's
own sheet does not. A line cannot be both `phantom` and `makeToOrder`.

**Route scrap** (`operations[].scrapPct`, BR-39.6) compounds backward: an operation
processes `Π (1 + scrap%)` over itself and every later operation, so its run time and
routing hours are charged on that quantity, and the BOM material, issued before the first
operation, is scaled by the first operation's factor. Setup is per lot and is not scaled
(BR-39.5). BOM line scrap multiplies the line quantity as before.

Costing sheet nodes (FR-39.2) are evaluated in sheet order: `Surcharge` (percentage of named
components, or of an earlier `Basis` or `Total` subtotal named in `basisRef`), `Rate` (per
routing hour or per unit), `Basis` (subtotal of named components) and `Total` (sum of earlier
subtotals named in `refs`). See slice 3 below.

## Duties

| Route | Duty (PRD 7.39.17) |
|---|---|
| `POST /v1/costing/calculations`, `/rolls`, `/mixed-costs`, `/sales-order-estimates`, `/sales-order-estimates:refresh`, `/sales-order-estimates:freeze`, `/estimates`, `/postings:cogs`, `/postings:price-difference` | `CostingCalculate` |
| `POST /v1/costing/comparisons`, `/reconciliations` | `CostingView` |
| `POST /v1/costing/variances/compute` | `CostingSystem` |
| `POST /v1/costing/activations:preview`, `/versions:activate`, `/conversions:validate`, `/conversions:advance` | `CostingActivate` |
| `POST /v1/costing/versions:transition` (block, unblock, close) | `CostingMaintain` |
| `GET /v1/costing/cost-components`, `/cost-groups`, `/versions`, `/versions/{id}` | `CostingView` |
| `POST /v1/costing/cost-components`, `/cost-groups`, `/versions`, `/versions/{id}/transition` | `CostingMaintain` |
| `POST /v1/costing/versions/{id}/activate` | `CostingActivate` |

The comparison and the reconciliation compute an inquiry without changing anything, so they
take the PRD's read duty `CostingView` (the PRD's `GET /costing/compare` and the inventory
value report).

## Web

`apps/web/src/modules/productcost/`: the costing workspace (`/productcost`), cost roll-up
(`/productcost/roll`), production variances (`/productcost/variances`), activation
preview (`/productcost/activation-preview`), mixed cost (`/productcost/mixed-cost`), sales
order estimate (`/productcost/sales-order-estimate`), component postings
(`/productcost/postings`), estimate comparison (`/productcost/compare`), standard
reconciliation (`/productcost/reconciliation`) and, from slice 3, costing versions
(`/productcost/versions`) and the standard cost conversion checklist
(`/productcost/conversion`), and from slice 4 ad-hoc estimates (`/productcost/estimates`) and
sales order estimate versions with refresh and freeze (`/productcost/sales-order-versions`).
The roll-up page offers the valuation view (Legal, Group, ProfitCenter) and shows the costing
sheet subtotals. From slice 5 the cost components (`/productcost/cost-components`) and cost
groups (`/productcost/cost-groups`) pages list and create the stored cost structure, and the
costing versions page lists the stored versions (status filter, next-page cursor), creates them
and blocks, unblocks, activates or closes them under their version counter; it replaced the
session-only version list of slice 3.

## Slice 3 (stateless)

- **Costing sheet Basis and Total nodes** (FR-39.2): a `Basis` node reports the sum of its
  `basis` components (rolled ones too with `includeRolled`) at its position in the sheet; a
  `Total` node sums the earlier `Basis` or `Total` nodes named in `refs`; a `Surcharge` with
  `basisRef` takes such a subtotal as its basis instead of `basis`. Subtotals are returned per
  node id in `result.subtotals` and add nothing to the split. Node ids are unique in a sheet,
  `component` is required on Surcharge and Rate and not allowed on Basis and Total, and every
  reference must name an earlier subtotal node; the API refuses violations with 400 and the
  domain fails an item that reaches it anyway with `CST-0708` (new code).
- **Group valuation view** (FR-39.17, BR-39.15, AC-39.14): `options.view` = `Legal`
  (default) or `Group`. In the Group view a purchased item flagged `intercompany` costs its
  supplier's Legal-view cost (`legalCost`, already converted at the receipt's rate) instead of
  the intercompany price, both when it is rolled and when a mode takes it at its active
  standard. Without `legalCost` the price applies and `CST-0761` warns (`groupCostUnknown`).
  `ProfitCenter` is built in slice 4 (below). A comparison can roll the same selection in
  different views (the valuation view comparison).
- **Version lifecycle** (FR-39.3, BR-39.2): `POST /v1/costing/versions:transition`
  (`CostingMaintain`) blocks, unblocks or closes a version: Open ⇄ Blocked, Active → Closed.
  `POST /v1/costing/versions:activate` (`CostingActivate`) moves Open or Blocked to Active;
  the maintain route refuses `activate` because the two duties are segregated (section
  7.39.17). Simulation versions cannot activate (`CST-0711`). `SpecifiedVersion` fallback
  needs a fallback version other than the version itself (`CST-0716`). Other moves are
  `CST-0715`. CST-0715 and CST-0716 are new codes. Activation here changes the status only;
  the revaluation posting is still the preview (`/activations:preview`).
- **Standard cost conversion checklist** (FR-39.12): `POST /v1/costing/conversions:validate`
  returns the blockers and the revaluation preview per position
  (on-hand × standard − actual value, rounded to the currency's minor unit; the journal equals
  the sum of the rows, AC-39.10). Negative on-hand (`CST-0732`), open in-transit transfers
  (`CST-0733`) and started production orders (`CST-0734`) are errors when their blocker is in
  `blockers` (all three when it is omitted, the parameter default) and warnings otherwise.
  The inventory close (`closeThrough` = the day before the conversion date, BR-39.11) is a
  later checklist step, so validation reports it as `closeComplete` with a `CST-0731`
  warning. `POST /v1/costing/conversions:advance` records the next step (`select`, `roll`,
  `validate`, `preview`, `close`, `activate`) for the calling user at the server's time:
  `validate` refuses a blocked report (`CST-0735`), `close` refuses a missing close
  (`CST-0731`), `activate` refuses either, and an out-of-order step is `CST-0736`. The
  checklist sent must be the steps so far in order, each with a user; the run status is
  derived from it. CST-0733 to CST-0736 are new codes.

The service stays stateless: the caller sends the current version or checklist and receives
the next state. Nothing is stored and no event is published until the persistence slice.

## Slice 4 (stateless)

- **ProfitCenter valuation view** (FR-39.17, `options.view` = `ProfitCenter`): a purchased
  item flagged `profitCenterSource` (supplied by another profit center of the same legal
  entity) costs its `transferPrice` (the transfer-price table entry for the profit-center
  pair) or, without one, the supplying profit center's cost `legalCost` plus `costPlusPct`
  percent, rounded to six decimals. This applies when the item is rolled and when a mode takes
  it at its active standard. With neither basis the item fails with `CST-0762` (new code), and
  a parent that takes it at its active standard fails with the same code. Items without the
  flag keep their price; the Legal and Group views ignore the flag. `profitCenterSource`,
  `transferPrice` and `costPlusPct` are only allowed on Purchased items, and an item cannot be
  both `intercompany` and `profitCenterSource`.
- **Ad-hoc estimates** (`POST /v1/costing/estimates`, FR-39.13, `CostingCalculate`): a
  costing model (`code`, `characteristics` with an optional `default`, and `lines` of type
  `Item`, `Activity`, `Subcontract` or `Overhead`) is priced for a `Quote`, `InternalOrder` or
  `ProjectTask`. Each line's `quantityFormula` and optional `priceFormula` are arithmetic over
  numbers and characteristic names (`+ - * /`, parentheses, unary minus; at most 500
  characters and 32 levels of nesting). A line without `priceFormula` takes the price of its
  `ref` from `prices` (the prices resolved from the selected costing version; `source`
  `Fallback` warns `CST-0703`, a missing price is `CST-0702`). Line amounts are quantity × price
  at six decimals; the costing `sheet` is applied to the split (`routingHours` drives Hour
  rates), and the split and total are rounded to the currency's minor unit with the residual
  to DM, so the split sums to the total. A characteristic without a value or default, a value
  for a characteristic the model does not have, a bad formula, division by zero, or a negative
  quantity or price is `CST-0764` (new code, 422). The formula grammar is local to this
  service until the wave-38 grammar (FR-38.17) is shared.
- **Sales order estimate versions** (FR-39.16, BR-39.14, `CostingCalculate`):
  - `POST /v1/costing/sales-order-estimates:refresh` costs the line like
    `/sales-order-estimates` and versions the result against `configHash`. The current version
    is the highest that is not Superseded. The same (`lineId`, `configHash`), the PRD's
    idempotency key, returns it unchanged (`changed: false`). A new hash marks every Pending and
    Frozen version Superseded, appends a Pending version, and warns `CST-0751` when a version was
    superseded. A roll that fails is still versioned, so the line has no usable estimate until
    the next refresh.
  - `POST /v1/costing/sales-order-estimates:freeze` freezes the current version at line
    confirmation. Freezing a Frozen version returns it unchanged. No current version, or one
    whose roll failed, is `CST-0763` (new code, 422): the "estimate missing at confirmation"
    alert of the refresh job.
  - The caller sends the versions it holds; versions of another line, duplicate version
    numbers or an unknown status are 400.

## Slice 5 (stored cost structure and costing versions)

The first persisted records. Every read and write runs in a tenant transaction
(`dbx.WithTenantTx`), so row-level security applies (tenant policy on every table, plus the
legal-entity scope on `costing_version`). Every create takes a required `Idempotency-Key`
(at most 200 characters) recorded in `idempotency_keys` with a hash of the command and its
body: a repeated key with the same body returns the stored response with
`Idempotent-Replayed: true` and writes nothing; a key reused with another body, or on another
command, is `idempotency_key_reuse` (422). Every change writes its event to the outbox in the
same transaction (see `events/asyncapi.yaml`).

- **Cost components** (FR-39.1): `GET` and `POST /v1/costing/cost-components`. A code is 1–20
  upper-case letters, digits or hyphens (`DM`, `IND-MAT`), with a name (≤ 120), a cost group
  type (`DirectMaterial`, `DirectManufacturing`, `Indirect`, `Undefined`) and a behaviour
  (`Fixed` or `Variable`, default `Variable`). At most 20 per tenant (`CST-0790`, checked under
  a per-tenant advisory lock so concurrent creates cannot exceed it); a duplicate code is
  `CST-0791` (409). Event `dev.passage.costing.costcomponent.created.v1`.
- **Cost groups** (FR-39.1): `GET` and `POST /v1/costing/cost-groups`. Same code, name, type and
  behaviour rules, and `componentId`, the cost component the group's costs are split into
  (`CST-0794`, 422, when it is not a component of the tenant). At most 50 per tenant
  (`CST-0792`); a duplicate code is `CST-0793` (409). Event
  `dev.passage.costing.costgroup.created.v1`.
- **Costing versions** (FR-39.3): `POST /v1/costing/versions` creates an Open version for a
  legal entity with a code (≤ 30, unique per legal entity, `CST-0713`, 409), a type (default
  `Standard`, `costing.versionType`), a fallback principle (default `Active`,
  `costing.fallback`), the site scope (`siteIds`, unique, empty for every site) and
  `effectiveFrom`. `SpecifiedVersion` needs `fallbackVersionId`, a stored version of the same
  legal entity (`CST-0716`). A legal entity outside the caller's scope is `CST-0718` (403).
  `GET /v1/costing/versions` pages in creation order (`limit` 1–500, default 100; `after` is
  the previous page's `next`) and filters by `legalEntityId`, `status` and `type`;
  `GET /v1/costing/versions/{id}` reads one (`CST-0717`, 404). Event
  `dev.passage.costing.version.created.v1`.
- **Lifecycle** (BR-39.2): `POST /v1/costing/versions/{id}/transition` with `{action, version}`
  blocks, unblocks or closes (CostingMaintain); `POST /v1/costing/versions/{id}/activate` with
  `{version}` activates (CostingActivate, segregated). `version` is the counter the caller read;
  a stale one is `CST-0714` (409) and changes nothing. The rules are those of slice 3
  (`CST-0711`, `CST-0715`). Activation stamps `activatedAt`. No pending prices are stored yet,
  so activating a version revalues and posts nothing (D6). Event
  `dev.passage.costing.version.transitioned.v1` with the action and both statuses.
- The slice-3 stateless routes (`/versions:transition`, `/versions:activate`) still serve
  callers that carry a version themselves.
- Without a store (no database) the stored routes answer `CST-0799` (503).

Migration `0002_cost_structure.sql` adds `cost_group`, `idempotency_keys` and `outbox`
(tenant_id first, RLS forced). Store tests run against Postgres with
`PASSAGE_TEST_DATABASE_URL`; with `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS)
they also run as the application role and check tenant and legal-entity isolation.

## Layout

```
cmd/productcost/        entrypoint: dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> register -> serve
internal/domain/        roll-up, planner, mass roll, variances, pricing, mixed cost, postings,
                        sales estimate and its versions, ad-hoc estimates and their formula
                        grammar, comparison, reconciliation, version lifecycle and
                        conversion checklist (pure, no I/O)
internal/api/           HTTP handlers, DTOs, validation (commercial.go holds the slice-2 routes,
                        lifecycle.go the slice-3 version and conversion routes, estimates.go
                        the slice-4 ad-hoc and sales order estimate version routes, persist.go
                        the slice-5 stored routes)
internal/store/         Postgres repository for cost components, cost groups and costing
                        versions (pgx, tenant transactions, idempotency, outbox)
internal/events/        event types and payloads (events_test.go checks events/asyncapi.yaml)
migrations/             costing schema (costing_version, cost_calculation, cost_component_split,
                        cost_component, variance_entry; from 0002 cost_group, idempotency_keys,
                        outbox) with RLS; migrations_test.go applies it and checks RLS when a
                        database is set
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for the slice-5 events
charts/productcost/     Helm chart
```

## Run

```
go run ./cmd/productcost            # requires DATABASE_URL for dbx.Connect
go test ./...                       # runs without a database; DB tests skip
PASSAGE_TEST_DATABASE_URL=... go test ./...   # enables testkit-backed tests
PASSAGE_TEST_APP_DATABASE_URL=...             # plus: store tests as the application role (RLS)
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
| CST-0708 | item error | costing sheet node of an unknown type, or a reference to a node that is not an earlier Basis or Total node (new code) |
| CST-0711 | 422 | simulation versions cannot be activated |
| CST-0715 | 422 | version transition not allowed from the current status (new code) |
| CST-0716 | 422 | `SpecifiedVersion` fallback without a fallback version, or pointing to itself (new code) |
| CST-0720 | 422 | variance identity failed for the order (residual above the rounding bound) |
| CST-0731 | 422 / warning | inventory close through the day before the conversion date is required (warning in validation; 422 on the close and activate steps) |
| CST-0732 | diagnostic | negative on-hand (error when the `NegativeOnHand` blocker is enabled) |
| CST-0733 | diagnostic | open in-transit transfers (error with `InTransit`; new code) |
| CST-0734 | diagnostic | production orders started but not ended (error with `StartedOrders`; new code) |
| CST-0735 | 422 | conversion checklist blocked by an enabled blocker (new code) |
| CST-0736 | 422 | conversion checklist step out of order, or checklist complete (new code) |
| CST-0741 | 422 | mixing ratios do not total 100.00% |
| CST-0751 | warning | sales order estimate superseded by a configuration change |
| CST-0761 | warning | Group view: no supplier Legal-view cost for an intercompany item; intercompany price used |
| CST-0762 | item error | ProfitCenter view: no transfer price and no cost-plus basis for a profit-center item (new code) |
| CST-0763 | 422 | no usable sales order estimate to freeze (new code) |
| CST-0764 | 422 | ad-hoc estimate refused: invalid model, formula or characteristic value, or a negative quantity or price (new code) |
| CST-0713 | 409 | the legal entity already has a costing version with this code (new code) |
| CST-0714 | 409 | the costing version changed since the caller read it: the `version` in the request is stale (new code) |
| CST-0716 | 422 | also: the `SpecifiedVersion` fallback version does not exist or belongs to another legal entity |
| CST-0717 | 404 | costing version not found in the caller's tenant and legal-entity scope (new code) |
| CST-0718 | 403 | the legal entity is outside the caller's legal-entity scope (new code) |
| CST-0790 | 422 | the tenant already holds 20 cost components (new code) |
| CST-0791 | 409 | a cost component with this code already exists (new code) |
| CST-0792 | 422 | the tenant already holds 50 cost groups (new code) |
| CST-0793 | 409 | a cost group with this code already exists (new code) |
| CST-0794 | 422 | the cost group names a cost component that does not exist (new code) |
| CST-0799 | 503 | the stored routes were called without a database (new code) |
| idempotency_key_reuse | 422 | the Idempotency-Key was already used with a different request (platform convention) |

Problem codes for the mass roll, the activation preview, the postings, the comparison and
the reconciliation are not in the catalog; their failures are per-item diagnostics or
request validation (COM-0001).

## Wave reference

PSG-39 (Wave 39), phase 7. Depends on PSG-38. Full list of deferred deliverables in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).
