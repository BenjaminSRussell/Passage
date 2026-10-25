# engineering

Wave 9 (PSG-9): engineering data for the `product` context: BOMs, routings, resources, engineering change orders. This module is slice 1, the pure domain kernel. Store, HTTP API, migrations, OpenAPI/AsyncAPI, Helm chart and the web module are slice 2 (see `NOT-IMPLEMENTED.md`).

## Layout

- `internal/domain/bom.go`: as-of version resolution, multi-level explosion, implosion (where-used), cycle and overlap checks, cost roll-up skeleton.
- `internal/domain/routing.go`: operation DAG validation and lead time (sequential sum, parallel branches take the max, efficiency).
- `internal/domain/eco.go`: ECO state machine, `Approver` seam to the Temporal approval workflow, release plan (expire old at `release_date - 1`, activate new).
- `internal/events/events.go`: event types and payloads.
- `testdata/`: `bom-golden-7x2000.json` (7 levels, 2,000 BOM lines), `routing-leadtime.json` (12 fixtures).

## Error codes

`ENG-BOM-CYCLE`, `ENG-BOM-OVERLAP`, `ENG-BOM-NOVERSION`, `ENG-BOM-LEVELS`, `ENG-BOM-QTY`, `ENG-BOM-LINE`, `ENG-BOM-COMP-OBSOLETE`, `ENG-BOM-PHANTOM-NOBOM`, `ENG-ROUTE-QTY`, `ENG-ROUTE-NEXT-MISSING`, `ENG-ROUTE-SECONDARY-NOPRIMARY`, `ENG-ROUTE-CYCLE`, `ENG-ECO-STATE`, `ENG-ECO-NOT-APPROVED`, `ENG-ECO-DRYRUN`, `ENG-ECO-DATE`. The PRD 1.9.18 catalog names the first seven present codes where they exist; `NOVERSION`, `LEVELS`, `QTY`, `LINE`, `ROUTE-QTY`, `ROUTE-CYCLE`, `ECO-STATE`, `ECO-DRYRUN` and `ECO-DATE` are not in the catalog yet and must be reconciled in slice 2.

## Event types

The PRD names `dev.passage.engineering.bom.version.activated.v1`, which has four segments after the context and fails `platform/go/events.ValidType`. The entity is folded into one segment instead: `bomversion`, `routeversion` (as `releasedproduct` is in the product service).

## Duties (slice 2)

`engineering.read`, `engineering.bom.write`, `engineering.route.write`, `engineering.eco.approve`, per PRD 1.9.17.

## Dependencies

`platform` (events, money, ids), `shopspring/decimal` (exact quantities; float64 would break the golden), `pgregory.net/rapid` (property tests). All three are already used by `services/product`.
