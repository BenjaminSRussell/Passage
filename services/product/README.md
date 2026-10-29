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
| PRD-NUM-FORMAT | 422 | product number fails `^[A-Z0-9][A-Z0-9._-]{0,39}$` |
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
