# revrec

Passage revenue recognition context (wave 29, PSG-29). Slice 1 delivers the pure
ASC 606 / IFRS 15 allocation: a standalone-selling-price (SSP) allocation of a
transaction price across performance obligations (BR-29.1, BR-29.3) and the ratable
schedule of each obligation. It is a stateless preview: nothing is stored and no
event is emitted. Contracts, recognition runs, roll-forward and the deferred
revenue postings arrive in a later slice (see `NOT-IMPLEMENTED.md`).

## Layout

```
cmd/revrec/             entrypoint: httpx router on HTTP_ADDR, /metrics on ADMIN_ADDR (no database in slice 1)
internal/domain/        pure rules: Allocate (proportional to SSP, residual rule), RatableSchedule
internal/api/           POST /v1/revrec/allocations:preview, validation, DTOs
api/openapi.yaml        OpenAPI 3.1 for the preview endpoint
events/asyncapi.yaml    AsyncAPI 3, no channels in slice 1 (explicit)
charts/revrec/          Helm chart (deployment, service with admin port, ServiceMonitor toggle)
```

## Endpoint

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/revrec/allocations:preview` | `revrec.view` (RevRecView) | pure computation; 200 with per-obligation allocation and schedules |

The duty is enforced with `platform/authz` (401 without a principal, 403 `AUTH-0003`
without it). The billing web module's "Revenue allocation" page calls this endpoint.

Example BR-29.1 bundle: hardware SSP 8,000.00, installation 1,500.00 (milestone),
support 2,500.00 (ratable 12 months), price 10,800.00 gives 7,200.00 / 1,350.00 /
2,250.00; the 12-month support schedule has 133.06 for the first partial month
(22/31 of 187.50) and the last period absorbs the rounding residual.

## Run

```
HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/revrec
```

## Test

```
go vet ./... && go test ./...
```

Table-driven domain tests cover the BR-29.1 worked example, the 10,799.99 residual
case (residual 0.02 to the largest obligation), SSP rules and the ratable schedule
rounding. The API test covers the preview response and validation.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Validation failed (see `errors` per field) or unknown residual rule |
| `AUTH-0001` | 401 | Missing or malformed tenant context |
| `AUTH-0003` | 403 | Missing duty `revrec.view` |
| `REV-0001` | 422 | Rule violation: no obligations, non-positive SSP, negative price |
| `REV-0002` | 422 | Ratable term ends before it starts |

## Notes

- No database in slice 1, so `DATABASE_URL` in the chart is optional
  (`optional: true`) and the store, migrations and outbox arrive with the contracts slice.
- Shares are truncated to the minor unit and the residual goes to the rule's
  obligation, which reproduces the BR-29.1 example exactly (1,349.99875 → 1,349.99).
- Package-level regular expressions in `internal/api` are compiled once and never
  mutated; they are the only package-level values besides constants.
- Dependencies: chi, shopspring/decimal, rapid (tests), platform kernel only.
