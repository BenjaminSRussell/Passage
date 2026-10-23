# Not implemented in PSG-3 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: entity definition language | built | `internal/domain`; the published JSON Schema file `entity.v1.json` is not generated |
| Deliverable 2: Postgres migration, OpenAPI 3.1, TypeScript client | built | `internal/gen`, golden-tested |
| Deliverable 2: Go domain model, repository, handlers, JSON Schema | slice 2 of PSG-3 | line budget; handlers need the registry binary and a database |
| Deliverable 2: Atlas or Goose migrations | not built | neither tool is installed; output is plain SQL for `dbx.Migrate`; adopting Atlas is a wave-1 decision |
| Deliverable 3: extension mechanism | slice 2 of PSG-3 | needs the registry and a running environment; every table already carries `ext jsonb` |
| Deliverable 4: registry service with versioning | slice 2 of PSG-3 | `Diff` and `CompareVersions` exist as pure functions; storage and HTTP are not built |
| Deliverable 5: field attributes | built | parsed, validated and carried to OpenAPI (`x-passage-pii`); computed expressions, visibility and audit are not enforced at runtime |
| Deliverable 6: decision tables and rule sets | slice 2 of PSG-3 | line budget |
| Deliverable 7: usage metering and CSV export | slice 2 of PSG-3 | line budget; needs gateway metrics and identity data from PSG-2 |
| AC2: zero-downtime extension | slice 2 of PSG-3 | needs registry, apply path and a running environment |
| AC4: 1,000 rps load | slice 2 of PSG-3 | needs generated handlers and a 3-node dev cluster |
| AC6: Helm chart lint | slice 2 of PSG-3 | the chart arrives with the registry binary |
| Tenant, LegalEntity, User replacing hand-written code | not applicable yet | PSG-2 is not in the repository; definitions exist as reference fixtures |
| CI chart lint, image and golangci lines for metadata | handoff | `.github/workflows/` and `deploy/` are wave 1 owned |
| Fixtures at `metadata/entities/_reference` | relocated | outside wave 3 ownership; they live in `testdata/entities/_reference` |
