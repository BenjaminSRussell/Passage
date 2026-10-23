# org

Organization service for Passage (wave 6). Slice 1 ships legal entities, the pure hierarchy as-of logic and unit-of-measure conversion. Everything else is listed in `NOT-IMPLEMENTED.md`.

## Run and test

```
cd services/org
go vet ./... && go test ./...
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/org
```

Store tests run only when `PASSAGE_TEST_DATABASE_URL` is set, against a role without BYPASSRLS.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/org/legal-entities` (optional `Idempotency-Key`) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities` (`limit` max 100, `cursor`; returns `{items, nextCursor}`) | `org.read` |
| `GET /v1/org/uom/convert?from=&to=&qty=` | any authenticated principal |
| `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Paths follow the architecture spine (`/v1/<context>/<collection>`), not the PRD's `/org/v1`. The chart is `charts/org` per CONVENTIONS, not the PRD's `passage-org`. The legal-entity header is the kernel's `X-Passage-Legal-Entity`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation (`errors` names the fields), bad `from`, `to` or `qty` |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant |
| AUTH-0003 | 403 | duty missing |
| ORG-LE-CODE-DUPLICATE | 409 | legal-entity code already used in the tenant |
| ORG-UOM-NO-PATH | 422 | no conversion within two hops inside one unit class |
| ORG-HIER-CYCLE, ORG-HIER-ROOT, ORG-HIER-DUPLICATE, ORG-HIER-DEPTH, ORG-HIER-SIZE, ORG-HIER-OVERLAP | domain | returned by `domain.ValidateNodes` and `ValidateVersions`; no endpoint publishes hierarchies yet |

## Metrics

`org_legal_entities_created_total`, `org_legal_entities_failed_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
