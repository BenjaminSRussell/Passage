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

`POST /v1/hello-ledger/entries` (optional `Idempotency-Key`), `GET /v1/hello-ledger/entries`, `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. `POST /v1/platform/releases` (duty `Maintain platform releases`, optional `Idempotency-Key`) records a promote or rollback and `GET /v1/platform/releases?service=&env=` (duty `View platform releases`) lists them. Contract: `api/openapi.yaml`.

## Releases

`platform_release` is append-only and `platform_environment` (seeded dev, stage, prod) is read-only for `passage_app`. Releases are platform-level, not customer data: rows are stored and read under the nil system tenant (`00000000-0000-0000-0000-000000000000`) server-side, so any caller holding the duty sees every release; the caller's tenant only authenticates the request. Each new release writes `dev.passage.platform.release.promoted.v1` or `dev.passage.platform.release.rolledback.v1` to the `outbox` table in the same transaction (an idempotent replay writes none). The event fires when the release is recorded, not on Argo sync Healthy. `deploy/scripts/stage_release.py` posts these rows when `PASSAGE_PLATFORM_API_URL` is set.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, `message` not 1 to 280 characters, or an invalid release (service, environment, action, digest, unregistered environment) |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | missing duty |

## Metrics

`hello_ledger_entries_created_total`, `hello_ledger_entries_failed_total`, `hello_ledger_releases_recorded_total`, `hello_ledger_releases_failed_total`, `hello_ledger_http_requests_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
