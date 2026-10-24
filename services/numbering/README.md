# numbering

Number sequence service for Passage (wave 5). Issues per-legal-entity numbers in `gap_free`, `continuous` or `non_continuous` mode. Gap-free allocation takes a row lock and is proven by a 10k-concurrent Jepsen-style test.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/numbering`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL` (role without BYPASSRLS). RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/numbering/sequences` (optional `Idempotency-Key`) | `numbering.write` |
| `GET /v1/numbering/sequences/{code}` | `numbering.read` |
| `POST /v1/numbering/sequences/{code}/next` (optional `Idempotency-Key`) | `numbering.write` |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Event: `dev.passage.numbering.sequence.allocated.v1`. The legal-entity header is `X-Passage-Legal-Entity`.

Wave 2 OPA seed data names `/v1/number-sequences`; this service follows the `/v1/<context>/<collection>` spine. Wire-up is a handoff to wave 2.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| NUM-0003 | 404 | sequence not found |
| NUM-SEQ-DUPLICATE | 409 | code already used for the legal entity |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| NUM-0002 | 422 | padded format width exhausted |

Metrics: `numbering_allocated_total`, `numbering_failed_total`.
