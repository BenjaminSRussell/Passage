# Not implemented in PSG-6 slice 1

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
| Events and `events/asyncapi.yaml` | slice 2 of PSG-6 | slice 1 emits no event; no outbox DDL ships. The kernel event-type regex allows three segments after `dev.passage`, so PRD names such as `org.hierarchy.version.activated` must collapse to `org.hierarchyversion.activated` |
| Jobs ORG-J1 to ORG-J6, Debezium connector; demo seed `ORG_SEED_PACKAGE=demo-manufacturer` | not built | Temporal and Kafka are wave 4; the seed needs sites and warehouses |
| Argo CD Application, ingress, image registry entry, Kubernetes Secret `org-db`, database `passage_org` and `passage_app` role | handoff | `deploy/` and CI are outside wave 6 ownership |
