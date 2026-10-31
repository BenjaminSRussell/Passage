# tax

Wave 13 deployable: tax determination, calculation, provider failover and settlement reconciliation. This slice ships the pure tax engine and a stateless calculate endpoint. One Go module, one image (`tax`). Deferred deliverables are listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/tax
go vet ./... && go test ./...
DATABASE_URL=postgres://... go run ./cmd/tax
```

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |
| `TAX_PROVIDER_TIMEOUT_MS` | external provider timeout before the internal engine takes over; default 2000 |

## API

`POST /v1/tax/calculate` (duty `Calculate tax`), `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. The browser reaches it as `/api/v1/tax/calculate`; the BFF rewrites the prefix. Request and response shapes are in `internal/domain/model.go`; `api/openapi.yaml` is a minimal duty-annotated stub; the full contract is slice B.

The request carries the document and an inline `config` (tax groups, item groups, codes with effective-dated rates, exemption certificates) because no tax store exists yet. Order of operations follows PRD BR-13.1; every step writes to `determination_trace`. Amounts and percentages are decimal strings with at most 6 decimal places.

Rounding: `per_line` rounds each line's tax per code; `per_document` rounds the code total once and distributes it to lines by largest remainder. For price-inclusive lines the base is the gross less the rounded taxes, so base plus taxes always equals the gross and the document tax equals the rounded sum of unrounded taxes. Brackets on inclusive lines are evaluated on the gross.

### Withholding

`POST /v1/tax/withholding/calculate` (duty `Calculate withholding tax`) is stateless like calculate. Body: `currency`, `party_type` (`vendor` or `customer`), `payment_date`, `code`, `gross_amount` (positive), `paid_to_date` (cumulative paid to this party this year before this payment, default 0) and inline `codes` (`code`, `jurisdiction`, `income_type`, effective-dated `rates` with `rate_pct`, optional `threshold`). With a threshold, only the part of `paid_to_date + gross_amount` above it is withheld (550 paid, 100 invoice, threshold 600: base 50). `gross_amount` may not have more decimals than the currency's minor unit, and `codes` must be unique. Tax is rounded to the currency minor unit. Response: `taxable_base`, `withholding_amount`, `net_payable`, `rate_pct` and `determination_trace`. Errors: TAX-0001 (no rate on the date), TAX-0002 (rate outside 0 to 100 or overlapping rates), TAX-0003 (requested code not in `codes`), COM-0001. Payables calls it with a signed service token; `service_grants.payables.tax` in `policies/authz/data.json` (mirrored in `platform/go/tenant`) grants it the duty. Metric: `tax_withholding_calculations_total`.

Naming: the PRD calls the chart `passage-tax`; existing charts are named after the service, so the chart is `charts/tax` (slice B).

## Error codes

| Code | Status | Meaning |
|---|---|---|
| TAX-0001 | 422 | no rate valid on the tax date |
| TAX-0002 | 422 | rate outside 0 to 100, or overlapping rates |
| TAX-0003 | 422 | party tax group or item tax group not resolvable |
| TAX-0007 | 422 | named provider is not configured; warning when the internal engine replaced a failed provider |
| COM-0001 | 400 | malformed body, unknown field, wrong JSON type or invalid field |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | caller lacks the duty (kernel authz) |

Warnings in the response `warnings` array: TAX-0004 (no code shared by the groups), TAX-0005 (certificate not valid on the tax date), TAX-0006 (reverse charge ineligible), TAX-0007 (provider fallback). TAX-0008 is returned by the settlement domain when a run differs from the GL movement.

## Metrics

`tax_calculations_total`, `tax_calculation_failures_total`, `tax_provider_fallbacks_total`.

## Handoffs outside the owned paths

- `policies/authz/data.json`: grant the duty `Calculate tax` to the service roles that call the endpoint.
- `deploy/argocd`, the CI matrix and root `lint-charts`: add the tax application once the chart exists (slice B).
- Wave 11 `services/ledger/`: the settlement domain posts through the `Poster` interface with key `post:tax:settlement:<id>:settle`; the real client replaces the interface when the ledger exists.
