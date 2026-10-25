# fx

Wave 12 (PSG-12): the pure FX domain engine in `internal/domain` and the rate store in `internal/store`. No service entrypoint yet.

## Run and test

```
cd services/fx
go vet ./...
go test ./...
```

## What is built

- `rates.go`: append-only rate book, lookup order exact, inverse (only when the rate type allows it), triangulation through a reference currency, else missing.
- `convert.go`: single-rounding conversion, residual allocation (BR-12.4), half-away-from-zero and half-even.
- `internal/store`: tenant-scoped rate types and versioned rates; a correction supersedes the current row, a lower-priority source (`ecb 10, openexchangerates 20, csv 30, manual 40`) arrives already superseded, and each row enqueues `dev.passage.fx.rate.published.v1` in the same transaction. `migrations/0001_init.sql` creates the tables.
- `internal/provider`: `Provider` adapters returning `RatePoint`s. `ecb` parses `eurofxref-daily.xml`, `openexchangerates` parses `latest.json` / `historical/{date}.json` and reports disabled (no request made) without an app id, `csv` parses `type,from,to,valid_from,rate` and rejects bad rows with the line number. `PayloadHash` is the SHA-256 of the record: the canonical `<Cube currency="USD" rate="1.0834"/>` for ECB, `"EUR":0.923` for OpenExchangeRates, the raw line for CSV. ECB and OpenExchangeRates rates beyond 10 decimals are rounded to 10; CSV rejects them. Recorded fixtures live in `testdata/providers/`.
- Run log and idempotency store methods: `StartRun` (one row per provider, rate type and run date; a succeeded key is returned as `done`), `RecordAttempt`, `FinishRunSucceeded`, `FinishRunFailed` (also enqueues `dev.passage.fx.provider.degraded.v1`), `ListRuns`, `LastSuccess`, `GetIdempotency`, `PutIdempotency` (`migrations/0002_idempotency.sql`). `PostRates` reports `Unchanged` for an identical current row and adds `previous_rate` and `deviation_pct` to `dev.passage.fx.rate.published.v1`.
- `reval.go`: closing-rate revaluation with reversal on period end plus one day (BR-12.8, BR-12.9).

## Error codes

| Sentinel | Code | Maps to (service convention) |
|---|---|---|
| `ErrRateMissing` | `LEDGER_RATE_MISSING` | `FX-0001`, 422 |
| `ErrRoundingExceeded` | `LEDGER_ROUNDING_EXCEEDED` | `FX-0003`, 422 |
| `ErrUnbalanced` | `LEDGER_UNBALANCED` | 422 |
| `ErrFutureRate` | `FX_RATE_DATE_FUTURE` | 422 |
| `ErrInvalidRate` | `FX_RATE_INVALID` | 400 |

The API layer that maps these to problems arrives in a later slice. `FX_RATE_STALE` is a warning, not an error, and lands with the API.

## Dependencies

No module beyond what `platform/go` already requires: `shopspring/decimal` for amounts and rates, `pgregory.net/rapid` for property tests. `platform` is consumed through the `replace` directive.

See `NOT-IMPLEMENTED.md` for the deliverables outside this slice.
