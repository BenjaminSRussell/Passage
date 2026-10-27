# warehouse (wave 31 — Advanced Warehouse Management, slices 1–2)

Directed warehouse work: location hierarchy, license plates, and the location
directive engine that decides where a put-away or pick line goes, with a
rule-by-rule trace. PRD: §6.31 of `docs/prd/phase-6-logistics.md`; ticket PSG-31.

## What slice 2 adds (pure domain, plus the simulate request)

- `internal/domain/hazmat.go` (FR-31.19, BR-31.15): `HazmatProfile` per item
  (UN number, storage class, water hazard class, flash point, net explosive mass),
  a symmetric `SegregationMatrix` (allowed / separate / prohibited, unlisted pairs
  allowed), section quantity limits in kg, and per-location `FireSection`,
  `RackGroup` and `AllowedStorageClasses`. `EvaluateWithCompliance` rejects a PUT
  location that is prohibited for the class in its section, separate within its
  rack, over the section limit, or outside its allowed classes. Each rejection is
  a trace reason, for example `hazmat: class 3 vs 5.1 prohibited in FCS-02`.
- `internal/domain/crossdock.go` (FR-31.13, BR-31.11): `PlanReceipt` allocates a
  receipt to planned links first, then to released demand in the supply window
  (ordered by SHIP_DATE, ORDER_PRIORITY or FIFO_RELEASE, then ship date and line
  id). Supply not `ACCEPTED` is held (`WMS-0113`) and never cross-docked. The
  window is 0–336 h (`DefaultSupplyWindow` 48 h).
- `internal/domain/packaging.go` (FR-31.17, BR-31.13 determination): `PackagingSpec`
  with levels, `DetermineSpec` by most-specific match (item+customer, item,
  group+customer, group) and `RoundUpToLP`.
- `internal/domain/labor.go` (FR-31.18, BR-31.14): `Travel` (Manhattan or
  Euclidean), the `Formula` planned seconds (rounded to 0.1 s), `Actual` from
  timestamps with long pauses removed as indirect time (pauses must lie inside
  the line and not overlap), `LaborLine` (planned, actual and the 0.2×–5×
  outlier flag with its `WMS-0118` text) and `Performances` (Σ planned ÷ Σ actual
  × 100) per group.
- `POST /v1/warehouse/location-directives:simulate` accepts an optional
  `compliance` block (`hazmatProfiles`, `segregation`, `sectionLimitsKg`).
  Invalid data is a 400 on field `compliance`. The section quantity is the used
  weight of the section's locations that hold an item with a hazmat profile.
- Web: the simulator page (`apps/web/src/modules/warehouse`) has a hazmat
  compliance field and a "Load hazmat example" button that runs the AC-31.14
  scenario.
- Cross-dock, packaging and labor are domain functions only in this slice: no
  endpoint or screen calls them yet (see `NOT-IMPLEMENTED.md`).

## What slice 1 contains

- `internal/domain`: pure, I/O-free logic.
  - `location.go`: `Location`, `Capacity`, `Usage`, admission rules (status, mixing,
    capacity arithmetic per BR-31.2), location names from a format (`A-01-02-03`).
  - `directive.go`: `Directive`, bands (`from_qty <= qty < to_qty`), actions and
    strategies, and `Evaluate` (BR-31.1). Returns the chosen location, the winning
    directive, and a trace of every step.
  - `lp.go`: license plate ids (external up to 40 characters, SSCC-18 generation
    with GS1 mod-10 check digit), nesting depth ≤ 5 (BR-31.9, WMS-0104).
  - `work.go`: work status machine (BR-31.3) with `CheckTransition`.
  - `errors.go`: domain errors with stable WMS codes.
- `internal/api`: `POST /v1/warehouse/location-directives:simulate` (duty
  `warehouse.setup.view`). The PRD names the route
  `/wms/v1/location-directives:simulate` and the duty `WMS_SETUP_VIEW`; they
  follow the `/v1/<service>/` path and `<service>.<area>.<verb>` duty naming
  every Passage service uses. Stateless: the caller sends the line, the
  directives and the candidate locations. A blocked line is a 200 answer with
  `code: WMS-0101`, not an error.
- `cmd/warehouse`: process entrypoint with `/healthz`, `/readyz`, `/metrics`.
- `api/openapi.yaml`, `events/asyncapi.yaml` (no events published in this slice).
- `charts/warehouse`: Helm chart (one deployment, HTTP and admin ports, optional
  ServiceMonitor). No database or secret yet: the service has no store.
- Web: `apps/web/src/modules/warehouse` — the location directive simulator at
  `/warehouse` (nav duty `warehouse.setup.view`): a work-line form, directives
  and candidate locations as JSON (pre-filled with the BR-31.1 worked example),
  and the result with the rule trace as a table. It posts to
  `/api/v1/warehouse/location-directives:simulate`.

Rules worth knowing:

- Directive lines (quantity bands) of one directive must not overlap
  (BR-31.1 step 2 says they are mutually exclusive); an overlap is a 400 on the
  later band's `fromQty`. Every line needs at least one action, and directive
  and location ids must be unique in the request.
- A PUT candidate passes status (`BLOCKED_IN`, `BLOCKED`, `INACTIVE` refuse),
  the empty-location rule for `empty_location`, mixing, then capacity. A PICK
  candidate passes status (`BLOCKED_OUT`, `BLOCKED`, `INACTIVE` refuse) and
  holds at least the whole line quantity: splitting a pick line across
  locations is a later slice.
- Strategy ordering: `consolidate` puts locations already holding the item
  first; every strategy then orders by pick sequence and name. `fifo`, `lifo`,
  `fefo`, `match_packing_quantity`, `round_up_to_lp` and `outbound_staging` are
  accepted but need quant dates, packing specifications or staging locations
  to order differently, so they behave like pick-sequence ordering for now.
- A location's item count is derived from `stock`; the request carries weight,
  volume and LP count under `used`.
- Work states (BR-31.3, `CheckTransition`): work enters `BLOCKED_NO_LOCATION`
  only at creation, never from `OPEN`. `BLOCKED_NO_LOCATION → CANCELLED` is
  allowed although BR-31.3 does not name it, so a wave can drop a line no
  directive can place.

Not yet built: persistence, migrations, waves, work creation, assignment,
replenishment, loads, receiving, sorting, VAS, labor endpoints, and the rest of the
wave. See `NOT-IMPLEMENTED.md`.

## Run and test

```sh
cd services/warehouse
go test ./...          # domain and API tests; no database needed
go vet ./...
go run ./cmd/warehouse # HTTP_ADDR defaults to :8080, ADMIN_ADDR to :9090
```

Test fixtures: `testdata/directive-scenarios-100.json` holds the AC-31.1
table (100 inbound scenarios: 10 quantities × 10 occupancy layouts of the four
demo locations, with the expected location, directive, line and action derived
by hand from the demo directives). `internal/api` posts every scenario to the
simulate endpoint and checks the answer and the trace's winning step.
Property test: `location_test.go` checks that a location never exceeds its
capacity under random receive sequences (AC-31.9 for the capacity half).

Dependencies beyond the platform module: `pgregory.net/rapid` (tests only, as
the conventions allow) and `github.com/go-chi/chi/v5` (via the platform). The
`shopspring/decimal` package is used for money-like quantities and weights.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| WMS-0101 | 200 (in the body: `blocked: true`) | No directive action yielded a location for the line (BLOCKED_NO_LOCATION). |
| COM-0001 | 400 | Malformed body, unknown field, or failed field validation (`errors` names the fields). |
| AUTH-0001 | 401 | No tenant on the request. |
| AUTH-0003 | 403 | Duty `warehouse.setup.view` missing. |

Domain warnings returned by the domain functions (no endpoint serves them yet):
`WMS-0113` (supply under quality hold, cross-dock deferred, from `PlanReceipt`) and
`WMS-0118` (labor line excluded as outlier, from `LaborLine`).

Trace reasons name the rejecting rule in plain text, for example
`capacity: lps 1/1` (AC-31.2), `mixing: holds NUT-M8` and
`hazmat: class 3 vs 5.1 prohibited in FCS-02` (AC-31.14). Later slices add
`WMS-0102` (capacity exceeded on confirmation), `WMS-0103` (mixing) and
`WMS-0110` (illegal transition) as HTTP responses once work is persisted.
