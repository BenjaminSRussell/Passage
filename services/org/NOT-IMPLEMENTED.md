# Not implemented in PSG-6 slice 6

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: LegalEntity | partial | `legal_entity` is persisted with create and list; update, deactivate, default-in-use rules and the tenant default legal entity are slice 2 |
| Deliverable 1: OperatingUnit, Site, Warehouse, Location | slice 2 of PSG-6 | line budget; no tables or endpoints |
| Deliverable 1: effective-dated hierarchies (AC1) | domain only | `domain.ParentAsOf` is tested against a 1,000-node, 5-purpose oracle; hierarchy tables, closure table, cycle, depth, size and overlap validation, draft/publish/supersede endpoints are slice 2 |
| Deliverable 2: hierarchy designer UI and legal-entity switch re-scoping list pages (AC2) | slice 2 of PSG-6 | line budget; `apps/web/src/modules/org/` is not created |
| Deliverable 3: address book, geocoding hook, address purposes; deliverable 4: fiscal calendars (AC4) | slice 2 of PSG-6 | line budget; geocoder interface with a null default ships with addresses; `internal/domain/fiscal.go` and `testdata/fiscal-patterns/` are next; period statuses per ledger belong to wave 14 |
| Deliverable 5: reference data | partial | UoM conversion is built (AC3) over a built-in conversion set in `domain.StandardConversions`; countries, currencies, regions (US, CA, IN, MX) and languages are seeded (tables `country`, `currency`, `region`, `language`, `reference_status`; loader `store.SeedReference`); the read API (`GET /v1/org/{countries,currencies,regions,languages}[/{code}]`) and tenant deactivation (`PATCH …/{code}`, table `reference_status`) are built; the `uom_conversion` table is a later slice of PSG-6 |
| Deliverable 6: teams, team types, memberships | slice 2 of PSG-6 | line budget; membership criteria need OPA/Rego, which is not present |
| Deliverable 6: legal-entity copy from template, currency rounding rules, organization name translations | slice 2 of PSG-6 | copy needs Temporal (wave 4) and will sit behind an interface with an in-package fake; the rest is line budget |
| Events `hierarchyversion.activated`, `address.changed`, `fiscalcalendar.generated` | slice 3 of PSG-6 | owning aggregates (hierarchies, addresses, fiscal calendars) are not built; the kernel type regex allows three segments, so `org.hierarchy.version.activated` collapses to `org.hierarchyversion.activated` |
| Events `legalentity.updated`, `operatingunit.created`, `site.created`, `warehouse.created`, `location.created`, `currencyrounding.changed` | declared in `events/asyncapi.yaml`, not yet emitted | the aggregates and routes that emit them are later slices of PSG-602 |
| Regions for countries other than US, CA, IN, MX | not built | delivered through localization packs (§1.6.20 'others via pack') |
| PSG-602 item 3: persisted units of measure and conversions | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 4: currency rounding rules | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 5: legal-entity lifecycle (PATCH, name history, filters, limit) | later slice of PSG-602 | `time_zone` and `timeZone` are already on create because the created event carries it; the next slice must not re-add the column |
| PSG-602 item 6: operating units | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 7: sites, warehouses, locations | later slice of PSG-602 | split from the outbox slice for the line budget |
| PSG-602 item 8: OpenAPI route-parity and AsyncAPI parse contract tests, per-aggregate metrics, chart appVersion bump | later slice of PSG-602 | this slice checks asyncapi declarations by substring because go.mod has no YAML parser |
| Persisted units of measure and conversions, convert over the persisted set, currency-rounding persistence and `GET /v1/org/currencies/{code}/round`, OpenAPI route-parity contract test | later slice of PSG-6 | `domain.Round`, `ValidateUnit` and `ParseMethod` (PSG-603) are not yet reachable from a route |
| Outbox relay (Debezium connector for `outbox`) | not built | Kafka and Debezium are wave 4; `outbox` and `idempotency_keys` tables exist with RLS, only `outbox` is written |
| Jobs ORG-J1 to ORG-J6, Debezium connector; demo seed `ORG_SEED_PACKAGE=demo-manufacturer` | not built | Temporal and Kafka are wave 4; the seed needs sites and warehouses |
| Argo CD Application, ingress, image registry entry, Kubernetes Secret `org-db`, database `passage_org` and `passage_app` role | handoff | `deploy/` and CI are outside wave 6 ownership |
| handoff delivered: services/fx/NOT-IMPLEMENTED.md row "ISO 4217 reference data" | done in PSG-606 | GET /v1/org/currencies and /v1/org/currencies/{code} serve ISO 4217 code, numeric code, name and minor units; switching services/fx off money.MinorUnits is wave 12's own work |
| OPA duty entries for /v1/org/** (org.read, org.legal-entity.write, org.reference.write) in policies/authz/data.json | handoff | policies/ is outside wave 6 ownership; the service enforces duties from forwarded headers today |
| Reference data screen (deactivate, add custom UoM, test conversion) in apps/web/src/modules/org/ | later slice of PSG-6 | needs this slice's API; the web module is not created |
