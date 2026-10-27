# planning (wave 22, PSG-22, slices 1 and 2)

Master planning for Passage: nets demand against supply level by level through
the BOM and writes planned purchase, production and transfer orders, each pegged
to the demand it serves, with action and delay messages.

Slice 1 built the pure netting engine, a static plan run that persists a
versioned, hashed result, the explain tree, and the message list. Slice 2 adds
the time fences of BR-22.4, planned-order maintenance (change, approve, delete
under If-Match, FR-22.11), and forecast cells with reduction (FR-22.8) that a
run can net as forecast demand. What is not built yet is listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/planning/          entrypoint: dbx.Connect -> dbx.Migrate (MIGRATE_ON_START=true) -> httpx.NewRouter -> register -> httpx.Serve
internal/domain/       pure engine: low-level codes, lot sizing, netting, pegging, messages, delay propagation, plan hash
internal/store/        Postgres repository (pgx); a plan run is one transaction with its outbox event
internal/api/          HTTP handlers under /v1/planning, RFC 9457 problems, /readyz with a database ping
internal/events/       plan.completed.v1 type and payload
migrations/            0001_init.sql (plan, plan_version, planned_order, pegging, action_message, outbox; RLS on all)
                       0002_forecast_and_maintenance.sql (planned_order version/vendor/deleted, forecast_model, forecast_line; RLS)
api/openapi.yaml       OpenAPI 3.1 for every exposed endpoint (kept in step by TestOpenAPI_MatchesRoutes)
events/asyncapi.yaml   AsyncAPI 3 for every event published
charts/planning/       Helm chart (deployment, service, servicemonitor toggle)
Dockerfile             distroless static image, built from the repository root
```

## Engine rules implemented (BR-22.1)

- Low-level codes from the BOM graph; a cyclic BOM is rejected (`PLAN-0004`).
- Netting per item in LLC order, requirements sorted by date, then priority, then ref.
- Coverage codes: requirement (lot-for-lot), period (one order per `periodDays`
  window for the window's demand less the supply already scheduled in it),
  min/max (reorder to max when projected stock, including planned supply in
  transit, is below min; checked at today even with no events), manual
  (shortage messages only).
- Safety stock with policy `today` (BR-22.5): a demand at today; its planned
  order is dated today + lead time and pegged to the safety demand at once.
- Lot sizing (FR-22.4): minimum, standard, multiple, then maximum (split into
  ⌈qty/max⌉ orders, at most 1,000 splits, `PLAN-0005`).
- Lead time offsets in calendar days; delivery = requirement date − receipt
  margin. An order whose order date falls before today gets futures date
  today + lead time (BR-22.1 step 7).
- Delay propagation (BR-22.2): after netting, delays run upward through the
  pegging, from the highest LLC down. A late component pushes the start of the
  planned production order it is pegged to, and a late planned order raises a
  delay message on the sales line or forecast it covers. AC-22.1 is a unit test.
- Pegging is FIFO by requirement date then priority (sales 1 > forecast 2 >
  safety 3 > dependent 4); every planned order's quantity is fully pegged
  (surplus is pegged to a `surplus` line).
- Existing receipts (BR-22.1 step 10): a receipt earlier than its earliest
  pegged demand by more than `positiveDays` gets Postpone (0 = unlimited, AX
  semantics, open question 2 of 4.22.12). `negativeDays` follows AX: netting
  waits for a receipt due within `negativeDays` after a shortage instead of
  planning a new order, and that late receipt gets Advance to the demand date.
  Read this way, step 10's "later than demand by more than negative_days" is
  the case that gets a new planned order rather than an Advance message.
- Time fences (BR-22.4), per item in days from the plan date: a requirement
  inside `freezeFenceDays` gets no planned order and what on-hand and
  receipts do not cover is a shortage message; Advance/Postpone only for
  receipts inside `actionFenceDays`; delay messages only for requirements
  (the late order's delivery date, or the late demand's date) inside
  `futuresFenceDays`. Futures dates still propagate outside the fence. 0 means
  no freeze and unlimited action and futures fences.
- Forecast demand (FR-22.8): with `forecastModel` on the run, the model's
  reduced forecast at the site is added as `forecast` demand (priority 2) of
  the snapshot's items at each period start; periods that ended before the
  plan date are skipped and earlier starts are due today.
- Plan hash: sha256 over orders, pegging and messages, each sorted by a total
  order over all fields (FR-22.14). Property tests check determinism, 100%
  pegging of planned orders, and that no non-manual demand is left short.

## Run and test

```
cd services/planning
go test ./...                # domain + API; store tests skip without a database
PASSAGE_TEST_DATABASE_URL=postgres://admin@host/db \
PASSAGE_TEST_APP_DATABASE_URL=postgres://app@host/db go test ./...   # store tests as the RLS-bound app role
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/planning   # HTTP_ADDR (:8080), ADMIN_ADDR (:9090, /metrics)
```

Store tests cover persistence, replay and key reuse, version numbering, the
outbox event, tenant isolation under RLS, the 409 on a concurrent run,
planned-order maintenance (versions, date and vendor rules, delete, superseded
versions) and forecast cells (reduction, recompute on a principle change,
forecast demand for a run).

## Planned-order maintenance and forecasts

- `GET /planned-orders/{id}` returns the order's `version` in the body and as
  `ETag`. `PATCH` and `DELETE` require `If-Match` with it: missing is 428
  `precondition_required`, stale is 412 `conflict_version`. Only open orders
  (unprocessed or approved) of the plan's current version can be maintained.
  `status: approved` approves; firming is a separate command (not built yet).
  Delete keeps the row with status `deleted`, so the version's pegging still
  resolves; lists leave it out.
- `PUT /forecasts/{modelCode}/lines` upserts up to 1,000 cells (each replaces
  the cell of its item, site and period start) under `If-Match` on the model;
  a model that does not exist yet has ETag `"0"`. Reduction principles:
  `none`, `percent` (keep 100 − percent), `transactions` (forecast − sales in
  the period, never below 0, BR-22.3), `transactions-dynamic` (sales above a
  period's forecast also consume the following periods). Until sales orders
  (w27) feed the planning service, `salesQty` per cell is entered with it.

## API

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/planning/plans/{planCode}:run` | `planning.run` (Idempotency-Key required) |
| GET | `/v1/planning/plans/{planCode}/versions?limit=` | `planning.read` |
| GET | `/v1/planning/plans/{planCode}/versions/{number}` | `planning.read` |
| GET | `/v1/planning/planned-orders?planCode=&status=&itemId=&limit=` | `planning.read` |
| GET | `/v1/planning/planned-orders/{id}` | `planning.read` |
| PATCH | `/v1/planning/planned-orders/{id}` | `planning.order.maintain` (If-Match required) |
| DELETE | `/v1/planning/planned-orders/{id}` | `planning.order.maintain` (If-Match required) |
| GET | `/v1/planning/planned-orders/{id}/pegging?depth=` | `planning.read` |
| GET | `/v1/planning/messages?planCode=&action=&limit=` | `planning.read` |
| GET | `/v1/planning/forecasts/{modelCode}/lines?itemId=&siteId=&limit=` | `planning.read` |
| PUT | `/v1/planning/forecasts/{modelCode}/lines` | `planning.forecast.maintain` (If-Match required) |

The web module calls these under `/api/v1/planning/...`; the BFF strips `/api`.
A run is synchronous in slice 1: the engine runs in the request and the store
writes the version, its rows and `plan.completed.v1` in one transaction, then
swaps the current pointer.

## Error codes

| HTTP | Code | Meaning |
|---|---|---|
| 400 | `COM-0001` | Request failed validation (field errors in `errors`) |
| 401 | `AUTH-0001` | No or malformed tenant |
| 403 | `AUTH-0003` | Missing duty |
| 404 | `PLAN-VERSION-NOT-FOUND` | No such version for the plan |
| 404 | `PLAN-ORDER-NOT-FOUND` | No such planned order |
| 404 | `PLAN-FORECAST-NOT-FOUND` | No such forecast model (422 when a run names it) |
| 409 | `PLAN-0001` | `PLAN_ALREADY_RUNNING`: a run of the plan is in progress |
| 409 | `PLAN-ORDER-NOT-EDITABLE` | The planned order is firmed, deleted or not in the current version |
| 412 | `conflict_version` | If-Match is not the current version of the order or forecast model |
| 422 | `PLAN-ORDER-INVALID` | Order date after delivery date, or a vendor on a non-purchase order |
| 428 | `precondition_required` | If-Match missing on a maintenance write |
| 422 | `PLAN-0004` | BOM cycle; low-level codes cannot be assigned |
| 422 | `PLAN-0005` | A requirement would split into more than 1,000 orders |
| 422 | `PLAN-INPUT-INVALID` | Snapshot rejected by the engine (unknown coverage or order type, duplicate or unknown item); `detail` names it |
| 422 | `idempotency_key_reuse` | Same Idempotency-Key with a different plan or snapshot |

## Wave reference

PSG-22 (Phase 4, Manufacturing and Planning). PRD section 4.22. Depends on PSG-21
(production orders, route lead times), PSG-17 (purchase orders), PSG-16 (on-hand),
PSG-9 (BOM and low-level codes), PSG-27 (sales demand, later).

## Dependencies

Only the platform kernel and the modules it already pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid, and `pgregory.net/rapid` for property tests).
