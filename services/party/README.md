# party

Global party and business partner service (PSG-7). This slice delivers the Fellegi-Sunter duplicate engine, tax registration validation with a VIES adapter, and the deployable skeleton. See `NOT-IMPLEMENTED.md` for everything else.

## Run

```
HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/party
```

`/healthz`, `/readyz` and `/v1/party/...` are served on `HTTP_ADDR`; Prometheus `/metrics` on `ADMIN_ADDR`. Tenant headers are trusted behind the gateway; JWT validation arrives with wave 2.

## Test

```
go vet ./... && go test ./...
make lint-charts
```

No database is needed in this slice.

## Duties

| Duty | Grants |
|---|---|
| `party.write` | `POST /v1/party/tax-registrations:validate` |
| `party.read` | reserved for the read endpoints of the next slice |

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `COM-0001` | 400 | malformed body, unknown field, wrong type, malformed legal entity |
| `COM-0500` | 500 | internal error, detail hidden |
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty |
| `PTY-0001` | 422 | tax registration failed format validation; `errors` names the field |

## Metrics

`party_tax_validations_total{status}`, `party_tax_validations_rejected_total`, `party_tax_checker_failures_total`.

VAT numbers are format-checked only: without a live VIES client the validation status is `unverified`. `VIES_FIXTURE=1` swaps in the recorded test fixture for dev and test; never set it in production.
