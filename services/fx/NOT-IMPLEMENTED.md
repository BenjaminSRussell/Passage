# Not implemented in PSG-12 (after PSG-1202 slice 2a)

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: exchange rate resolution, inverse, triangulation | built | `internal/domain/rates.go` |
| Deliverable 1: rate audit store (versioned rows, BR-12.10 precedence, outbox event) | built | `internal/store`, `migrations/0001_init.sql` |
| Deliverable 1: rate HTTP API, historical rate API, convert endpoint | PSG-1202 slice 2b | needs HTTP layer on the store |
| Deliverable 1: provider adapters (ECB, OpenExchangeRates, CSV) | PSG-1202 slice 2c | no adapter yet |
| Deliverable 2: rounding, residual allocation, three-currency conversion | built | `internal/domain/convert.go` |
| Deliverable 2: single-line residual to `rounding_difference_account` (BR-12.4 last sentence) | not built | needs ledger currency policy, owned by `services/ledger` (PSG-11, absent from the worktree) |
| Deliverable 2: realized/unrealized gain-loss accounts | not built | account configuration lives in `services/ledger` (PSG-11 absent); the calculation returns gain and loss amounts |
| Deliverable 3: ledger groups and posting layers (AC2) | slice 3 of PSG-12 | line budget; persistence needs `services/ledger` (PSG-11 absent) |
| Deliverable 4: revaluation calculation, `closing_rate` method, reversal date | built | `internal/domain/reval.go` |
| Deliverable 4: `cumulative_no_reversal` method, run table, posting, reversal entries, batch schedule | not built | posting and run state are owned by `services/ledger` (PSG-11 absent) |
| Deliverable 5: currency translation (CTA) | slice 3 of PSG-12 | line budget; posting side needs `services/ledger` |
| Deliverable 6: market data series | PSG-1202 slice 2d | migration `0002`, domain, store and API |
| AC4: provider outage fallback and alert, import run log store | PSG-1202 slice 2c | needs provider adapters and the degraded event |
| Temporal schedule for provider imports | deferred | CronJobs in slice 2e are the interim scheduler |
| ECB SDW market data series adapter | deferred | series import via CSV shape only in slice 2d |
| Demo entity with EUR reporting currency | handoff | needs `services/ledger` and `services/org` waves |
| OpenAPI, AsyncAPI, route/event contract test, Helm chart `passage-fx`, CronJobs, PrometheusRule, Dockerfile, `cmd/fx` | PSG-1202 slice 2e | no HTTP surface yet |
| Web workspace `apps/web/src/modules/fx/` | slice 4 of PSG-12 | depends on the API |
| Revaluation batch scheduled in dev | handoff | `deploy/` is outside the owned paths of this ticket |
| ISO 4217 reference data | handoff | wave 6 owns it; `money.MinorUnits` is used meanwhile |
