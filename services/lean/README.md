# lean

Lean manufacturing for Passage (wave 24, PSG-24, slice 1). Production flows with their
activities and activation, kanban rules (fixed, scheduled, event), kanban generation, the
kanban card state machine and the kanban board.

Slice 1 persists flows, activities, rules and cards (Postgres, RLS on every table, outbox
events in the same transaction). Kanban jobs per activity, flow costing periods and lean
subcontracting are later slices; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/lean              entrypoint: dbx.Connect, optional dbx.Migrate, httpx router, serve
internal/domain       pure rules (no I/O): kanban.go (BR-24.6 state machine, event cards),
                      rules.go (FR-24.9 rule check, BR-24.7 count and capacity, BR-24.8
                      quantities), flow.go (FR-24.8 connectivity), flowcost.go (BR-24.9)
internal/store        Postgres repository: flows, rules, generation, moves, board
internal/api          HTTP handlers: decode → validate → tenant → store/domain → JSON/problem
internal/events       event types and payloads
migrations/           0001_init.sql (production_flow, flow_activity, kanban_rule, kanban,
                      idempotency_keys, outbox), RLS on every table
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
| GET | `/v1/lean/kanbans/{id}` | `lean.read` | One card |
| POST | `/v1/lean/kanbans/{id}:move` | `lean.kanban.work` | Move a card one step (BR-24.6); `version`; Idempotency-Key |
| GET | `/v1/lean/board` | `lean.kanban.work` | Board columns by state; `siteId`, `flowId`, `ruleId` |

Duties are enforced with the kernel `authz.RequireDuty`. The OPA entries are a handoff to
`policies/`.

## Events

| Type | When |
|---|---|
| `dev.passage.lean.flow.activated.v1` | a flow is activated |
| `dev.passage.lean.kanban.created.v1` | a card is generated (one event per card) |
| `dev.passage.lean.kanban.statechanged.v1` | a card moves (PRD `kanban.state-changed`) |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Malformed body or invalid input; field errors in `errors` |
| `LEAN-0001` | 409 | Kanban move not allowed (BR-24.6, AC-24.7) |
| `LEAN-0002` | 409 | Rule would exceed its number of kanbans (BR-24.7) |
| `LEAN-0004` | 422 | Process activity has no output location (FR-24.8) |
| `LEAN-0101` | 404 | Production flow not found |
| `LEAN-0102` | 404 | Kanban rule not found |
| `LEAN-0103` | 404 | Kanban not found |
| `LEAN-0104` | 409 | Stale `version` |
| `LEAN-0105` | 409 | Flow name in the value stream, activity name or sequence, or rule code already used |
| `LEAN-0106` | 409 | Flow is not a draft, so it cannot be activated |
| `LEAN-0107` | 422 | Rule cannot generate now: scheduled replenishment, or not effective today |
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
