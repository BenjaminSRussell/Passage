# platform-health (hello-ledger)

Wave 1 placeholder deployable that proves the delivery path: merge, CI, signed image, Argo CD, running pod behind TLS. One Go module, one image (`hello-ledger`).

## Run and test

```
cd services/platform-health
go vet ./... && go test ./...
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/hello-ledger
```

Store tests run only when `PASSAGE_TEST_DATABASE_URL` is set, against a role without BYPASSRLS.
Local cluster: `make -C deploy deploy-local` (see `docs/runbooks/delivery/deploy-local.md`).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

`POST /v1/hello-ledger/entries` (optional `Idempotency-Key`), `GET /v1/hello-ledger/entries`, `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. Contract: `api/openapi.yaml`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, or `message` not 1 to 280 characters |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |

## Metrics

`hello_ledger_entries_created_total`, `hello_ledger_entries_failed_total`, `hello_ledger_http_requests_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
