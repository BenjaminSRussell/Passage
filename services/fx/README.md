# fx

Wave 12 (PSG-12), slice 1: the pure FX domain engine in `internal/domain`. No service entrypoint yet.

## Run and test

```
cd services/fx
go vet ./...
go test ./...
```

## What is built

- `rates.go`: append-only rate book, lookup order exact, inverse (only when the rate type allows it), triangulation through a reference currency, else missing.
- `convert.go`: single-rounding conversion, residual allocation (BR-12.4), half-away-from-zero and half-even.
- `reval.go`: closing-rate revaluation with reversal on period end plus one day (BR-12.8, BR-12.9).

## Error codes

| Sentinel | Code | Maps to (service convention) |
|---|---|---|
| `ErrRateMissing` | `LEDGER_RATE_MISSING` | `FX-0001`, 422 |
| `ErrRoundingExceeded` | `LEDGER_ROUNDING_EXCEEDED` | `FX-0003`, 422 |
| `ErrUnbalanced` | `LEDGER_UNBALANCED` | 422 |
| `ErrFutureRate` | `FX_RATE_FUTURE` | 422 |
| `ErrInvalidRate` | `FX_RATE_INVALID` | 400 |

The API layer that maps these to problems arrives in a later slice. `FX_RATE_STALE` is a warning, not an error, and lands with the API.

## Dependencies

No module beyond what `platform/go` already requires: `shopspring/decimal` for amounts and rates, `pgregory.net/rapid` for property tests. `platform` is consumed through the `replace` directive.

See `NOT-IMPLEMENTED.md` for the deliverables outside this slice.
