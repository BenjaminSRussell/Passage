# product

Product information management for Passage (wave 8). Tenant-level products and masters, variant explosion, released products with site/warehouse default cascade, item and item-model groups, attributes, GS1 identifiers, lifecycle process policies, relations, service products, and a constraint configurator (in-process solver; optional Python CP-SAT worker).

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/product`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL`. RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |
| `CONFIGURATOR_URL` | optional HTTP base of `configurator-py`; empty uses the in-process solver |

`pgregory.net/rapid` is test-only (property tests). Extra module justification: none beyond the platform set plus rapid for AC-8.3.

## API

| Operation | Duty |
|---|---|
| `POST /v1/product/products` (optional `Idempotency-Key`) | `product.write` |
| `GET /v1/product/products`, `GET /v1/product/products/{id}`, `GET /v1/product/products:search` | `product.read` |
| `POST /v1/product/products/{id}/variants:explode` (optional body `limit` 0-500 creates at most that many variants and reports `remaining`; repeat until it is 0) | `product.write` |
| `GET /v1/product/released-products?after=&limit=` (keyset page of released product ids, `next` empty on the last page, limit 1-500) | `product.read` |
| `GET /v1/product/products/{id}/variants:lookup` | `product.read` |
| `POST /v1/product/releases` | `product.release` |
| `PATCH /v1/product/released-products/{id}`, `PUT .../site-defaults/{siteId}` | `product.released.write` |
| `GET /v1/product/released-products/{id}/process-policy?process=` | `product.read` |
| `POST /v1/product/gtins:validate`, `GET /v1/product/gtins/{gtin}` | `product.read` |
| `POST /v1/product/gtins` | `product.gtin.allocate` |
| `POST /v1/product/configuration-models/{id}:solve` | `product.configure` |
| `POST /v1/product/setup/*` | `product.setup.write` |
| `GET /v1/product/uom/convert` | `product.read` |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`, `events/asyncapi.yaml`. Paths follow `/v1/<context>/...`. The legal-entity header is `X-Passage-Legal-Entity`.

Wave 3 metadata and wave 4 Temporal/Kafka are not imported. Explosion runs in-process with 500-row outbox checkpoints. Number sequences and org site/warehouse membership sit behind in-package defaults (manual product numbers; site IDs accepted as UUIDs).

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400/404 | malformed body or missing resource |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| PRD-NUM-FORMAT | 422 | product number fails `^[A-Z0-9][A-Z0-9._-]{0,39}$`; 409 when the number exists (`errors.existingId` is the existing product id) |
| PRD-ID-TAKEN | 409 | caller-supplied product id (import duty only) is already in use |
| PRD-VERSION-CONFLICT / PRD-VERSION-REQUIRED | 412 / 428 | `PATCH /v1/product/products/{id}` with a stale or missing `If-Match` version |
| PRD-ORG-SERVICE-CALLER | 503 | a service caller placed a site or warehouse and product cannot sign the org lookup (no signing key or no `product -> org` grant) |
| PRD-VAR-CAP | 422 | Cartesian product exceeds the tenant cap |
| PRD-VAR-DIMVAL-UNKNOWN | 422 | dimension value not in the group |
| PRD-RELEASE-DRAFT | 422 | draft products cannot be released |
| PRD-RELEASE-DEFAULT-MISSING | 422 | no profile or site default for a required field |
| PRD-DIMGRP-LOCKED | 409 | inventory transactions lock the dimension/model group |
| PRD-GTIN-CHECKDIGIT | 422 | GS1 mod-10 failure |
| PRD-GTIN-DUPLICATE | 409 | GTIN already assigned in the tenant |
| PRD-CFG-INFEASIBLE | 422 | selections conflict; `iis` lists constraints |
| PRD-ATTR-REQUIRED / PRD-ATTR-RANGE | 422 | missing or out-of-range attribute at activate |
| PRD-LC-BLOCKED / PRD-LC-WARN | 422 / 200 | process policy |
| PRD-REL-SELF | 422 | self-relation |
| PRD-SVC-STOCKED | 422 | storage/tracking or stocked model group on a service |
| PRD-GRPCHG-ONHAND | 200 (item) | group-change skipped because on-hand > 0 |

Metrics: `product_created_total`, `product_exploded_total`, `product_released_total`, `product_failed_total`.

## Migration loader (duty `product.import.write`)

The migration service loads legacy items as `service:migration-loader`. Duty `product.import.write`
is accepted next to the ordinary duty on `POST /v1/product/products` (the only route where a
caller-supplied `id` is honoured), `PATCH /v1/product/products/{id}` (new; `If-Match` = version; name,
description, searchName, lifecycleState), `POST .../products/{id}/attributes|identifiers|relations`,
`POST /v1/product/releases` and `PATCH /v1/product/released-products/{id}`. Release and the released
patch stay inside the legal entity of the token (403 `AUTH-0003` otherwise). It opens no other route.

- A supplied `id` must be the hyphenated form of an RFC 9562 UUID of version 1 to 8 (nil, max and
  braces/URN forms are 400) and is stored lower-cased. Ids and numbers are unique per tenant only, so
  a 409 never reveals another tenant's products.
- The `lifecycleState` of the product PATCH follows BR-8.1 (`draft -> active -> phase_out -> active |
  obsolete`, `obsolete` terminal; 422 `PRD-LC-ILLEGAL`) and the move to `active` needs every required
  attribute (BR-8.9, 422 `PRD-ATTR-REQUIRED`/`PRD-ATTR-RANGE`). A PATCH that changes nothing keeps the
  version and writes no `product.updated` event.
- When `ORG_BASE_URL` is set, a release with sites or warehouses is checked with org. A person's call
  forwards their headers; a service caller's lookup goes out under a token product signs itself
  (chart `serviceIdentity.signingKeySecret`, duty `org.read`). Until product holds the grant `product -> org`
  and a signing key, such a release answers 503 `PRD-ORG-SERVICE-CALLER` without calling org or
  writing anything (see NOT-IMPLEMENTED.md).
