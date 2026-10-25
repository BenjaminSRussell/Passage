# close

Wave 14 deployable: period control, close checklist, allocations and year-end. This slice ships the period-control decision (`:check` endpoint and `domain.Decide`), the allocation, year-end and checklist engines as pure domain code, and the service skeleton. One Go module, one image (`close`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/close
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/close
```

Store tests need `PASSAGE_TEST_DATABASE_URL` and `PASSAGE_TEST_APP_DATABASE_URL`; they skip without them.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## Period control and the ledger posting API

`period_status` holds one row per ledger, fiscal period and module (`all`, `gl`, `ap`, `ar`, `inventory`, `assets`, `cash`, `production`, `projects`, `tax`, `bank`, `payroll`) with status `open`, `on_hold`, `closed` or `permanently_closed`.

`domain.Decide(rows, module, group)` implements BR-14.1. The module row wins, else the `all` row, else the period is never opened. `open` allows. `on_hold` allows only the row's allowed group (LEDGER-0014). Everything else, including never opened, rejects (LEDGER-0013). `domain.ModuleForSource` maps `source.system` to a module; `manual` posts to `gl`; unknown or omitted systems are rejected if any module row is closed.

The ledger (wave 11) enforces the rule inside its posting transaction by reading the rows with `store.PeriodRows` (`select ... for share`) and calling `Decide`, or by calling `POST /v1/close/period-statuses:check`. Reversal, migration and batch posts keep their original source system and use the same decision. `TestPostingPaths_ClosedRejected` is the closed-period contract test over every path.

## API

`POST /v1/close/period-statuses:check` (duty `period.check`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`. The AsyncAPI file declares `dev.passage.close.period.statuschanged.v1` (the PRD spells the verb `status-changed`, which the kernel `events.ValidType` pattern rejects); nothing publishes it yet.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| LEDGER-0013 | 422 | period closed, permanently closed or never opened (contract slug `period_closed`) |
| LEDGER-0014 | 403 | period on hold and the caller's group is not allowed |
| CLOSE-0001 | domain | invalid period transition, or task not pending |
| CLOSE-0002 | reserved | close busy (lock wait), used by the close run slice |
| CLOSE-0003 | domain | task blocked by an open dependency |
| CLOSE-0004 | 400 | invalid field or domain input |
| ALLOC-0001 | domain | reciprocal allocation did not converge within the iteration cap |
| ALLOC-0002 | domain | allocation basis is zero for every destination |
| COM-0001 | 400 | malformed body, unknown field or wrong JSON type |
| COM-0500 | 500 | internal error, detail hidden; a store failure never allows a posting |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

## Engines

- `Allocate` splits an amount by basis with rounding to the currency scale and the residual on the largest share, ties to the first defined (BR-14.4).
- `Reciprocal` solves reciprocal department allocation by Gauss-Seidel on unrounded decimals and rounds once; it fails with ALLOC-0001 at the iteration cap (BR-14.5).
- `CloseYear` returns closing entries (P&L to retained earnings, per dimension combination when requested) and opening entries, and verifies opening equals closing per combination before returning (BR-14.8).
- `Instantiate`, `Eligible`, `Complete`, `Skip` run the checklist DAG; a dependency is satisfied when completed or skipped (BR-14.10).

Engines return proposals; nothing here posts to the ledger.

## Metrics

`close_period_checks_total`, `close_period_rejections_total`, `close_period_check_failures_total` (stdlib atomics rendered as text).
