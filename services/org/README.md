# org

Organization service for Passage (wave 6). Slice 1 ships legal entities, the pure hierarchy as-of logic and unit-of-measure conversion. Create also writes `dev.passage.org.legalentity.created.v1` to the `outbox` table in the same transaction (an Idempotency-Key replay writes none), and PATCH writes `dev.passage.org.legalentity.updated.v1` with `{id, changed_fields, version}` in the same transaction when a value changed; operating units write `dev.passage.org.operatingunit.created.v1` on create and `dev.passage.org.operatingunit.updated.v1` on a version bump the same way; sites, which are legal-entity-scoped, write `dev.passage.org.site.created.v1` and `dev.passage.org.site.updated.v1` the same way; warehouses, which take their legal entity from their site, write `dev.passage.org.warehouse.created.v1` and `dev.passage.org.warehouse.updated.v1` the same way; events are declared in `events/asyncapi.yaml`. Everything else is listed in `NOT-IMPLEMENTED.md`.

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
| `POST /v1/org/legal-entities` (required `timeZone`, IANA name; optional `defaultLanguage` default `en-US`, `registrationNumber`, `Idempotency-Key`; country, currency and language must be active; 500 per tenant) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities` (`limit` max 100, `cursor`; filters `status` active or inactive, `country`, `currency`; returns `{items, nextCursor}`) | `org.read` |
| `PATCH /v1/org/legal-entities/{id}` (body `version` required, optional `name`, `timeZone`, `defaultLanguage`, `registrationNumber`, `status`; code, country, currency immutable) | `org.legal-entity.write` |
| `GET /v1/org/legal-entities/{id}`, `GET /v1/org/legal-entities/{id}/names` (name history) | `org.read` |
| `POST /v1/org/operating-units` (body `type` department, cost_center, business_unit or value_stream, `code` upper-cased and unique per type, `name`; optional `defaultLegalEntityId`, `managerPartyId` for cost centers, `Idempotency-Key`) and `PATCH /v1/org/operating-units/{id}` (body `version` required, optional `name`, `status`, `defaultLegalEntityId`, `managerPartyId`; `""` clears an id; type and code immutable) | `org.operating-unit.write` |
| `POST /v1/org/sites` (body `legalEntityId`, `code` upper-cased and unique per legal entity, `name`; optional `timeZone` defaulting to the legal entity's, `Idempotency-Key`) and `PATCH /v1/org/sites/{id}` (body `version` required, optional `name`, `timeZone`, `status`; legal entity and code immutable) | `org.site.write` |
| `GET /v1/org/sites` (`limit`, `cursor`; filters `legalEntityId`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/sites/{id}` | `org.read` |
| `POST /v1/org/warehouses` (body `siteId`, `code` upper-cased and unique per legal entity, `name`; optional `type` standard, transit, quarantine or subcontractor, `timeZone` defaulting to the site's, `Idempotency-Key`; the legal entity is the site's) and `PATCH /v1/org/warehouses/{id}` (body `version` required, optional `siteId` within the same legal entity, `name`, `type`, `timeZone`, `status`; legal entity and code immutable) | `org.site.write` |
| `GET /v1/org/warehouses` (`limit`, `cursor`; filters `legalEntityId`, `siteId`, `type`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/warehouses/{id}` | `org.read` |
| `GET /v1/org/operating-units` (`limit`, `cursor`; filters `type`, `defaultLegalEntityId`, `status`; returns `{items, nextCursor}`) and `GET /v1/org/operating-units/{id}` | `org.read` |
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
| ORG-LE-NOT-FOUND, ORG-VERSION-CONFLICT | 404, 409 | unknown, malformed or out-of-scope legal-entity id; stale version |
| ORG-LE-REFERENCE-INACTIVE, ORG-LE-LIMIT | 422 | country, currency or default language unknown or inactive for the tenant; 500 legal entities already |
| ORG-OU-NOT-FOUND | 404 | unknown or malformed operating-unit id |
| ORG-OU-CODE-DUPLICATE | 409 | operating-unit code already used for the type in the tenant |
| ORG-OU-LEGAL-ENTITY-INVALID | 422 | default legal entity unknown, inactive or outside the caller's scope |
| ORG-SITE-NOT-FOUND | 404 | unknown, malformed or out-of-scope site id |
| ORG-SITE-CODE-DUPLICATE | 409 | site code already used in the legal entity |
| ORG-SITE-LEGAL-ENTITY-INVALID | 422 | legal entity unknown, inactive or outside the caller's scope |
| ORG-WH-NOT-FOUND | 404 | unknown, malformed or out-of-scope warehouse id |
| ORG-WH-CODE-DUPLICATE | 409 | warehouse code already used in the legal entity |
| ORG-WH-SITE-INVALID | 422 | site unknown, inactive or outside the caller's scope |
| ORG-WH-LE-MISMATCH | 422 | the new site belongs to a different legal entity than the warehouse |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| ORG-REF-NOT-FOUND | 404 | unknown reference code |
| ORG-UOM-NO-PATH | 422 | no conversion within two hops inside one unit class |

Metrics: `org_legal_entities_created_total`, `org_legal_entities_failed_total`, `org_legal_entities_updated_total`, `org_reference_status_changed_total`, `org_operating_units_created_total`, `org_operating_units_updated_total`, `org_sites_created_total`, `org_sites_updated_total`, `org_warehouses_created_total`, `org_warehouses_updated_total` (stdlib atomics rendered as text).
