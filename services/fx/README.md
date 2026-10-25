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
