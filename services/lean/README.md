# lean

Lean manufacturing for Passage (wave 24, PSG-24, slices 1, 2, 4, 5 and 6). Production flows with their
activities and activation, kanban rules (fixed, scheduled, event), kanban generation, the
kanban card state machine and the kanban board (slice 1). Slice 2 adds takt vs cycle on the
board (FR-24.14) and the lean subcontracting price rule as a preview of the purchase order
lines for completed service jobs (FR-24.20, BR-24.16). Slice 4 adds kanban jobs, one per
activity of the rule's flow, started and completed on the work-cell board with quantity, scrap
and handling units; the jobs drive their card to In progress and Completed (FR-24.10, BR-24.6).
Slice 5 adds flow costing periods (FR-24.12): a period is opened for a flow and date range and
settled after its last day, only when every card with jobs in it is Completed or Emptied
(BR-24.9, LEAN-0003), with variance = material + conversion − output in the period currency
(AC-24.8) and the `flowperiod.settled` event. Slice 6 adds the kanban rule analysis (PRD 4.24.14:
cycles, lead time from planning to completion, late share, active cards against the maximum),
the backflush preview of a period's conversion cost (FR-24.12: cycle time × processed quantity ×
the activity's hourly rate) and the CSV export of a flow's costing periods. Every card completion
and job completion is recorded in an append-only history, so a reused fixed card's earlier
cycles still count.

Flows, activities, rules, cards and jobs are stored in Postgres (RLS on every table, outbox
events in the same transaction). The subcontracting preview stores nothing. Deriving period
material cost from material issues, conversion rates from the resources service, the ledger journal, and PO creation and receipt in
purchasing are later slices or handoffs; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/lean              entrypoint: dbx.Connect, optional dbx.Migrate, httpx router, serve
internal/domain       pure rules (no I/O): kanban.go (BR-24.6 state machine, event cards),
                      rules.go (FR-24.9 rule check, BR-24.7 count and capacity, BR-24.8
                      quantities), flow.go (FR-24.8 connectivity, FR-24.14 takt vs cycle),
                      flowcost.go (BR-24.9 gate, variance in money), subcontract.go (FR-24.20, BR-24.16), jobs.go
                      (job start and completion and the card steps they cause, BR-24.6),
                      analysis.go (PRD 4.24.14 rule KPI), backflush.go (FR-24.12 conversion)
internal/store        Postgres repository: flows, rules, generation, moves, board (store.go);
                      kanban jobs and the work-cell board (jobs.go); flow costing
                      periods and the settlement gate (costing.go); cycle and job-completion
                      history and the reads of the analysis and backflush (analysis.go)
internal/api          HTTP handlers: decode → validate → tenant → store/domain → JSON/problem
internal/events       event types and payloads
migrations/           0001_init.sql (production_flow, flow_activity, kanban_rule, kanban,
                      idempotency_keys, outbox), 0002_kanban_jobs.sql (kanban_job,
                      kanban.handling_units), 0003_flow_costing.sql
                      (flow_costing_period), 0004_kanban_history.sql (kanban_cycle,
                      kanban_job_completion, kanban.planned_at), RLS on every table
api/openapi.yaml      OpenAPI 3.1, checked against the router by internal/api/openapi_test.go
events/asyncapi.yaml  AsyncAPI 3, checked against internal/events by events_test.go
charts/lean           Helm chart (helm lint), 2 replicas as in PRD 4.24.10
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/lean
```

`DATABASE_URL` is required. `HTTP_ADDR` defaults to `:8080`. The gateway sets
`X-Passage-Tenant`, `X-Passage-User` and `X-Passage-Duties`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://owner@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

Store tests use `testkit.DBWithApp` (queries as the `passage_app` role under RLS) and are
skipped when `PASSAGE_TEST_DATABASE_URL` is unset.

- AC-24.6 (`lean-kanban-sim`): an event rule with fixed qty 20 and a sales line of 50 creates
  three Planned cards that complete for 60 in total (domain and store).
- AC-24.7: Planned → Completed is refused with LEAN-0001 (domain, API and store); the card
  keeps its state.
- BR-24.6 property tests against an independent oracle; emptied event cards are spent, fixed
  cards return to Not planned.
- BR-24.7: a fixed rule never holds more active cards than its number of kanbans, including on
  card reuse and under concurrent generation.
- FR-24.8 (`lean-flow-demo`): a process activity without an output cannot be activated
  (LEAN-0004); predecessor cycles are refused.
- FR-24.14: the board filtered to a flow reports takt time against the slowest activity.
- FR-24.10 and BR-24.6 with jobs: a card of a rule with a flow gets one job per activity; the
  first started job runs the card (through Prepared unless the activity needs picking), the
  last completed job completes it with that job's report and handling units, exactly once
  under concurrent completions; a card with jobs is never moved to In progress or Completed by
  hand; reuse clears the jobs (domain property test and store tests). AC-24.7 on the work-cell
  board: a job that has not started cannot complete (LEAN-0001).
- AC-24.8 and BR-24.9 (store, against Postgres under RLS): a period with an open card that has
  jobs in it is refused with LEAN-0003 naming the card and stays open; once every card is done
  it settles with variance 900.10 + 300.00 − 1150.25 = 49.85 USD, writes one
  `flowperiod.settled` event, replays by key and never settles twice (LEAN-0111); a card in
  progress since an earlier period blocks, a reused card's earlier cycle does not; a period that
  includes today is LEAN-0112; overlapping periods are LEAN-0109; amounts finer than the
  currency's minor unit are refused, not rounded (domain table and rapid property: output +
  variance = material + conversion).
- PRD 4.24.14 kanban rule analysis (domain table and rapid property; store against Postgres): a
  fixed card reused once has two cycles, each timed from its planning; a jobless event card
  completed after its due time is late (100 %); an open card past due is overdue now; the period
  filter is by completion day (UTC); nothing measured is null, not zero.
- FR-24.12 backflush (domain table and rapid property: the total equals the sum of the activity
  costs at the minor unit; store): Mix 40 s × 45.5 units at 90/h and Fill 30 s × 45.5 at 60/h give
  68.25, counting the completions of a card reused since; service activities take no rate.
- AC-24.17: a service job of 20 on vendor V in `po-per-kanban` mode gives one PO line of 20 at
  the agreement price; periodic-po accumulates per vendor, item and price (rapid property:
  quantity and amount are conserved); the agreement valid on the completion date prices the
  job, a newer overlapping one wins, ValidTo is inclusive; LEAN-0005 without an agreement.

## Endpoints

| Method | Path | Duty | Purpose |
|---|---|---|---|
| POST | `/v1/lean/flows` | `lean.flow.write` | Create a draft flow with activities (FR-24.8); Idempotency-Key |
| GET | `/v1/lean/flows` | `lean.read` | Flows, newest first; `status`, `limit` |
| GET | `/v1/lean/flows/{id}` | `lean.read` | One flow with its activities |
| POST | `/v1/lean/flows/{id}:activate` | `lean.flow.write` | Validate connectivity and activate; `version`; Idempotency-Key |
| POST | `/v1/lean/kanban-rules` | `lean.kanban-rule.write` | Create a kanban rule (FR-24.9); Idempotency-Key |
| GET | `/v1/lean/kanban-rules` | `lean.read` | Rules by code with active card count; `flowId`, `limit` |
| GET | `/v1/lean/kanban-rules/{id}` | `lean.read` | One rule |
| POST | `/v1/lean/kanban-rules:fixed-count` | `lean.kanban-rule.write` | Suggested number of kanbans (BR-24.7); writes nothing |
| POST | `/v1/lean/kanbans:generate` | `lean.kanban.write` | Cards for a fixed rule (count) or an event rule (demand); Idempotency-Key |
| GET | `/v1/lean/kanbans/{id}` | `lean.read` | One card with its jobs |
| POST | `/v1/lean/kanbans/{id}:move` | `lean.kanban.work` | Move a card one step (BR-24.6); `version`; Idempotency-Key |
| GET | `/v1/lean/board` | `lean.kanban.work` | Board columns by card state; `siteId`, `flowId`, `ruleId`; with `flowId`, `takt` compares takt time and the bottleneck cycle time (FR-24.14). With `workCellId`: the work-cell board, columns of jobs by job state (FR-24.10) |
| POST | `/v1/lean/kanban-jobs/{id}:start` | `lean.kanban.work` | Start a job; the first one runs its card (BR-24.6); `version`; Idempotency-Key |
| POST | `/v1/lean/kanban-jobs/{id}:complete` | `lean.kanban.work` | Complete a job with `reportedQty`, `scrapQty`, `handlingUnits`; the last one completes its card; `version`; Idempotency-Key |
| POST | `/v1/lean/flows/{id}/costing-periods` | `lean.flow.settle` | Open a costing period (`periodStart`, `periodEnd`, `currency`); no overlap with the flow's other periods; Idempotency-Key |
| GET | `/v1/lean/flows/{id}/costing-periods` | `lean.read` | The flow's periods, latest first; `limit` |
| POST | `/v1/lean/flows/{id}/costing-periods/{periodId}:settle` | `lean.flow.settle` | Settle after the last day with `materialCost`, `conversionCost`, `outputValue` (BR-24.9, AC-24.8); `version`; Idempotency-Key |
| GET | `/v1/lean/kanban-rules/{id}/analysis` | `lean.read` | Rule KPI over `from`..`to` (default the last 30 days, at most 366): cycles, average lead time against the rule's, late share, active against maximum, overdue now (PRD 4.24.14) |
| POST | `/v1/lean/flows/{id}/costing-periods/{periodId}:backflush` | `lean.flow.settle` | Conversion cost of the period by activity from the hourly `rates` given (FR-24.12); writes nothing |
| GET | `/v1/lean/flows/{id}/costing-periods:export` | `lean.read` | The flow's periods as CSV, oldest first (PRD 4.24.14 flow costing period report) |
| POST | `/v1/lean/subcontracting:preview` | `lean.flow.write` | PO lines for completed service jobs priced by purchase agreement (FR-24.20, BR-24.16); writes nothing |

Duties are enforced with the kernel `authz.RequireDuty`. The OPA entries are a handoff to
`policies/`.

## Events

| Type | When |
|---|---|
| `dev.passage.lean.flow.activated.v1` | a flow is activated |
| `dev.passage.lean.kanban.created.v1` | a card is generated (one event per card) |
| `dev.passage.lean.kanban.statechanged.v1` | a card moves (PRD `kanban.state-changed`); when a job moved it, with `jobId` and, on completion, the job's quantities and handling units |
| `dev.passage.lean.kanbanjob.statechanged.v1` | a job is started or completed on the work-cell board |
| `dev.passage.lean.flowperiod.settled.v1` | a costing period is settled (PRD `flow-period.settled`): amounts, variance, card count; `journalEntryId` null until the ledger posts it |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Malformed body or invalid input; field errors in `errors` |
| `LEAN-0001` | 409 | Kanban or job move not allowed (BR-24.6, AC-24.7), including a hand move of a card with jobs to In progress or Completed |
| `LEAN-0002` | 409 | Rule would exceed its number of kanbans (BR-24.7) |
| `LEAN-0003` | 409 | Period cannot be settled: cards with jobs in it are not Completed or Emptied; the first 20 card numbers are listed (BR-24.9, `FLOW_PERIOD_OPEN_KANBANS`) |
| `LEAN-0004` | 422 | Process activity has no output location (FR-24.8) |
| `LEAN-0005` | 422 | No valid purchase agreement for a service item on the job's completion date (FR-24.20) |
| `LEAN-0101` | 404 | Production flow not found |
| `LEAN-0102` | 404 | Kanban rule not found |
| `LEAN-0103` | 404 | Kanban not found |
| `LEAN-0104` | 409 | Stale `version` |
| `LEAN-0105` | 409 | Flow name in the value stream, activity name or sequence, or rule code already used |
| `LEAN-0106` | 409 | Flow is not a draft, so it cannot be activated |
| `LEAN-0107` | 422 | Rule cannot generate now: scheduled replenishment, or not effective today |
| `LEAN-0108` | 404 | Kanban job not found |
| `LEAN-0109` | 409 | Costing period overlaps another period of the flow |
| `LEAN-0110` | 404 | Costing period not found on this flow |
| `LEAN-0111` | 409 | Costing period is already settled |
| `LEAN-0112` | 409 | Costing period has not ended (its last day, UTC, has not passed) |
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different request |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry for its answer |
| `AUTH-0001` | 401 | No tenant or principal |
| `AUTH-0003` | 403 | Missing duty |
| `COM-0500` | 500 | Internal error (detail hidden) |

Amounts are validated against the period currency's minor unit (COM-0001 when finer), so the
stored variance is exact; the table also checks variance = material + conversion − output.
Backflush rates take at most six decimals; each activity cost is rounded once to the minor unit
and the conversion cost is their sum.

## Wave reference

PRD section 4.24 (`docs/prd/phase-4-manufacturing.md`), Wave 24. Owned paths:
`services/lean/` and `apps/web/src/modules/lean/` (screens listed in that module's README).

## Dependencies

Only the platform module and what it already pulls in: chi, pgx/v5, shopspring/decimal,
pgregory.net/rapid (tests).
