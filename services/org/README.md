# org

Organization service for Passage (wave 6). Slice 1 ships legal entities, the pure hierarchy as-of logic and unit-of-measure conversion. Create also writes `dev.passage.org.legalentity.created.v1` to the `outbox` table in the same transaction (an Idempotency-Key replay writes none), and PATCH writes `dev.passage.org.legalentity.updated.v1` with `{id, changed_fields, version}` in the same transaction when a value changed; events are declared in `events/asyncapi.yaml`. Everything else is listed in `NOT-IMPLEMENTED.md`.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/org`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL` (role without BYPASSRLS).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## Reference data

Countries, currencies, regions and languages are platform-owned rows under the `sys` tenant (`00000000-0000-0000-0000-000000000000`): every tenant can read them, no tenant can insert, update or delete them. The data lives in `migrations/reference/countries.csv`, `currencies.csv`, `regions.csv` and `languages.csv`, embedded as `migrations.Reference`, and `store.SeedReference` loads it after the migrations when `MIGRATE_ON_START=true`. Regions cover US, CA, IN and MX as full ISO 3166-2 codes (`US-CA`). A malformed row fails the whole file with `<file> line <n>: <reason>`; re-running the seed changes nothing. Tenants never edit the `sys` rows: `PATCH /v1/org/{collection}/{code}` deactivates or reactivates a row for the calling tenant only, by writing `reference_status`, and every read returns the tenant's effective status.

## API

| Operation | Duty |
|---|---|
| `POST /v1/org/legal-entities` (required `timeZone`, IANA name; optional `defaultLanguage` default `en-US`, `registrationNumber`, `Idempotency-Key`; code is upper-cased; country, currency and language must be active for the tenant; at most 500 per tenant) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities` (`limit` max 100, `cursor`; filters `status` active or inactive, `country`, `currency`; returns `{items, nextCursor}`) | `org.read` |
| `GET /v1/org/legal-entities/{id}` | `org.read` |
| `PATCH /v1/org/legal-entities/{id}` (body `version` required, optional `name`, `timeZone`, `defaultLanguage`, `registrationNumber`, `status`; code, country and currency are immutable; emits `legalentity.updated` only when a value changed) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities/{id}/names` (name history, newest first; returns `{items}`) | `org.read` |
| `GET /v1/org/{countries,currencies,regions,languages}` (`status` active or inactive; `country` for regions; returns `{items}`) and `GET /v1/org/{countries,currencies,regions,languages}/{code}` | `org.read` |
| `PATCH /v1/org/{countries,currencies,regions,languages}/{code}` (body `{"status":"active"\|"inactive"}`; not for legal-entity-scoped callers) | `org.reference.write` |
| `GET /v1/org/uom/convert?from=&to=&qty=` | any authenticated principal |
| `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Paths follow the architecture spine (`/v1/<context>/<collection>`), not the PRD's `/org/v1`. The chart is `charts/org` per CONVENTIONS, not the PRD's `passage-org`. The legal-entity header is `X-Passage-Legal-Entity`.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation (`errors` names the fields), bad `from`, `to` or `qty`, unknown `timeZone` |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error with hidden detail |
| ORG-LE-CODE-DUPLICATE | 409 | legal-entity code already used in the tenant |
| ORG-LE-CODE-RESERVED | 400 | legal-entity code is DAT, SYS or ALL |
| ORG-LE-NOT-FOUND | 404 | unknown legal entity, malformed id, or outside the caller's legal-entity scope |
| ORG-VERSION-CONFLICT | 409 | stale version; re-read with GET |
| ORG-LE-REFERENCE-INACTIVE | 422 | country, currency or default language is unknown or inactive for the tenant (`errors` names the field) |
| ORG-LE-LIMIT | 422 | the tenant already has 500 legal entities |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| ORG-REF-NOT-FOUND | 404 | unknown reference code |
| ORG-UOM-NO-PATH | 422 | no conversion within two hops inside one unit class |

Metrics: `org_legal_entities_created_total`, `org_legal_entities_failed_total`, `org_legal_entities_updated_total`, `org_reference_status_changed_total` (stdlib atomics rendered as text).
