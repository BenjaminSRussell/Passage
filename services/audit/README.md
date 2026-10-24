# audit

Append-only field-level audit log for Passage (wave 5). Changes arrive through `POST /v1/audit/changes`, which is the in-process stand-in for a wave-4 Debezium/Kafka consumer (`internal/cdc.Consumer`). Each row is hash-chained; updates and deletes are rejected by a trigger.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/audit`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL`. RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/audit/changes` | `audit.write` |
| `GET /v1/audit/records/{entity}/{recordId}` | `audit.read` |
| `POST /v1/audit/alert-rules` | `audit.write` |
| `GET /v1/audit/alerts` | `audit.read` |
| `POST /v1/audit/texts/expand` | authenticated |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Event: `dev.passage.audit.change.recorded.v1`.

Wave 3 entity metadata is not imported; `entity` is an opaque name. Wave 4 Kafka is not imported; a future consumer calls `cdc.Consumer.Apply`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body or missing change fields |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| AUD-0001 | 422 | domain rejection (invalid change, checkout rules) |

Metrics: `audit_changes_applied_total`, `audit_changes_failed_total`.
