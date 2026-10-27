# lean

Lean manufacturing for Passage (wave 24, PSG-24, slices 1, 2 and 4). Production flows with their
activities and activation, kanban rules (fixed, scheduled, event), kanban generation, the
kanban card state machine and the kanban board (slice 1). Slice 2 adds takt vs cycle on the
board (FR-24.14) and the lean subcontracting price rule as a preview of the purchase order
lines for completed service jobs (FR-24.20, BR-24.16). Slice 4 adds kanban jobs, one per
activity of the rule's flow, started and completed on the work-cell board with quantity, scrap
and handling units; the jobs drive their card to In progress and Completed (FR-24.10, BR-24.6).

Flows, activities, rules, cards and jobs are stored in Postgres (RLS on every table, outbox
events in the same transaction). The subcontracting preview stores nothing. Flow costing
periods and PO creation and receipt in purchasing are later slices; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/lean              entrypoint: dbx.Connect, optional dbx.Migrate, httpx router, serve
internal/domain       pure rules (no I/O): kanban.go (BR-24.6 state machine, event cards),
                      rules.go (FR-24.9 rule check, BR-24.7 count and capacity, BR-24.8
                      quantities), flow.go (FR-24.8 connectivity, FR-24.14 takt vs cycle),
                      flowcost.go (BR-24.9), subcontract.go (FR-24.20, BR-24.16), jobs.go
                      (job start and completion and the card steps they cause, BR-24.6)
internal/store        Postgres repository: flows, rules, generation, moves, board (store.go);
                      kanban jobs and the work-cell board (jobs.go)
internal/api          HTTP handlers: decode → validate → tenant → store/domain → JSON/problem
internal/events       event types and payloads
migrations/           0001_init.sql (production_flow, flow_activity, kanban_rule, kanban,
                      idempotency_keys, outbox), 0002_kanban_jobs.sql (kanban_job,
                      kanban.handling_units), RLS on every table
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

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Malformed body or invalid input; field errors in `errors` |
| `LEAN-0001` | 409 | Kanban or job move not allowed (BR-24.6, AC-24.7), including a hand move of a card with jobs to In progress or Completed |
| `LEAN-0002` | 409 | Rule would exceed its number of kanbans (BR-24.7) |
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
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different request |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry for its answer |
| `AUTH-0001` | 401 | No tenant or principal |
| `AUTH-0003` | 403 | Missing duty |
| `COM-0500` | 500 | Internal error (detail hidden) |

`LEAN-0003` (period cannot settle, BR-24.9) is raised by the domain rule `CheckSettleable`,
which is not reachable over HTTP until flow costing is built.

## Wave reference

PRD section 4.24 (`docs/prd/phase-4-manufacturing.md`), Wave 24. Owned paths:
`services/lean/` and `apps/web/src/modules/lean/` (screens listed in that module's README).

## Dependencies

Only the platform module and what it already pulls in: chi, pgx/v5, shopspring/decimal,
pgregory.net/rapid (tests).
