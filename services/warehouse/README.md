# warehouse (wave 31 — Advanced Warehouse Management, slices 1–4)

Directed warehouse work: location hierarchy, license plates, and the location
directive engine that decides where a put-away or pick line goes, with a
rule-by-rule trace. PRD: §6.31 of `docs/prd/phase-6-logistics.md`; ticket PSG-31.

## What slice 4 adds (dock appointments and ASN receiving)

- `migrations/0002_docks_receiving.sql`: `dock_door` (code unique per
  warehouse), `dock_appointment` (direction, reference, a half-open window on
  the 15-minute grid of at most 24 hours, checked by the table too),
  `receipt` (one per ASN reference and warehouse, tolerance, status, version)
  and `receipt_line` (one row per ASN pallet: LP id, item, PO line reference,
  expected quantity, weight and volume, and once received the quantity, the
  variance, the discrepancy flag and reason, the put-away work, who and when).
  Forced row-level security on all four.
- `internal/domain/dock.go` (FR-31.8): `OnSlotGrid`, `Overlaps` (half open:
  09:00–09:45 and 09:45–10:00 do not overlap) and `CheckDoorSlot`, which
  refuses an overlap on the same door with WMS-0108 (AC-31.8
  DOOR_SLOT_OVERLAP).
- `internal/domain/receiving.go` (FR-31.9, BR-31.10): `Receipt.Receive`
  records what arrived on one pallet. The variance is received − ASN and
  that difference as a % of the ASN quantity (4 decimals); a variance beyond
  the receipt's tolerance (default ±2%, 0–20) is a discrepancy with the
  WMS-0107 text, compared exactly before rounding. The actual quantity is
  received either way. A pallet is received once. `PutAwayLine` makes the
  pallet one PURCHASE_PUTAWAY line of one LP, with weight and volume scaled to
  the received quantity and the LP id, receipt id and ASN line as attributes.
- `internal/store`: `CreateDoor`, `ListDoors`, `CreateAppointment` (locks the
  door row, then checks overlaps, so concurrent bookings of one door are
  serialized), `ListAppointments`, `RegisterASN`, `ListReceipts`,
  `GetReceipt` and `ReceiveLine` (row lock on the receipt, optional If-Match
  receipt version; creates the pallet's put-away work through the same
  planning path as `POST /work`, so it respects open reservations; enqueues
  `work.created` and `receipt.registered` in the same transaction). Every
  command runs once per Idempotency-Key.
- Event `dev.passage.wms.receipt.registered.v1` (topic `passage.wms.receipt`):
  one per received pallet with `receipt_id`, `po_line_refs`, `lp_ids` and
  `items` (qty by item, the PRD's fields) plus the ASN quantity and variance.
- Web: Receiving (receipt list with pallets received and discrepancies,
  status filter), Register ASN (one row per pallet), the receipt page (receive
  each pallet with the quantity that arrived; shows the variance, the WMS-0107
  warning and a link to the put-away work), and Dock schedule (a day's
  appointments in local time with a door filter, the doors with Add door) and
  Book a dock appointment.

| Route | Duty (PRD duty) |
|---|---|
| `POST /v1/warehouse/dock-doors` | `warehouse.setup.maintain` (WMS_SETUP_MAINTAIN) |
| `GET /v1/warehouse/dock-doors?warehouseId=` | `warehouse.inquiry.view` (WMS_INQUIRY) |
| `POST /v1/warehouse/dock-appointments` | INBOUND `warehouse.receipt.register` (WMS_RECEIVE), OUTBOUND `warehouse.shipment.confirm` (WMS_SHIP_CONFIRM) |
| `GET /v1/warehouse/dock-appointments?warehouseId=&doorId=&from=&to=` | `warehouse.inquiry.view` |
| `POST /v1/warehouse/receipts:from-asn` | `warehouse.receipt.register` (WMS_RECEIVE) |
| `GET /v1/warehouse/receipts?warehouseId=&status=&limit=&cursor=`, `GET /v1/warehouse/receipts/{id}` | `warehouse.inquiry.view` |
| `POST /v1/warehouse/receipts/{id}/lines/{n}:receive` | `warehouse.receipt.register` |

Receiving is two steps, as in AC-31.7: the ASN is registered with every
pallet EXPECTED, then each pallet is received as it is scanned at the dock.
A pallet that never arrives stays EXPECTED and is not a discrepancy.

## What slice 3 adds (persistence and directed work)

- `migrations/0001_init.sql`: `location` (capacity limits and current use as
  numeric columns), `location_stock` (on-hand by location and item, the
  stand-in for quants), `location_directive` (bands and actions as jsonb),
  `work`, `work_line`, `outbox` and `idempotency_keys`. Every table has
  `tenant_id` first and forced row-level security. Quantities are
  `numeric(20,6)`, weights and volumes `numeric(18,6)`.
- `internal/domain/execution.go`: `Plan` places each line with the directives
  and reserves what it takes for the following lines (BR-31.2); `Reserve`
  applies every open line of the warehouse before planning, so open PUT lines
  use room (weight, volume, LPs and their item for mixing and emptiness) and
  open PICK lines hold their stock. `Assign`, `ConfirmLine` (only by the
  assignee; quantity must equal the open quantity, over-pick tolerance 0%,
  WMS-0106), `ShortPick` (PICK lines only; remainder becomes a child OPEN work,
  AC-31.5), `Cancel` (with a reason; assignee, or anyone with the supervise
  duty), and `Location.Move`/`Issue` for the confirmed stock movement.
- `internal/store`: tenant-scoped transactions; every command runs once per
  `Idempotency-Key` (`once`: same key and request returns the stored answer,
  same key with another request is `idempotency_key_reuse`; a failed command
  releases its key). `AssignNext` first returns ASSIGNED work idle past
  `WMS_LOCK_TIMEOUT_MIN` to OPEN, then takes the best OPEN work, both with
  `FOR UPDATE SKIP LOCKED` (BR-31.8). Confirmation locks the work and the
  location rows and re-checks status, mixing and capacity (FR-31.1). Work events
  go to the outbox in the same transaction.
- `internal/api/execution.go`: the routes below, under `/v1/warehouse/` like
  the simulator. Work commands take an optional `If-Match` work version.
- Events `dev.passage.wms.work.created.v1` (new work, including a short-pick
  remainder) and `dev.passage.wms.work.completed.v1` (last open line confirmed
  or short picked), source `warehouse`, documented in `events/asyncapi.yaml`.
- Web (`apps/web/src/modules/warehouse`): Locations (list with capacity, use and
  stock; Add locations generates names from a LocationFormat such as
  A-01-02-03), Location directives (list; New directive), Work (list with status
  filter and "Assign next work to me"; Create work shows where each line went
  and why; work details confirm, short pick or cancel each line). The warehouse
  id travels in the `?warehouse=` URL parameter and is remembered per browser.

| Route | Duty (PRD duty) |
|---|---|
| `POST /v1/warehouse/locations:bulk` | `warehouse.setup.maintain` (WMS_SETUP_MAINTAIN) |
| `GET /v1/warehouse/locations?warehouseId=&zone=&limit=&cursor=` | `warehouse.setup.view` (WMS_SETUP_VIEW) |
| `POST /v1/warehouse/location-directives` | `warehouse.setup.maintain` |
| `GET /v1/warehouse/location-directives?warehouseId=&workType=&direction=` | `warehouse.setup.view` |
| `POST /v1/warehouse/work` (CreateWorkFromOrderLines) | `warehouse.work.supervise` (WMS_WORK_SUPERVISE) |
| `GET /v1/warehouse/work?warehouseId=&status=&limit=&cursor=`, `GET /v1/warehouse/work/{id}` | `warehouse.inquiry.view` (WMS_INQUIRY) |
| `POST /v1/warehouse/work:assign` | `warehouse.work.execute` (WMS_WORK_EXECUTE) |
| `POST /v1/warehouse/work/{id}/lines/{n}:confirm` | `warehouse.work.execute` |
| `POST /v1/warehouse/work/{id}:exception` (SHORT_PICK, CANCEL) | `warehouse.work.execute`; cancelling another user's work also needs `warehouse.work.supervise` |

Configuration: `DATABASE_URL` (without it only the simulator is served),
`MIGRATE_ON_START=true` to apply migrations at start, `WMS_LOCK_TIMEOUT_MIN`
(5–240, default 30).

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

Not yet built: waves, replenishment, license plates in the store, loads,
receipt without an ASN, sorting, VAS, labor endpoints, and the rest of the
wave. See `NOT-IMPLEMENTED.md`.

## Run and test

```sh
cd services/warehouse
go test ./...          # domain and API tests; store and API database tests skip
go vet ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # adds the store and HTTP flow tests
PASSAGE_TEST_APP_DATABASE_URL=postgres://app@... # also runs the RLS isolation test (non-owner role)
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/warehouse
                       # HTTP_ADDR defaults to :8080, ADMIN_ADDR to :9090
```

Test fixtures: `testdata/directive-scenarios-100.json` holds the AC-31.1
table (100 inbound scenarios: 10 quantities × 10 occupancy layouts of the four
demo locations, with the expected location, directive, line and action derived
by hand from the demo directives). `internal/api` posts every scenario to the
simulate endpoint and checks the answer and the trace's winning step.
Outbound: `internal/domain/directive_outbound_test.go` holds 100 PICK scenarios. Wave release: `domain.ReleaseWave` / `Store.ReleaseWave` create one SALES_PICK work per order and min/max REPLENISHMENT work (priority 40, ahead of picking) in one transaction (unmet pick demand and open refills count toward the refill; the refill is cut to the destination's weight/volume room; the release locks the warehouse's locations; a pick that only the refill can cover is WAITING with `depends_on` the refill works, is never assigned, and becomes OPEN (event `work.released`) in the transaction that completes or cancels the last of them; a short-picked refill hands the wait to its remainder work; migration `0004_work_dependencies.sql`). `wave_test.go` files cover 500 orders and the 10k-lines/day simulation (`PASSAGE_WMS_SIM_10K=1` for the whole day). No route or wave record yet.
Property test: `location_test.go` checks that a location never exceeds its
capacity under random receive sequences (AC-31.9 for the capacity half).

Dependencies beyond the platform module: `pgregory.net/rapid` (tests only, as
the conventions allow), and `github.com/go-chi/chi/v5` and `github.com/jackc/pgx/v5`
(both via the platform). The `shopspring/decimal` package is used for
quantities and weights.

## Pallet identity, RFID and automation (#32)

`PUT/GET /v1/warehouse/gs1-settings` hold the tenant's GS1 company prefix; receiving a pallet whose ASN line names no license plate draws the next SSCC (under a row lock) and uses it as the plate. The put-away work line carries `lpId`, `sscc`, `lot` and `expiry`, and `GET /v1/warehouse/pallets/{sscc}/label` gives a device the same, so it prints the label with no second scan. `POST /v1/warehouse/rfid-reads:ingest` takes reader reads (EPC decoding in `internal/domain/epc.go`). `/v1/warehouse/automation/*` carries transport orders to a PLC's connector and the PLC's events back (an arrival confirms the work line). Duties `warehouse.rfid.ingest` and `warehouse.automation.execute`; migrations `0006` and `0007`.

## Shipped without a packing slip (#35, 35-1)

`POST /v1/warehouse/shipments/{id}:ship` (duty `warehouse.shipment.confirm`) records that the fulfillment shipment `{id}` left the
dock without a packing slip posted for it, and enqueues `dev.passage.wms.shipment.shipped.v1` (topic `passage.wms.shipment`) in the
same transaction. The EDI engine's outbound consumer sends the whole shipment's 856 or DESADV from it, as it does from fulfillment's
`asn.issued` for a slip. A shipment is confirmed once (migration 0008). `GET /v1/warehouse/shipments/{id}/shipped` reads it back.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| WMS-0101 | 200 simulate (`blocked: true`); on work, the line `reason` | No directive action yielded a location for the line (BLOCKED_NO_LOCATION). |
| WMS-0102 | 422 | Confirmed PUT would exceed the location's capacity. |
| WMS-0103 | 422 | Confirmed PUT would mix items in a location that forbids it. |
| WMS-0105 | 409 | Line already confirmed with another Idempotency-Key (BR-31.8). |
| WMS-0106 | 422 | Confirmed quantity above the open quantity (over-pick tolerance 0%). |
| WMS-0107 | 200 receive (`warnings`), and the pallet's `reason` | Received quantity beyond the receipt's ASN tolerance; the discrepancy is recorded and the quantity received (BR-31.10). |
| WMS-0108 | 409 | The door is already booked for an overlapping window (AC-31.8 DOOR_SLOT_OVERLAP). |
| WMS-RECEIPT-LINE-RECEIVED | 409 | The ASN pallet is already received (with another Idempotency-Key). |
| WMS-GS1-NOT-SET | 422 / 404 | A pallet needs an SSCC or license plate id the warehouse must generate and no GS1 company prefix is set (422 on receive; 404 reading the settings). |
| WMS-GS1-LOCKED | 409 | The GS1 company prefix cannot change once SSCCs were issued under it. |
| WMS-GS1-EXHAUSTED | 409 | The prefix has no serial reference left. |
| WMS-PALLET-NOT-FOUND | 404 | No pallet of this tenant has the SSCC. |
| WMS-EPC-INVALID | (in `rejected`) | A tag read that is not a 96-bit EPC of a scheme the warehouse decodes. |
| WMS-AUTO-ORDER-NOT-FOUND, WMS-AUTO-ORDER-EXISTS, WMS-AUTO-ORDER-STATE | 404, 409, 409 | Automation orders: unknown, a second order for the line, a report on an order past PENDING. |
| WMS-AUTO-LOCATION, WMS-AUTO-LOAD-UNIT | 422 | A location a telegram cannot name (more than 12 characters, or missing), or a line with no pallet SSCC. |
| WMS-0110 | 409 | Illegal work or line transition: confirming unassigned or finished work, a quantity below the open one (use a short pick), short picking a PUT line, cancelling after a confirmation, or a location status/stock that refuses the move. |
| WMS-DUPLICATE | 409 | Location name or door code taken in the warehouse, directive sequence taken for the work type and direction, or the ASN already registered in the warehouse, or the shipment already confirmed shipped. |
| WMS-VERSION-CONFLICT | 409 | `If-Match` is not the work's (or receipt's) current version. |
| WMS-NOT-ASSIGNEE | 403 | The work is assigned to another user (or unassigned, for a cancel without the supervise duty). |
| WMS-WORK-NOT-FOUND, WMS-RECEIPT-NOT-FOUND, WMS-LINE-NOT-FOUND | 404 | No such work or receipt in the tenant, or no such line on it. |
| WMS-SHIPMENT-NOT-FOUND | 404 | No ship confirmation for the shipment in the caller's legal entities. |
| WMS-DOOR-NOT-FOUND | 404 | No such dock door in the appointment's warehouse. |
| idempotency_key_reuse | 422 | The Idempotency-Key was used for another request (method, path, user and body). |
| COM-0001 | 400 | Malformed body, unknown field, missing Idempotency-Key or X-Passage-User, or failed field validation (`errors` names the fields). |
| AUTH-0001 | 401 | No tenant on the request, or (persisted routes) a tenant that is not a UUID. |
| AUTH-0003 | 403 | The route's duty is missing (for an appointment, the duty of its direction). |

Domain warnings returned by the domain functions (no endpoint serves them yet):
`WMS-0113` (supply under quality hold, cross-dock deferred, from `PlanReceipt`) and
`WMS-0118` (labor line excluded as outlier, from `LaborLine`).

Trace reasons name the rejecting rule in plain text, for example
`capacity: lps 1/1` (AC-31.2), `mixing: holds NUT-M8` and
`hazmat: class 3 vs 5.1 prohibited in FCS-02` (AC-31.14).

Metrics on the admin port: `warehouse_directive_simulations_total`,
`warehouse_directive_blocked_total`, `warehouse_work_created_total`,
`warehouse_work_blocked_total`, `warehouse_work_assignments_total`.
