# Not implemented in PSG-6 slice 3

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: LegalEntity | partial | `legal_entity` is persisted with create and list; update, deactivate, default-in-use rules and the tenant default legal entity are slice 2 |
| Deliverable 1: OperatingUnit, Site, Warehouse, Location | slice 2 of PSG-6 | line budget; no tables or endpoints |
| Deliverable 1: effective-dated hierarchies (AC1) | domain only | `domain.ParentAsOf` is tested against a 1,000-node, 5-purpose oracle; hierarchy tables, closure table, cycle, depth, size and overlap validation, draft/publish/supersede endpoints are slice 2 |
| Deliverable 2: hierarchy designer UI and legal-entity switch re-scoping list pages (AC2) | slice 2 of PSG-6 | line budget; `apps/web/src/modules/org/` is not created |
| Deliverable 3: address book, geocoding hook, address purposes; deliverable 4: fiscal calendars (AC4) | slice 2 of PSG-6 | line budget; geocoder interface with a null default ships with addresses; `internal/domain/fiscal.go` and `testdata/fiscal-patterns/` are next; period statuses per ledger belong to wave 14 |
| Deliverable 5: reference data | partial | UoM conversion is built (AC3) over a built-in conversion set in `domain.StandardConversions`; countries, currencies, languages and the `uom_conversion` table are slice 2 |
| Deliverable 6: teams, team types, memberships | slice 2 of PSG-6 | line budget; membership criteria need OPA/Rego, which is not present |
| Deliverable 6: legal-entity copy from template, currency rounding rules, organization name translations | slice 2 of PSG-6 | copy needs Temporal (wave 4) and will sit behind an interface with an in-package fake; the rest is line budget |
| Events `hierarchyversion.activated`, `address.changed`, `fiscalcalendar.generated` | slice 3 of PSG-6 | owning aggregates (hierarchies, addresses, fiscal calendars) are not built; the kernel type regex allows three segments, so `org.hierarchy.version.activated` collapses to `org.hierarchyversion.activated` |
| Events `legalentity.updated`, `operatingunit.created`, `site.created`, `warehouse.created`, `location.created`, `currencyrounding.changed` | declared in `events/asyncapi.yaml`, not yet emitted | the aggregates and routes that emit them are later slices of PSG-602 |
| PSG-602 item 2: platform reference data (countries, regions, currencies, languages) | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 3: persisted units of measure and conversions | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 4: currency rounding rules | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 5: legal-entity lifecycle (PATCH, name history, filters, limit) | later slice of PSG-602 | `time_zone` and `timeZone` are already on create because the created event carries it; the next slice must not re-add the column |
| PSG-602 item 6: operating units | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 7: sites, warehouses, locations | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 8: OpenAPI route-parity and AsyncAPI parse contract tests, per-aggregate metrics, chart appVersion bump | later slice of PSG-602 | this slice checks asyncapi declarations by substring because go.mod has no YAML parser |
| PSG-603 item 5 delivered (`domain.Round`, `ValidateUnit`, `ParseMethod`); items 1-4, 6, 7: reference tables and API, persisted UoM, rounding persistence and API, contracts, metrics and docs | follow-up slices of PSG-603 | line budget; the seed CSVs alone exceed one slice, and the rounding domain functions are not yet reachable from a route |
| Outbox relay (Debezium connector for `outbox`) | not built | Kafka and Debezium are wave 4; `outbox` and `idempotency_keys` tables exist with RLS, only `outbox` is written |
| Jobs ORG-J1 to ORG-J6, Debezium connector; demo seed `ORG_SEED_PACKAGE=demo-manufacturer` | not built | Temporal and Kafka are wave 4; the seed needs sites and warehouses |
| Argo CD Application, ingress, image registry entry, Kubernetes Secret `org-db`, database `passage_org` and `passage_app` role | handoff | `deploy/` and CI are outside wave 6 ownership |
