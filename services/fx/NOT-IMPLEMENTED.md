# Not implemented in PSG-12 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: exchange rate resolution, inverse, triangulation | built | `internal/domain/rates.go` |
| Deliverable 1: provider adapters (ECB, OpenExchangeRates), historical rate API, rate audit store | slice 2 of PSG-12 | line budget; needs store and HTTP layers |
| Deliverable 2: rounding, residual allocation, three-currency conversion | built | `internal/domain/convert.go` |
| Deliverable 2: single-line residual to `rounding_difference_account` (BR-12.4 last sentence) | not built | needs ledger currency policy, owned by `services/ledger` (PSG-11, absent from the worktree) |
| Deliverable 2: realized/unrealized gain-loss accounts | not built | account configuration lives in `services/ledger` (PSG-11 absent); the calculation returns gain and loss amounts |
| Deliverable 3: ledger groups and posting layers (AC2) | slice 2 of PSG-12 | line budget; persistence needs `services/ledger` (PSG-11 absent) |
| Deliverable 4: revaluation calculation, `closing_rate` method, reversal date | built | `internal/domain/reval.go` |
| Deliverable 4: `cumulative_no_reversal` method, run table, posting, reversal entries, batch schedule | not built | posting and run state are owned by `services/ledger` (PSG-11 absent) |
| Deliverable 5: currency translation (CTA) | slice 3 of PSG-12 | line budget; posting side needs `services/ledger` |
| Deliverable 6: market data series | slice 2 of PSG-12 | line budget; needs store, provider import and API |
| AC4: provider outage fallback and alert | slice 2 of PSG-12 | needs provider adapters and the degraded event |
| OpenAPI, AsyncAPI, Helm chart `passage-fx`, Dockerfile, `cmd/fx` | slice 2 and 3 of PSG-12 | no HTTP surface in this slice |
| Web workspace `apps/web/src/modules/fx/` | slice 3 of PSG-12 | depends on the API |
| Revaluation batch scheduled in dev | handoff | `deploy/` is outside the owned paths of this ticket |
| ISO 4217 reference data | handoff | wave 6 owns it; `money.MinorUnits` is used meanwhile |
| Event names | open question | PRD names `dev.passage.fx.rate.published.v1`, the architecture lists `ledger.exchangerate.published.v1`; no event is emitted in this slice |
