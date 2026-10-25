# party

Global party and business partner service (PSG-7). This slice delivers the Fellegi-Sunter duplicate engine, tax registration format validation, and the deployable skeleton. See `NOT-IMPLEMENTED.md` for everything else.

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 ADMIN_ADDR=:9090 go run ./cmd/party
```

`DATABASE_URL` is required. `MIGRATE_ON_START=true` applies the embedded migrations in `migrations/` at startup. `/readyz` pings the database and returns 503 `{"status":"unavailable"}` when it cannot.

`/healthz`, `/readyz` and `/v1/party/...` are served on `HTTP_ADDR`; Prometheus `/metrics` on `ADMIN_ADDR`. Tenant headers are trusted behind the gateway; JWT validation arrives with wave 2.

## Test

```
go vet ./... && go test ./...
make lint-charts
```

Database tests (`internal/store`, the serve test in `cmd/party`) are skipped unless `PASSAGE_TEST_DATABASE_URL` is set. The RLS tests also need `PASSAGE_TEST_APP_DATABASE_URL`, a connection as the non-bypass `passage_app` role, and fail without it. The database needs the `pg_trgm` extension.

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

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres connection string; the chart reads it from the `existingSecret` secret (`party-db`) |
| `MIGRATE_ON_START` | `false` | `true` applies `migrations/*.sql` at startup |
| `HTTP_ADDR`, `ADMIN_ADDR` | `:8080`, `:9090` | listen addresses |
| `NUMBERING_URL` | empty | numbering service base URL; read by the next slice |
| `PARTY_DEDUP_LIKELY_WEIGHT` | `12` | likely-duplicate weight; read by the next slice |
| `PARTY_DEDUP_POSSIBLE_WEIGHT` | `6` | possible-duplicate weight; read by the next slice |
| `PARTY_VIES_TIMEOUT_MS` | `5000` | tax checker timeout; read by the tax slice |

No new Go dependency: `pgx` and `chi` were already required, and the database helpers come from `platform/go/dbx`.

## Metrics

`party_tax_validations_total{status}`, `party_tax_validations_rejected_total`, `party_tax_checker_failures_total`.

VAT numbers are format-checked only and return `unverified`; this slice has no VIES client.
