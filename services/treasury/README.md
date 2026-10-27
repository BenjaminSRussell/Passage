# treasury

Treasury bounded context (Go). Wave 36, slice 1 (PSG-36): bank accounts and
their lifecycle, bank statement import (camt.053), the cash forecast, and the
pure matching-score engine (domain only, not yet behind an API). Screens live
in `apps/web/src/modules/treasury`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_treasury
export MIGRATE_ON_START=true          # dev only; stage/prod run migrations as a PreSync Job
export TREASURY_MAX_FILE_MB=50        # 1-200, default 50
go run ./cmd/treasury                 # HTTP :8080, admin (/metrics) :9090
```

Env: `DATABASE_URL` (required), `MIGRATE_ON_START`, `TREASURY_MAX_FILE_MB`,
`HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`).

## Test

```sh
go vet ./... && go test -count=1 ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test -count=1 ./...   # runs DB tests
```

Store tests need `PASSAGE_TEST_DATABASE_URL` (admin, applies migrations) and
`PASSAGE_TEST_APP_DATABASE_URL` (a non-superuser role, so row-level security
applies); they skip without them. API tests use an in-memory fake store and
always run, including the OpenAPI-matches-routes contract test.

## Layout

- `internal/domain`: bank accounts, statement and forecast invariants (pure, no I/O).
- `internal/store`: pgx repositories, tenant-scoped transactions (RLS).
- `internal/api`: HTTP handlers, RFC 9457 problems, idempotency and `If-Match`.
- `internal/events`: event type constants and payloads.
- `migrations/0001_init.sql`: tables `bank_account`, `bank_statement`,
  `bank_statement_line`, `cash_forecast_entry`, `idempotency_keys`, `outbox`.
  Every tenant table has `tenant_id` first, RLS enabled and forced, and the
  legal-entity scope policy where it carries `legal_entity_id`.
- `api/openapi.yaml`, `events/asyncapi.yaml`: contracts.
- `charts/treasury`: Helm chart (`/healthz`, `/readyz`, ServiceMonitor toggle).

## Routes

Bank accounts, statements, and cash forecast under `/v1/treasury/...`. See
`api/openapi.yaml` for the full list. `POST bank-accounts` and
`POST cash-forecast-entries` require `Idempotency-Key` (stored in
`idempotency_keys` in the same transaction as the insert). `:activate`,
`:deactivate` and `:close` require `If-Match` with the account ETag and the
`treasury.bank-account.approve` duty. Statement import is idempotent on the
file's SHA-256 (BR-36.8).

Duties: `treasury.view`, `treasury.bank-account.write`,
`treasury.bank-account.approve`, `treasury.statement.import`,
`treasury.forecast.write` (the PRD's `TreasuryView`,
`TreasuryBankAccountMaintain` and `TreasuryStatementImport` in the kernel's
dotted naming).

## Error codes

Problems use `type` `https://passage.dev/problems/<code>` and carry `code` and
`serviceCode`.

| code | HTTP | meaning |
|------|------|---------|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `COM-0001` | 400 | request failed validation (field errors in `errors`) |
| `TRS-0400` | 404 | bank account not found |
| `TRS-0401` | 422 | IBAN fails shape or mod-97 check |
| `TRS-0403` | 422 | legal entity holds 500 bank accounts already |
| `TRS-0404` | 409 | duplicate bank account code or IBAN |
| `TRS-0405` | 200 | identical file, or the same statement id with the same closing balance, re-uploaded: returns the existing statement (replay, not an error) |
| `TRS-0406` | 409 | same statement id with a different closing balance |
| `TRS-0407` | 201 (warning) | electronic sequence number skips numbers; returned in `warnings`, the statement is stored |
| `TRS-0408` | 422 | statement currency differs from the account currency |
| `TRS-0409` | 422 | statement IBAN is not the bank account it was uploaded to |
| `TRS-0412` | 422 | statements can only be imported into an active account |
| `TRS-0413` | 404 | statement not found |
| `TRS-0417` | 422 | account status does not allow the requested transition |
| `TRS-0490` | 413 | statement file exceeds `TREASURY_MAX_FILE_MB` |
| `TRS-0491` | 400 | statement file could not be read |
| `TRS-0493` | 422 | statement cannot be parsed (camt.053 only; one Stmt per file) |
| `TRS-0494` | 422 | opening balance plus lines does not equal closing balance |
| `conflict_version` | 412 | `If-Match` does not match the current ETag |
| `precondition_required` | 428 | `If-Match` is required on activate, deactivate and close |
| `idempotency_key_reuse` | 422 | `Idempotency-Key` reused with a different body |

## Dependencies

Only the platform module and what it pulls in (chi, pgx/v5, shopspring/decimal,
google/uuid). No new dependencies.
