# planning (wave 22, PSG-22, slices 1 to 10)

Master planning for Passage: nets demand against supply level by level through
the BOM and writes planned purchase, production and transfer orders, each pegged
to the demand it serves, with action and delay messages.

Slice 1 built the pure netting engine, a static plan run that persists a
versioned, hashed result, the explain tree, and the message list. Slice 2 adds
the time fences of BR-22.4, planned-order maintenance (change, approve, delete
under If-Match, FR-22.11), and forecast cells with reduction (FR-22.8) that a
run can net as forecast demand. Slice 3 adds stateless planning calculators.
Slice 4 adds firming into target orders with grouping (FR-22.10), Cancel and
Decrease messages beyond the coverage time fence (FR-22.7), and the version
purge (`PLANNING_MAX_VERSIONS`). Slice 5 adds the site work calendar on a run
(FR-22.5, BR-22.8), the stored MD04-style net requirements view with
`GET /net-requirements` (FR-22.12, AC-22.10), and the `plan.started.v1` and
`message.raised.v1` events. Slice 6 adds Increase messages for shortages inside
the freeze fence (FR-22.7), the forecast consensus workflow with segregation of
duties and `forecast.published.v1` (FR-22.9, 4.22.17), the dynamic-plan reach
set (BR-22.6, domain only) and the forecast history window in `forecast-py`.
Slice 7 adds coverage groups as persisted setup (FR-22.2), auto-firming of
planned orders inside the firming time fence during a run (BR-22.4), and
resumable batch generation in `forecast-py` (FR-22.9, 4.22.16). Slice 9 adds
item coverage as persisted setup. Slice 10 adds DDMRP buffer profiles, stored
buffers with recalculation and the buffer status inquiry (FR-22.16), and quota
arrangements with their sources (FR-22.17), each with web screens.
What is not built yet is listed in
[NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

## Layout

```
cmd/planning/          entrypoint: dbx.Connect -> dbx.Migrate (MIGRATE_ON_START=true) -> httpx.NewRouter -> register -> httpx.Serve
internal/domain/       pure engine: low-level codes, lot sizing, netting, pegging, messages, delay propagation, plan hash,
                       work calendar (calendar.go), net requirements view (netreq.go), forecast consensus
                       (consensus.go), dynamic-plan reach set (reach.go), coverage group setup rules (coveragegroup.go),
                       item coverage setup rules (itemcoverage.go), buffer profile and quota arrangement setup rules
                       (buffer_profile.go, quota_arrangement.go);
                       calculators: ddmrp.go, quota.go, strategy.go, shelflife.go, projection.go
internal/store/        Postgres repository (pgx); a plan run is one transaction with its outbox events
internal/api/          HTTP handlers under /v1/planning, RFC 9457 problems, /readyz with a database ping
internal/events/       plan.started.v1, plan.completed.v1, message.raised.v1, message.applied.v1, message.dismissed.v1, plannedorder.firmed.v1 and forecast.published.v1 types and payloads
internal/forecastclient/ HTTP client of forecast-py for POST /forecasts/{modelCode}:generate (slice 28, FORECAST_URL, flag planning.statistical-forecast); forecast.generated.v1
migrations/            0001_init.sql (plan, plan_version, planned_order, pegging, action_message, outbox; RLS on all)
                       0002_forecast_and_maintenance.sql (planned_order version/vendor/deleted, forecast_model, forecast_line; RLS)
                       0003_firming.sql (firming_batch with RLS, firmed target on planned_order, cancel/decrease actions)
                       0004_net_requirements.sql (net_requirement per version, item and day; RLS)
                       0005_increase_and_consensus.sql (increase action, forecast_model consensus status, forecast_line writer)
                       0006_coverage_groups.sql (coverage_group setup with the FR-22.2 defaults and checks; RLS)
                       0007_item_coverage.sql (item_coverage per item, site and planning scope; RLS)
                       0008_buffer_profile_ddmrp.sql (buffer_profile, ddmrp_buffer per item/site/warehouse, item_coverage profile FK; RLS)
                       0009_quota_arrangement.sql (quota_arrangement, quota_source; RLS)
api/openapi.yaml       OpenAPI 3.1 for every exposed endpoint (kept in step by TestOpenAPI_MatchesRoutes)
events/asyncapi.yaml   AsyncAPI 3 for every event published
charts/planning/       Helm chart (deployment, service, servicemonitor toggle)
Dockerfile             distroless static image, built from the repository root
```

## Engine rules implemented (BR-22.1)

- Low-level codes from the BOM graph; a cyclic BOM is planned without the lines that close a cycle, each raising a `bom-cycle` warning (`PLAN-0004`, slice 33).
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
- Lead time offsets: delivery = requirement date − receipt margin, order date
  = delivery − lead time. An order whose order date falls before today gets
  futures date today + lead time (BR-22.1 step 7). Without a `calendar` on the
  run these are calendar days. With one (closed weekdays and closed dates of
  the site, FR-22.5, BR-22.8) margins, lead times, futures dates and the
  delay propagation count working days; a demand on a closed day moves
  forward and a receipt or delivery on a closed day moves backward to the
  nearest open day. Period, positive and negative days stay calendar days.
- Net requirements view (FR-22.12): per item and day with movement, gross
  requirements (demand, dependent demand, safety stock at today), scheduled
  receipts, planned receipts, PAB(t) = PAB(t−1) + SR + PR − GR from on-hand,
  and the day's net requirement max(0, GR − max(PAB(t−1), 0) − SR); past-due
  lines count at today. Stored per version (`net_requirement`); property tests
  reconcile it with the run (closing PAB = on-hand + Σ supply − Σ demand) and
  check PAB never goes negative where coverage must meet every demand.
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
- Cancel and Decrease (FR-22.7, BR-22.1 step 10): a receipt dated at or beyond
  `coverageFenceDays` from the plan date whose quantity demand does not peg
  gets Cancel (nothing pegged; qty = the receipt) or Decrease (qty = the
  unpegged part), inside the action fence. Inside the coverage fence surplus
  stays as it is (BR-22.2: PO-1's 400 surplus raises no Cancel). 0 disables
  them; the PRD default of 100 days is not applied because the fence does not
  yet limit netting (NOT-IMPLEMENTED.md).
- Increase (FR-22.7): a shortage inside the freeze fence gets no planned order
  (BR-22.4), so the latest existing receipt dated on or before the shortage,
  inside the action fence, gets Increase by the shortage (qty = the shortfall
  of non-safety demand, date = the receipt's own date). Later frozen shortages
  of the same receipt add to its one message. The shortage message stays, so
  the demand still has a message for its uncovered part. A receipt after the
  shortage is not asked (it would still be late), nor is one for safety stock.
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
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/planning   # HTTP_ADDR (:8080), ADMIN_ADDR (:9090, /metrics), PLANNING_MAX_VERSIONS (20, 2 to 100), PLANNING_HORIZON_DAYS (365, 30 to 1095), PLANNING_FEATURE_FLAGS (comma-separated planning.dynamic, planning.statistical-forecast, planning.mps-scope; unknown names stop startup), PLANNING_MAX_CONCURRENT_RUNS (1, 1 to 16), PLANNING_RUN_TIMEOUT_MINUTES (60, 1 to 720)
```

### Asynchronous plan runs (#479)

A synchronous `POST /plans/{planCode}:run` runs inside the request, and the platform router cancels a
request after 30 s (`httpx.RequestTimeout`), which is every run above roughly 3,000 items of the
`mrp-50k` shape. With `Prefer: respond-async` the request is validated and the snapshot built as
before (400 and 422 still come back in the request), then the service records a `plan_run` row
(migration `0030`) and answers 202 with the run and `Location: /v1/planning/plan-runs/{id}`. The
engine and the version's write run in the background on a context that keeps the caller's principal
but not the request's deadline:

- `GET /plan-runs/{id}`: `running` (phase `planning`, then `persisting` with `rowsWritten` of
  `rowsTotal`), then `succeeded` with the version it made current, `failed` with a problem, or
  `cancelled`. The version's rows commit in one transaction, so a failed or cancelled run stores none.
- `POST /plan-runs/{id}:cancel`: stops the run at once on the instance that executes it, or on its
  next heartbeat (every 5 s) from any other replica. The engine phase is not interrupted; a cancel
  during it takes effect before the first row is written.
- One run per plan (partial unique index; 409 `PLAN-0001`, also for a synchronous run while an
  asynchronous one is alive). Each process executes at most `PLANNING_MAX_CONCURRENT_RUNS` runs
  (default 1, 503 `PLAN-RUN-CAPACITY` with `Retry-After` beyond it) for at most
  `PLANNING_RUN_TIMEOUT_MINUTES` (default 60, then `PLAN-RUN-TIMEOUT`). On shutdown, runs still
  executing are cancelled and recorded as `PLAN-RUN-INTERRUPTED`; a process that dies leaves a row
  whose heartbeat goes stale, which reads as failed (`PLAN-RUN-ABANDONED`) after 2 min and is
  closed by the next run of the plan.
- The same Idempotency-Key and body return the same run (200, `Idempotent-Replayed: true`). The run
  stores its version under that key, so a synchronous retry with the key replays the version.

Without the preference the run is synchronous, as the web run screen still uses it for the
snapshots it sends. Not built: a queue (a run beyond capacity is refused, not queued), resuming a
run after a restart (the request's snapshot lives only in memory), and the Temporal batch job.

### Scale run through the API (AC2, #479)

`TestRunAPI_ScalePostgres` (`internal/api/scale_pg_test.go`) sends the `mrp-50k` snapshot of the
engine scale test (`domain.ScaleSnapshot`: five BOM levels, 4 sales lines per item over 12 monthly
periods, open POs, mixed coverage and lot rules) as one asynchronous
`POST /v1/planning/plans/{planCode}:run` (`Prefer: respond-async`) over a loopback HTTP server with the
production router (`httpx.NewRouter`, 30 s request timeout), polls `GET /plan-runs/{id}` every 2 s
until the run succeeded, and the version is stored in Postgres through the RLS-bound app role.
Planning has no item master of its own, so the items, BOMs and demand travel in the request; Postgres
holds the plan. It fails if the 202 takes as long as the request timeout or a run takes 10 min or
more. It is skipped unless `PASSAGE_PLANNING_API_SCALE_ITEMS` is set:

```
PASSAGE_TEST_DATABASE_URL=... PASSAGE_TEST_APP_DATABASE_URL=... \
PASSAGE_PLANNING_API_SCALE_ITEMS=50000 PASSAGE_PLANNING_API_SCALE_RUNS=1 \
go test -count=1 -timeout 60m -run TestRunAPI_ScalePostgres -v ./internal/api/
```

`PASSAGE_PLANNING_API_SCALE_RUNS` (default 2) repeats the run with a new Idempotency-Key and compares
the stored plan hashes.

Measured on 2026-11-05 through the asynchronous route and the production router (30 s request timeout in place),
on PostgreSQL 16 (local throwaway cluster, default settings) on the same kind of shared 4-vCPU VM, one run:

| Items | Run | End to end | 202 after | Engine | Planned orders | Messages | Rows stored | On disk | Test-process peak RSS |
|---|---|---|---|---|---|---|---|---|---|
| 10,000 | 1 | 1 min 56.6 s | 366 ms | 9.8 s | 442,164 | 28,400 | 1,899,692 | 627 MB | 1.4 GiB |
| 50,000 | 1 | 9 min 41.1 s | 1.87 s | 57.9 s | 2,159,385 | 141,806 | 9,238,394 | 3,072 MB | 6.2 GiB |

The 50k run meets the 10 min budget through the API, but with little margin on this host; persistence is
still the cost (about 16,000 rows per second). Peak RSS is for the whole test process (generator, client,
server and plan), so the server alone holds less; the chart's memory limit is 8Gi so one 50k run fits.

The earlier numbers below are from 2026-11-04, on the synchronous route served without the router's
request timeout (the `PASSAGE_PLANNING_API_SCALE_NO_TIMEOUT` switch this change removed).

Measured on 2026-11-04 on PostgreSQL 16.15 (local cluster, default settings) and the test process on
the same host: a shared cloud VM with 4 vCPUs, 15 GB RAM and other workloads running at the same time.
This is not the 3-node reference cluster, and the numbers are single runs, not a benchmark:

| Items | Body | Run | End to end | Engine | Planned orders | Messages | Notes |
|---|---|---|---|---|---|---|---|
| 1,000 | 1.0 MiB | 1 and 2 | 9.3 s, 9.4 s | 0.6 s, 0.4 s | 42,402 | 2,637 | equal hashes, production router |
| 5,000 | 5.1 MiB | 1 | 30.0 s, then 500 | — | — | — | production router: the 30 s timeout cancels the run |
| 5,000 | 5.1 MiB | 1 and 2 | 46.3 s, 69.1 s | 2.9 s, 3.1 s | 208,790 | 13,004 | equal hashes, no timeout |
| 50,000 | 50.7 MiB | 1 | 8 min 4 s | 35.1 s | 2,159,385 | 141,806 | no timeout; under the 10 min budget |
| 50,000 | 50.7 MiB | 2 | > 10 min | — | — | — | no timeout; the client gave up at 10 min, so the hash comparison was not reached |

The 50k version takes about 4.5 GB in Postgres (8.9 GB with the second version nearly written) and the test process peaked near
7.3 GiB RSS (sampled; it holds the generator, the request, the server and the plan). Persistence, not
the engine, is the cost: rows go in at about 20,000 per second (5k run: about 900,000 rows in 43 s) as single-row inserts in
pipelined batches (COPY is refused on tables with forced RLS), and each message is one outbox insert.

What the run found, and what changed (#479):

- The 4 MiB body limit of `httpx.Decode` refused a snapshot above about 4,000 items. The run route now
  reads up to 128 MiB (`maxRunBody`), enough for `maxItems` (100,000).
- All of a run's rows were queued in one pgx batch, about 9 million statements at 50k items; the test
  process was killed by the OOM killer at 13 GB. `RunPlan` now sends batches of 10,000 rows
  (`runBatchRows`) inside the same transaction.
- The platform router's 30 s request timeout cancelled any run that takes longer. The run now moves
  off the request with `Prefer: respond-async` (above); the synchronous route keeps the timeout.
- Not changed: faster persistence (multi-row `insert ... select from unnest(...)` per table, and
  batching the `message.raised.v1` outbox rows) would cut most of the 8 minutes; it is a larger change
  of `RunPlan` and is left for the batch-job slice.

Store tests cover persistence, replay and key reuse, version numbering, the
outbox events (plan.started before plan.completed, one message.raised per
stored message), the stored net requirements of the current version, tenant isolation under RLS, the 409 on a concurrent run,
planned-order maintenance (versions, date and vendor rules, delete, superseded
versions) and forecast cells (reduction, recompute on a principle change,
forecast demand for a run), firming (AC-22.8 grouping, target ids, ETag bump,
one outbox event per order, replay, key reuse, 409 `PLAN-0002`, all-or-nothing
refusal of unknown, foreign, deleted and superseded orders), stored Cancel and
Decrease messages, and the version purge.

## Planned-order maintenance and forecasts

- `GET /planned-orders/{id}` returns the order's `version` in the body and as
  `ETag`. `PATCH` and `DELETE` require `If-Match` with it: missing is 428
  `precondition_required`, stale is 412 `conflict_version`. Only open orders
  (unprocessed or approved) of the plan's current version can be maintained.
  `status: approved` approves; firming is the separate command below.
  Delete keeps the row with status `deleted`, so the version's pegging still
  resolves; lists leave it out.
- `PUT /forecasts/{modelCode}/lines` upserts up to 1,000 cells (each replaces
  the cell of its item, site and period start) under `If-Match` on the model;
  a model that does not exist yet has ETag `"0"`. Reduction principles:
  `none`, `percent` (keep 100 − percent), `transactions` (forecast − sales in
  the period, never below 0, BR-22.3), `transactions-dynamic` (sales above a
  period's forecast also consume the following periods). Until sales orders
  (w27) feed the planning service, `salesQty` per cell is entered with it.
  Each written cell records its writer (`updated_by`).

## Forecast consensus (slice 6, FR-22.9)

A forecast model is `draft`, `submitted` or `published`; only a draft accepts
cell writes (409 `PLAN-FORECAST-LOCKED` otherwise). Every step takes `If-Match`
on the model's version (an existing model, so at least `"1"`) and bumps it, so
a replayed step is answered 412 instead of being applied twice.

- `:submit` (`planning.forecast.maintain`) locks a draft that has cells (422
  `PLAN-CONSENSUS-EMPTY` without) and records the submitter.
- `:publish` (`planning.forecast.approve`) marks every cell `consensus`, locks
  the model for good and writes `forecast.published.v1` (model, version, item
  series, periods, first and last day, submitter and approver) to the outbox
  in the same transaction.
- `:reject` (`planning.forecast.approve`) returns a submitted model to draft.
- 4.22.17 segregation of duties: Approve forecast consensus conflicts with
  Maintain forecasts on the same version, so publish and reject are refused
  (403 `PLAN-CONSENSUS-SOD`) for the submitter, for anyone who wrote a cell of
  the model, and for a request without a user (`X-Passage-User`); a submit
  without a user is refused the same way. A wrong status is 409
  `PLAN-CONSENSUS-STATE`.

## Firming (slice 4)

`POST /planned-orders:firm` takes 1 to 1,000 distinct planned-order ids and a
`grouping` (default `vendor-date`) and requires `Idempotency-Key`. In one
transaction it locks the orders in id order, refuses the whole batch when any
id is unknown for the tenant (404), firmed (409 `PLAN-0002`), deleted or of a
superseded version (409 `PLAN-ORDER-NOT-EDITABLE`), groups them, and marks each
`firmed` with `firmedTargetType`, `firmedTargetId` and a new ETag. Planned
purchase orders of one site and vendor merge into one purchase order: all of
them (`vendor`), per delivery date (`vendor-date`), per Monday-to-Sunday week
(`vendor-week`), or not at all (`none`); production and transfer orders never
merge. One `dev.passage.planning.plannedorder.firmed.v1` per order goes to the
outbox in the same transaction (the PRD's `planned-order` entity is spelled
without the hyphen the kernel's type rule refuses). The response, stored in
`firming_batch`, lists the target orders; a replay of the key returns it with
`Idempotent-Replayed: true`.

BR-22.9 describes firming as a saga that creates the target order first. Here
planning firms and publishes; purchasing (PSG-17) and production (PSG-21)
create the orders with the given ids from the event, and the compensation on
their failure is a handoff (NOT-IMPLEMENTED.md).

Each run keeps the newest `PLANNING_MAX_VERSIONS` versions of its plan and
deletes older ones with their planned orders, pegging and messages, in the run
transaction. A version holding a firmed planned order is kept.

## Coverage groups and auto-firming (slice 7)

Coverage groups (FR-22.2) are tenant setup under the duty
`planning.setup.maintain` (Maintain planning setup, 4.22.17): `POST
/coverage-groups` creates one (optional `Idempotency-Key`; a code in use is 409
`PLAN-COVERAGE-EXISTS`), `PUT /coverage-groups/{code}` replaces its settings
under `If-Match`, and `GET` lists or reads them. Omitted fields take the PRD
defaults: coverage time fence 100 days, negative days 1, positive days 0,
freeze and firming fences 0, auto-firm off, action and futures messages on.
Period coverage needs `periodDays` 1 to 365, and `autoFirm` needs a firming
fence of at least 1 day (also enforced by table checks). A run does not read
the groups yet: coverage still travels per item on the run input
(NOT-IMPLEMENTED.md).

## Item coverage (slice 9)

Item coverage (data model ItemCoverage, 4.22.4, with the FR-22.16..FR-22.20
fields) says how one item is planned at a site, in a warehouse of its own (MRP
area) or at a subcontractor, under `planning.setup.maintain`: `POST
/item-coverages` (optional `Idempotency-Key`), `PUT /item-coverages/{id}` under
`If-Match`, and `GET` lists (optionally `?itemId=`) or reads them. It carries
the coverage group, min and max quantity (decimal strings; max 0 is no
maximum, otherwise at least min), receipt margin, lead-time override, default
vendor, planned order type, planning strategy, planning scope, decoupling
point, buffer profile id, required shelf life and priority-based planning.
Omitted fields take site scope, purchase orders, `mts-net` and no lot limits.
A warehouse scope names its warehouse; a subcontractor scope names the vendor
and plans provision transfers. An item has one coverage per site and scope
(per warehouse, per subcontractor vendor): another is 409
`PLAN-ITEM-COVERAGE-EXISTS`; a coverage group the tenant lacks is 422
`PLAN-COVERAGE-GROUP-UNKNOWN`. A run does not read item coverage yet
(NOT-IMPLEMENTED.md).

Auto-firming (BR-22.4): an item whose run coverage has `firmingFenceDays` and
`autoFirm` gets every planned order whose order date falls inside the fence
(order date − plan date < fence; past-due orders included) firmed by the run
itself. In the run transaction the orders are grouped as a planner's firming
would with the default grouping `vendor-date` (planned orders carry no vendor
yet, so purchases of the site merge per delivery date), one `firming_batch`
keyed `plan-run:<version id>` is stored, the orders are written `firmed` with
their target ids, and one `plannedorder.firmed.v1` per order goes to the
outbox. Auto-firming changes only which orders are firmed, never quantities or
dates; the plan hash records it. A replay of the run's Idempotency-Key firms
nothing again. As with manual firming, the next run does not carry firmed
orders forward: until the target orders reach the snapshot as receipts, the
caller sends them as `receipts`, or the next run would plan (and firm) the same
demand again.

## Runs read stored item coverage (slice 11)

`POST /plans/{code}:run` reads the site-scope item coverage at `siteId` of the
snapshot's items (one query, under RLS). An item sent without a coverage code
takes the whole coverage block from it (the group's code, fences, period,
positive and negative days and auto-firm, with the item coverage's min, max and
receipt margin); one without `orderType` takes the planned order type; one
without `leadTimeDays` takes the lead-time override when there is one. Inline
values always win, including an explicit `leadTimeDays: 0`. An item left
without coverage or order type is 400 with `items[i].coverage.code` or
`items[i].orderType`. The Idempotency-Key fingerprint is taken before the
stored values are filled in, so a retry of the same request replays the first
version even if the coverage was edited in between. Warehouse and
subcontractor scopes are not read (FR-22.18).

## Buffer profiles, stored buffers and quota arrangements (slice 10)

DDMRP buffer profiles (FR-22.16, data model BufferProfile) are planning setup
under `planning.setup.maintain`: `POST /buffer-profiles` (optional
`Idempotency-Key`), `PUT /buffer-profiles/{id}` under `If-Match`, and `GET`
lists or reads them. A profile holds the item type, LTF and VF (numeric(5,3)),
order cycle, MOQ, spike threshold (1 to 500 percent of red, default 50), spike
horizon (null means the item's DLT) and the ADU window (default 90 past, 0
future days; at least 7 days together). A code in use is 409
`PLAN-PROFILE-EXISTS`. Item coverage's `bufferProfileId` now refers to a stored
profile: an unknown one is 422 `PLAN-PROFILE-NOT-FOUND` (the constraint is
`NOT VALID`, so rows written before the profile table existed are kept).

`POST /ddmrp/buffers:recalculate` (`planning.run`, the run-side job of
4.22.16) evaluates one buffer with its stored profile through the same code as
`:evaluate` (BR-22.11) and upserts the result per item, site and warehouse in
one statement, so a repeat replaces the row and needs no Idempotency-Key. The
caller sends ADU (or history), DLT, DAF, on-hand, open supply and the demand
days until the run supplies them. `GET /ddmrp/buffers?siteId=&zone=&maxPriority=`
is the DDMRP buffer status inquiry (4.22.14), most urgent (lowest NFP / TOG)
first.

Quota arrangements (FR-22.17, data model QuotaArrangement and QuotaSource) are
planning setup too: `POST /quota-arrangements` (optional `Idempotency-Key`),
`PUT /quota-arrangements/{id}` under `If-Match` (replaces all sources), and
`GET` lists (`?itemId=&siteId=&validOn=`) or reads them. Each source names
exactly the reference of its type (vendor, production version or
from-warehouse), at most once per arrangement, with quota, base, allocated
(carried over on create), an optional maximum per period with its period
length, priority and a blocked flag; the quotas must add up to more than 0.
Validity periods (inclusive) of arrangements for the same item and site must
not overlap: 409 `PLAN-QUOTA-OVERLAP`, checked under a transaction advisory
lock on the item and site. Responses carry each source's BR-22.12 rating. A plan
run allocates its planned orders to the arrangement's sources (slice 19).

A plan run (and a simulation) replenishes a decoupling point by its stored
buffer (slice 20, FR-22.16, BR-22.11): an item whose site-scope item coverage
has `decouplingPoint` and that has a site-scope buffer at the run's site (of the
plan's legal entity) gets one demand of the buffer's planned quantity, due after
the DLT and no earlier than its lead time, instead of being netted; its own
demand, receipts, on-hand, safety stock and min/max are not used, it is removed
from its parents' components, and its own BOM still explodes. Its planned orders
carry `planningPriority` (NFP / TOG) and `bufferZone`; a red buffer raises the
`red-zone` action message (PLAN-0010) on each order. The buffer is as fresh as
its last recalculation (NOT-IMPLEMENTED.md row 7).

## Planning calculators (slice 3)

Five POST endpoints compute a result from the request and store nothing, so they
take no Idempotency-Key and emit no event. They hold the rules the engine will
use once buffers, quotas, strategies and batches are persisted (see
NOT-IMPLEMENTED.md):

- DDMRP buffer (FR-22.16, BR-22.11): zones from ADU, DLT, LTF, VF, MOQ and order
  cycle; qualified demand = due on or before day 0 plus days whose total demand
  exceeds the spike threshold inside the spike horizon; NFP = on-hand + open
  supply − qualified demand; at or below the top of yellow the planned quantity
  is TOG − NFP (at least MOQ, rounded up to `orderMultiple`); priority NFP / TOG.
- Quota allocation (FR-22.17, BR-22.12): lowest (allocated + base) / quota wins,
  ties by priority then id; `split` water-fills to equal ratings, dropping
  shares below `minSplitQty` but never the last source. Quota-0 sources are a
  fallback when all others are blocked; none usable is 422 `PLAN-0009`.
- Forecast consumption (FR-22.19, BR-22.13) for assemble-to-order-final; in a run the
  sales that find no forecast raise the `sales-on-top` message (PLAN-0015), and
  `mts-gross` forecast is not netted against on-hand.
- Default vendor (FR-22.3): a run item's `vendorId`, else the default vendor of its stored item coverage, is
  stamped on planned purchases after quota allocation; a purchase with none raises `no-vendor` (PLAN-0008).
  An item with no coverage code and no stored item coverage takes the coverage group `REQ` (stored, else built-in requirement coverage). A requirement
  that splits over the item's maximum order quantity raises `lot-split` (PLAN-0005), and a planned order no source of its
  quota arrangement can take raises `no-quota-source` (PLAN-0009) and stays without a source.
  `GET /messages` returns the catalog `code` and `text` of the actions that have one.
- Supply schedule and inventory forecast (FR-22.21, FR-22.22): day, week or
  30-day month buckets; value is closing on-hand × unit cost as platform
  `money`, rounded to the currency's minor unit.
- Shelf-life allocation (FR-22.20, BR-22.15): FEFO over batches with
  expiry − demand day ≥ required shelf life; the rest is the shortfall.

## API

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/planning/plans/{planCode}:run` | `planning.run` (Idempotency-Key required; `Prefer: respond-async` answers 202 with a plan run) |
| GET | `/v1/planning/plan-runs/{id}` | `planning.run` or `planning.read` |
| POST | `/v1/planning/plan-runs/{id}:cancel` | `planning.run` |
| GET | `/v1/planning/plans/{planCode}/versions?limit=` | `planning.read` |
| GET | `/v1/planning/plans/{planCode}/versions/{number}` | `planning.read` |
| GET | `/v1/planning/plans/{planCode}/diff?from=&to=&limit=` | `planning.read` |
| GET | `/v1/planning/planned-orders?planCode=&status=&itemId=&limit=` | `planning.read` |
| GET | `/v1/planning/planned-orders/{id}` | `planning.read` |
| PATCH | `/v1/planning/planned-orders/{id}` | `planning.order.maintain` (If-Match required) |
| DELETE | `/v1/planning/planned-orders/{id}` | `planning.order.maintain` (If-Match required) |
| POST | `/v1/planning/planned-orders:firm` | `planning.order.firm` (Idempotency-Key required) |
| GET | `/v1/planning/planned-orders/{id}/pegging?depth=` | `planning.read` |
| GET | `/v1/planning/messages?planCode=&action=&status=&sort=&limit=` | `planning.read` (open messages unless `status` is applied, dismissed or all) |
| GET | `/v1/planning/messages/{id}` | `planning.read` |
| POST | `/v1/planning/messages/{id}:apply` | `planning.order.maintain` (If-Match required, Idempotency-Key optional) |
| POST | `/v1/planning/messages/{id}:dismiss` | `planning.order.maintain` (If-Match required, Idempotency-Key optional) |
| GET | `/v1/planning/forecasts/{modelCode}/lines?itemId=&siteId=&limit=` | `planning.read` |
| PUT | `/v1/planning/forecasts/{modelCode}/lines` | `planning.forecast.maintain` (If-Match required) |
| POST | `/v1/planning/forecasts/{modelCode}:submit` | `planning.forecast.maintain` (If-Match required) |
| POST | `/v1/planning/forecasts/{modelCode}:publish` | `planning.forecast.approve` (If-Match required; not the submitter or a cell writer) |
| POST | `/v1/planning/forecasts/{modelCode}:reject` | `planning.forecast.approve` (If-Match required; same rule) |
| POST | `/v1/planning/ddmrp/buffers:evaluate` | `planning.read` (stateless calculator) |
| POST | `/v1/planning/quota-arrangements:allocate` | `planning.read` (stateless calculator) |
| POST | `/v1/planning/strategies:consume-forecast` | `planning.read` (stateless calculator) |
| POST | `/v1/planning/supply-schedule:project` | `planning.read` (stateless calculator) |
| POST | `/v1/planning/shelf-life:allocate` | `planning.read` (stateless calculator) |
| GET | `/v1/planning/coverage-groups?limit=` | `planning.read` |
| POST | `/v1/planning/coverage-groups` | `planning.setup.maintain` (Idempotency-Key optional) |
| GET | `/v1/planning/coverage-groups/{code}` | `planning.read` |
| PUT | `/v1/planning/coverage-groups/{code}` | `planning.setup.maintain` (If-Match required) |
| GET | `/v1/planning/item-coverages?itemId=&limit=` | `planning.read` |
| POST | `/v1/planning/item-coverages` | `planning.setup.maintain` (Idempotency-Key optional) |
| GET | `/v1/planning/item-coverages/{id}` | `planning.read` |
| PUT | `/v1/planning/item-coverages/{id}` | `planning.setup.maintain` (If-Match required) |
| GET | `/v1/planning/buffer-profiles?limit=` | `planning.read` |
| POST | `/v1/planning/buffer-profiles` | `planning.setup.maintain` (Idempotency-Key optional) |
| GET | `/v1/planning/buffer-profiles/{id}` | `planning.read` |
| PUT | `/v1/planning/buffer-profiles/{id}` | `planning.setup.maintain` (If-Match required) |
| GET | `/v1/planning/ddmrp/buffers?siteId=&zone=&maxPriority=&limit=` | `planning.read` |
| POST | `/v1/planning/ddmrp/buffers:recalculate` | `planning.run` (upsert per scope; no Idempotency-Key) |
| GET | `/v1/planning/quota-arrangements?itemId=&siteId=&validOn=&limit=` | `planning.read` |
| POST | `/v1/planning/quota-arrangements` | `planning.setup.maintain` (Idempotency-Key optional) |
| GET | `/v1/planning/quota-arrangements/{id}` | `planning.read` |
| PUT | `/v1/planning/quota-arrangements/{id}` | `planning.setup.maintain` (If-Match required) |

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
| 403 | `PLAN-CONSENSUS-SOD` | 4.22.17: the forecast consensus is decided by its submitter or a cell writer, or a consensus step has no user |
| 404 | `PLAN-VERSION-NOT-FOUND` | No such version for the plan |
| 404 | `PLAN-ORDER-NOT-FOUND` | No such planned order (on firming: an id unknown for the tenant) |
| 404 | `PLAN-FORECAST-NOT-FOUND` | No such forecast model (422 when a run names it) |
| 404 | `PLAN-COVERAGE-NOT-FOUND` | No such coverage group |
| 404 | `PLAN-ITEM-COVERAGE-NOT-FOUND` | No such item coverage |
| 404 | `PLAN-PROFILE-NOT-FOUND` | No such buffer profile (422 when an item coverage or a recalculation names it) |
| 404 | `PLAN-QUOTA-NOT-FOUND` | No such quota arrangement |
| 409 | `PLAN-0001` | `PLAN_ALREADY_RUNNING`: a run of the plan is in progress |
| 409 | `PLAN-0002` | BR-22.9: a planned order to firm is already firmed |
| 409 | `PLAN-ORDER-NOT-EDITABLE` | The planned order is firmed, deleted or not in the current version |
| 409 | `PLAN-FORECAST-LOCKED` | The forecast model is submitted or published; its cells are locked |
| 409 | `PLAN-CONSENSUS-STATE` | The consensus step is not allowed from the model's status |
| 409 | `PLAN-COVERAGE-EXISTS` | A coverage group with the code already exists |
| 409 | `PLAN-ITEM-COVERAGE-EXISTS` | The item already has a coverage for that site and planning scope |
| 409 | `PLAN-PROFILE-EXISTS` | A buffer profile with the code already exists |
| 409 | `PLAN-QUOTA-OVERLAP` | The validity overlaps another quota arrangement of the same item and site |
| 422 | `PLAN-COVERAGE-GROUP-UNKNOWN` | An item coverage names a coverage group the tenant does not have |
| 422 | `PLAN-CONSENSUS-EMPTY` | A forecast model without cells cannot be submitted |
| 412 | `conflict_version` | If-Match is not the current version of the order, forecast model, coverage group, item coverage, buffer profile or quota arrangement |
| 422 | `PLAN-ORDER-INVALID` | Order date after delivery date, or a vendor on a non-purchase order |
| 428 | `precondition_required` | If-Match missing on a maintenance write |
| 422 | `PLAN-0005` | A requirement would split into more than 1,000 orders |
| 422 | `PLAN-0009` | Quota arrangement has no usable source (all blocked, or quota 0 with others blocked) |
| 422 | `PLAN-INPUT-INVALID` | Snapshot rejected by the engine (unknown coverage or order type, duplicate or unknown item), or calculator input the domain rejects (empty ADU window); `detail` names it |
| 422 | `idempotency_key_reuse` | Same Idempotency-Key with a different plan or snapshot, different orders or grouping on firming, or another coverage group, item coverage, buffer profile or quota arrangement body |

## Wave reference

PSG-22 (Phase 4, Manufacturing and Planning). PRD section 4.22. Depends on PSG-21
(production orders, route lead times), PSG-17 (purchase orders), PSG-16 (on-hand),
PSG-9 (BOM and low-level codes), PSG-27 (sales demand, later).

## Dependencies

Only the platform kernel and the modules it already pulls in (chi, pgx/v5,
shopspring/decimal, google/uuid, and `pgregory.net/rapid` for property tests).

## Legal-entity scope (#241)

`plan`, `plan_version`, `forecast_model` and `quota_arrangement` carry a nullable
`legal_entity_id` (migration 0012) with a RESTRICTIVE legal-entity policy;
`planned_order`, `pegging`, `action_message`, `net_requirement`, `forecast_line` and
`quota_source` follow their parent. A row with no entity is visible only to
all-entity callers. A plan run takes an optional `legalEntityId` that owns the plan
when the run creates it (default: the caller's single entity; required for a caller
scoped to several or to all entities; 403 outside the caller's scope). For an existing
plan it is an assertion (422 when it differs), and a forecast model netted into the
run must belong to the plan's entity (422 `PLAN-FORECAST-ENTITY`). Forecast-model
writes and quota-arrangement creates take `legalEntityId` the same way. A plan or
forecast model code held outside the caller's scope, including by a row with no
entity yet, is 409 `PLAN-CODE-TAKEN`.

### Firming batches, item coverage and DDMRP buffers (#292)

| Table | Decision | Owner of a new row |
|---|---|---|
| `firming_batch` (with its `response` document) | entity-specific root (migration 0013) | the entity of the planned orders' plan version, for a planner's firming and for the run's auto-firm batch; a request mixing entities is 422 `PLAN-FIRM-ENTITY` |
| `item_coverage` | entity-specific root | `legalEntityId` on the create, default the caller's single entity (422 for several or all; 403 outside the scope); an update keeps the owner |
| `ddmrp_buffer` | entity-specific root | `legalEntityId` on `:recalculate`, same rules; a buffer of the same item, site and warehouse that the caller's scope cannot write is 409 `PLAN-BUFFER-ENTITY` and is never overwritten |
| `planning_scenario` | entity-specific root (migration 0014): belongs to one plan (`plan_id`) and carries that plan's legal entity | copied from the plan when the scenario is created; the plan must have run and be visible to the caller (404 `PLAN-NOT-FOUND`), so a scenario is never created, read, changed or run outside the plan's entity; the simulation version it writes carries the same entity |
| `coverage_group`, `buffer_profile` | stay tenant-level: shared planning setup that coverages and buffers of every entity refer to by id | n/a |

Orders the caller cannot see are 404 `PLAN-ORDER-NOT-FOUND` when firming, so a batch
never forms from, or replays into, another entity's orders. The item-coverage scope
key and the firming and profile idempotency keys stay unique per tenant: a clash with
another entity's row is the same 409 or 422 as a clash inside the entity, without
revealing the row. The run reads item coverage under the caller's scope, so a coverage
held by an entity the caller cannot see is as good as absent.

The quota-arrangement overlap rule (409 `PLAN-QUOTA-OVERLAP`) runs per legal entity
as well as per item and site. That assumes a site belongs to exactly one legal
entity; the site register lives in the org service, so a site shared between
entities would let each entity hold an arrangement for it.

### Simulation scenarios (FR-22.15, BR-22.14)

A scenario belongs to a plan that has run at least once and to that plan's legal entity.
Its demand factor (overall, or per item) scales independent demand only: sales lines and
forecast; safety-stock and transfer-issue lines keep their quantity and dependent demand
follows from the explosion. Added sales lines are never scaled; the forecast model's cells
are scaled before the item's strategy plans them, so under `assemble-to-order-final` added
lines consume the scaled forecast (BR-22.13). `POST /v1/planning/scenarios/{code}:run` applies a stored
scenario to the plan-run snapshot and stores a `simulation` version of the scenario's plan. It never becomes current, never
auto-firms, publishes only `scenario.simulated.v1` (no `plan.completed.v1`, `message.raised.v1`
or `plannedorder.firmed.v1`), and its planned orders are 409 `PLAN-0003` on firm, change and
delete. Read its orders with `GET /planned-orders?planCode=&version=N` and compare it with the
operational version through `GET /plans/{planCode}/diff`. Duty `planning.simulate`.

A BOM cycle no longer fails a run (PLAN-0004): the cycle-closing component lines are skipped and each raises a `bom-cycle` warning message (item the parent, `demandRef` the component). Which lines are cut does not depend on the order of the snapshot: the walk starts from the top-level items in id order, then any cycle no top-level item reaches from its smallest id, and follows components in id order, so the line leading back up is cut and the plan hash is stable.
