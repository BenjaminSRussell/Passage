# close

Wave 14 deployable: period control, close checklist, allocations, accruals, regrouping and year-end. One Go module, one image (`close`). Follow-ups are listed in `NOT-IMPLEMENTED.md`.

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
| `CLOSE_REOPEN_DEFAULT_DAYS` | default 3 (1–30); used when a reopen request omits `expiresDays` |
| `CLOSE_REOPEN_APPROVERS` | default 2 (2–3) |
| `CLOSE_LOCK_WAIT_MS` | default 5000 (1000–30000); `lock_timeout` on period transitions |
| `CLOSE_REOPEN_SWEEP_INTERVAL` | default `1m`; how often expired reopens are closed again |

## Period control and the ledger posting API

`period_status` holds one row per ledger, fiscal period and module (`all`, `gl`, `ap`, `ar`, `inventory`, `assets`, `cash`, `production`, `projects`, `tax`, `bank`, `payroll`) with status `open`, `on_hold`, `closed` or `permanently_closed`.

`domain.Decide(rows, module, group)` implements BR-14.1. The module row wins, else the `all` row, else the period is never opened. `open` allows. `on_hold` allows only the row's allowed group (LEDGER-0014). Everything else, including never opened, rejects (LEDGER-0013). `domain.ModuleForSource` maps `source.system` to a module; `manual` posts to `gl`; unknown or omitted systems are rejected if any module row is closed.

The ledger (wave 11) enforces the rule inside its posting transaction by reading the rows with `store.PeriodRows` (`select ... for share`) and calling `Decide`, or by calling `POST /v1/close/period-statuses:check`. Reversal, migration and batch posts keep their original source system and use the same decision. `TestPostingPaths_ClosedRejected` is the closed-period contract test over every path, and `period_contract_test.go` runs the same rule through the real HTTP routes for every source system the posting services send.

## API

`POST /v1/close/period-statuses:check` and `GET /v1/close/periods:resolve?ledgerId=&postingDate=&sourceSystem=` (duty `period.check` for both; the resolver needs the caller's single legal entity and returns the fiscal period id, its dates and the module status, or 404 `CLOSE-0005` / 409 `CLOSE-0006`), `PUT /v1/close/periods/{fiscalPeriodId}` (duty `period.maintain`) registers a period's dates and opens the `all` module, only when the ledger service (`GET /v1/ledger/ledgers/{id}` with a signed `ledger.ledger.lookup` service token, `LEDGER_URL` plus the service signing key) says the ledger belongs to the caller's legal entity (403 otherwise, 503 when the ledger is unreachable or unconfigured); same dates replay with 200, moved or overlapping dates are 409 `CLOSE-0001`. `GET /v1/close/periods?ledgerId=` lists the matrix; `PATCH /v1/close/periods/{fiscalPeriodId}/modules/{module}` holds, closes or permanently closes a module (`lock_timeout` from `CLOSE_LOCK_WAIT_MS`; GL/`all` cannot close while another module is open). A closed period reopens only through `POST ...:request-reopen` plus two distinct approvers (`period.reopen`; the requester cannot approve). Expired reopens are closed again by the in-process sweeper. `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`. Period transitions enqueue `dev.passage.close.period.statuschanged.v1` (the PRD spells the verb `status-changed`, which the kernel `events.ValidType` pattern rejects).

Checklist: `PUT/GET /v1/close/templates/{templateId}`, `POST /v1/close/runs` (Idempotency-Key), `GET /v1/close/runs/{runId}/tasks`, and `POST /v1/close/tasks/{taskId}:start|:complete|:skip|:reassign` (`close.checklist.write` / `close.checklist.read`). Dependencies are enforced by the DAG; skip needs a reason.

Allocations, accruals and regrouping post through the shared `ledgerclient` with source system `close` and **never** set `PostClosed` (only year-end does). A closed GL period is refused in close (`LEDGER-0013`) before the ledger is called. `POST /v1/close/allocation-runs` (duty `close.allocation.run`) proposes from the trial balance and optionally posts; `:post` / `:reverse` follow. Accrual `:apply` spreads the amount stored on the scheme and posts the occurrences that are due; the sweeper posts later occurrences and `autoReverse` reversals when they fall due. Recurring journals stay in the ledger. Regrouping posts one journal per key date; a rule already claimed for that ledger and key date is `CLOSE-0010`. Remaining-term items without a due date fall into the shortest bucket. Write endpoints require `Idempotency-Key` (409 `COM-0409` on reuse with a different body).

Year-end close: `POST /v1/close/year-end-runs` (duty `close.yearend.run`) takes only `ledgerId`, a four digit `fiscalYear`, `retainedEarningsAccount`, optional `retainDimensions` and `supersedesRunId`; any other field (balances, class, currency, dates) is a 400. Close never accepts amounts, accounts or dates from the caller, because it holds `ledger.entry.post`: it confirms the ledger belongs to the caller's legal entity, reads the ledger's chart once (`ledger.account.lookup`; the retained earnings account must be a postable equity account) and requires the year's fiscal periods to be contiguous, non-overlapping and to cover 1 January to 31 December, every module closed (general ledger may be on hold) and the last period not permanently closed, because the closing entry posts into it (404 `CLOSE-0005` for a gap or a missing first or last day, 409 `CLOSE-0006` for an overlap, 409 `CLOSE-0001` for a status). It reads the balances per account and dimension from the ledger trial balance (`ledger.balance.read`; a trial balance in a currency other than the ledger's is a 422), takes each account's class from the chart and derives the closing date (last period end) and the proposed opening date (next day) from its own periods. `domain.CloseYear` computes the proposals; the run is stored in `year_end_run` with its proposals and each posted journal id (sequence per ledger and year).

Only the closing entry (profit and loss to retained earnings, dated the last day of the year) is posted. The ledger's balances are cumulative from inception and it has no period 0 or opening flag, so an opening entry would double every balance sheet account; the opening proposal is stored on the run as a verification artifact (it equals the closing balance sheet per account and dimension, BR-14.8) and never posted. The entry goes through the shared `ledgerclient` with a signed `ledger.entry.post` token (source system `close`, document and idempotency key UUIDs derived from the run id), and the run is marked `completed` with `dev.passage.close.yearend.completed.v1` in the outbox in the same transaction. The last period is still closed or on hold in the close service, which the ledger refuses without `ledger.period.postclosed`. The ledger has no allowed-group forwarding to pass, so `close` is the only service whose grant to the ledger holds that flag duty (`policies/authz/data.json` `service_grants`, mirrored in `tenant.serviceGrants`), and `ledgerclient` signs it only for the closing entry and the reversals of a re-run.

Proposals are derived from the ledger once and frozen, with the run's dispatched mark, before the first ledger write; a retry resends the frozen body under the same idempotency keys, so an entry the ledger accepted but never answered is replayed and recorded, not re-derived. Repeating a completed request returns the stored run (200) before preconditions are re-read, also for a completed re-run that names the same `supersedesRunId`; a different request for the same ledger and year is 409 `COM-0409`. A definite ledger refusal (400, 404, 422) or a validation failure on the call that first sends anything to the ledger marks the run `abandoned` and frees the year; any other failure, or a refusal after an earlier attempt, leaves it `pending` and only the same request resumes it. A completed year is redone by naming the latest completed run in `supersedesRunId`: close reverses its closing entry on its own date (`ledger.journal.reverse`, journal `reverseClosing`) and posts again from the ledger's current balances. A ledger refusal is 422 `CLOSE-0008` (including ledger 4xx answers such as 403, which a retry cannot fix); balances or a chart that cannot be closed are 400 `CLOSE-0009`; a posting the ledger is still processing is 409 `CLOSE-0002` with `Retry-After`; an unreachable ledger is 503 and the same request is safe to retry. `GET /v1/close/year-end-runs/{runId}` (duty `close.yearend.read`) returns the run. Both need `LEDGER_URL` and the service signing key, else 503.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| LEDGER-0013 | 422 | period closed, permanently closed or never opened (contract slug `period_closed`) |
| LEDGER-0014 | 403 | period on hold and the caller's group is not allowed |
| CLOSE-0001 | domain | invalid period transition, or task not pending |
| CLOSE-0002 | 409 | the ledger is still processing the same posting; retry the same request after `Retry-After` |
| CLOSE-0003 | domain | task blocked by an open dependency |
| CLOSE-0004 | 400 | invalid field or domain input |
| CLOSE-0005 | 404 | no fiscal period covers the date, or the year's periods leave a gap or miss 1 January or 31 December |
| CLOSE-0006 | 409 | several fiscal periods cover a date or overlap |
| CLOSE-0007 | 404 | no such year-end run, template, task, allocation/accrual/regrouping rule or run |
| CLOSE-0011 | 422 | the request does not match the stored rule, ledger, period or currency, or there is nothing to post |
| CLOSE-0008 | 422 | the ledger refused a year-end request, or its trial balance is not in the ledger's currency |
| CLOSE-0009 | 400 | the ledger's balances or chart cannot be closed (account outside the chart, unclassifiable type, balances that do not sum to zero) |
| CLOSE-0010 | 409 | a posted regrouping already exists for this ledger, key date and rules |
| COM-0409 | 409 | a different year-end request for a ledger and year that has an unfinished or completed run, or Idempotency-Key reused with a different body |
| ALLOC-0001 | domain | reciprocal allocation did not converge within the iteration cap |
| ALLOC-0002 | domain | allocation basis is zero for every destination |
| COM-0001 | 400 | malformed body, unknown field or wrong JSON type |
| COM-0500 | 500 | internal error, detail hidden; a store failure never allows a posting |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

## Engines

- `Allocate` splits an amount by basis with rounding to the currency scale and the residual on the largest share, ties to the first defined (BR-14.4).
- `Reciprocal` solves reciprocal department allocation by Gauss-Seidel on unrounded decimals and rounds once; it fails with ALLOC-0001 at the iteration cap (BR-14.5).
- `CloseYear` returns closing entries (P&L to retained earnings, per dimension combination when requested) and opening proposals, and verifies opening equals closing per combination before returning (BR-14.8). Only the closing entries are posted.
- `Instantiate`, `Eligible`, `Complete`, `Skip` run the checklist DAG; a dependency is satisfied when completed or skipped (BR-14.10).

- `SpreadAccrual` splits an amount across occurrences (equal, by days in the month, or custom) with the residual on the last (BR-14.7).
- `Regroup` remaining-term, debit/credit balance and account-balance-sign rules emit reversing reclassifications (BR-14.11–14.13).
- `ApproveReopen` requires two distinct approvers and rejects the requester.

Engines return proposals; year-end, allocations, accruals and regrouping post them through `ledgerclient`. Year-end is the only path that sets `PostClosed`.

## Metrics

`close_period_checks_total`, `close_period_rejections_total`, `close_period_check_failures_total`, `close_year_end_runs_completed_total` (stdlib atomics rendered as text).
