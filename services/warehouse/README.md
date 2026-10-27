# warehouse (wave 31 — Advanced Warehouse Management, slice 1)

Directed warehouse work: location hierarchy, license plates, and the location
directive engine that decides where a put-away or pick line goes, with a
rule-by-rule trace. PRD: §6.31 of `docs/prd/phase-6-logistics.md`; ticket PSG-31.

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
replenishment, loads, receiving, and the rest of the wave. See `NOT-IMPLEMENTED.md`.

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

Trace reasons name the rejecting rule in plain text, for example
`capacity: lps 1/1` (AC-31.2) and `mixing: holds NUT-M8`. Later slices add
`WMS-0102` (capacity exceeded on confirmation), `WMS-0103` (mixing) and
`WMS-0110` (illegal transition) as HTTP responses once work is persisted.
