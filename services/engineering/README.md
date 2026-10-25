# engineering

Wave 9 (PSG-9): engineering data for the `product` context: BOMs, routings, engineering change orders. Slice 1 is the pure domain kernel, slice 2 adds the store, HTTP API, specs, chart and web module. What is not built is in `NOT-IMPLEMENTED.md`.

## Layout

- `cmd/engineering/main.go`: wiring. Env: `DATABASE_URL` (required, from Secret `engineering-db`), `MIGRATE_ON_START=true` (dev only), `HTTP_ADDR` (default `:8080`), `ADMIN_ADDR` (default `:9090`, serves `/metrics`).
- `internal/domain/bom.go`: as-of version resolution, multi-level explosion, implosion (where-used), cycle and overlap checks, cost roll-up skeleton.
- `internal/domain/routing.go`: operation DAG validation and lead time (sequential sum, parallel branches take the max, efficiency).
- `internal/domain/eco.go`: ECO state machine, `Approver` seam to the Temporal approval workflow, release plan (expire old at `release_date - 1`, activate new).
- `internal/store`: pgx repositories under `dbx.WithTenantTx`; `Catalog` loads only the BOM versions reachable from the requested products (recursive query, down for explosion, up for where-used) into a `Snapshot` that satisfies `domain.Catalog`. `ReleaseEco` applies a `ReleasePlan` and enqueues its events in one transaction.
- `internal/api`: handlers, validation, duties, metrics, readiness.
- `internal/events/events.go`: event types and payloads.
- `migrations/0001_init.sql`: tables with `tenant_id` first, tenant RLS (enabled and forced), idempotency and outbox tables.
- `api/openapi.yaml`, `events/asyncapi.yaml`, `charts/engineering`, `Dockerfile`.
- `testdata/`: `bom-golden-7x2000.json` (7 levels, 2,000 BOM lines), `routing-leadtime.json` (12 fixtures).

## Routes and duties

| Route | Duty |
|---|---|
| `POST/GET /v1/engineering/bom-versions` | `engineering.bom.write` / `engineering.read` |
| `POST /v1/engineering/boms:explode`, `GET /v1/engineering/boms:where-used`, `POST /v1/engineering/routes:lead-time` | `engineering.read` |
| `POST /v1/engineering/ecos` | `engineering.bom.write` |
| `POST /v1/engineering/ecos/{id}:release` | `engineering.eco.approve` |

`engineering.route.write` (PRD 1.9.17) has no route until routing persistence exists. Creating a BOM version defaults to `draft`; `active` is accepted so a first version can exist without an ECO, and is checked for overlap. An ECO is created `submitted`; `:release` asks the approver, then releases or schedules it.

## Error codes

`ENG-BOM-CYCLE`, `ENG-BOM-OVERLAP`, `ENG-BOM-NOVERSION`, `ENG-BOM-LEVELS`, `ENG-BOM-QTY`, `ENG-BOM-LINE`, `ENG-BOM-COMP-OBSOLETE`, `ENG-BOM-PHANTOM-NOBOM`, `ENG-ROUTE-QTY`, `ENG-ROUTE-NEXT-MISSING`, `ENG-ROUTE-SECONDARY-NOPRIMARY`, `ENG-ROUTE-CYCLE`, `ENG-ECO-STATE` (409), `ENG-ECO-NOT-APPROVED`, `ENG-ECO-DRYRUN`, `ENG-ECO-DATE` (all other domain codes map to 422), plus store codes `ENG-ECO-NOTFOUND` (404), `ENG-BOM-VERSION-NOTFOUND` (404), `ENG-DUPLICATE` (409) and `idempotency_key_reuse` (422). The PRD 1.9.18 catalog names the first seven present codes where they exist; `NOVERSION`, `LEVELS`, `QTY`, `LINE`, `ROUTE-QTY`, `ROUTE-CYCLE`, `ECO-STATE`, `ECO-DRYRUN`, `ECO-DATE` and the store codes are not in the catalog yet and must be reconciled with it.

## Event types

The PRD names `dev.passage.engineering.bom.version.activated.v1`, which has four segments after the context and fails `platform/go/events.ValidType`. The entity is folded into one segment instead: `bomversion`, `routeversion` (as `releasedproduct` is in the product service). Emitted: `bomversion.activated`, `bomversion.expired`, `eco.submitted`, `eco.approved`, `eco.released`.

## Dependencies

`platform` (events, outbox, dbx, httpx, authz, money, ids), `shopspring/decimal` (exact quantities; float64 would break the golden), `pgregory.net/rapid` (property tests), `go-chi/chi/v5` and `jackc/pgx/v5` (the versions the kernel pins; `Register(chi.Router)` and `pgx.Tx` are the kernel's own signatures). All are already used by `services/org` and `services/product`.
