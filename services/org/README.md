# org

Organization service for Passage (wave 6). Slice 1 ships legal entities, the pure hierarchy as-of logic and unit-of-measure conversion. Create also writes `dev.passage.org.legalentity.created.v1` to the `outbox` table in the same transaction (an Idempotency-Key replay writes none); events are declared in `events/asyncapi.yaml`. Everything else is listed in `NOT-IMPLEMENTED.md`.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/org`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL` (role without BYPASSRLS).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/org/legal-entities` (required `timeZone`, IANA name; optional `Idempotency-Key`) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities` (`limit` max 100, `cursor`; returns `{items, nextCursor}`) | `org.read` |
| `GET /v1/org/uom/convert?from=&to=&qty=` | any authenticated principal |
| `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Paths follow the architecture spine (`/v1/<context>/<collection>`), not the PRD's `/org/v1`. The chart is `charts/org` per CONVENTIONS, not the PRD's `passage-org`. The legal-entity header is `X-Passage-Legal-Entity`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation (`errors` names the fields), bad `from`, `to` or `qty`, unknown `timeZone` |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error with hidden detail |
| ORG-LE-CODE-DUPLICATE | 409 | legal-entity code already used in the tenant |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| ORG-UOM-NO-PATH | 422 | no conversion within two hops inside one unit class |

Metrics: `org_legal_entities_created_total`, `org_legal_entities_failed_total` (stdlib atomics rendered as text).
