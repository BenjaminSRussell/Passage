# Not implemented in PSG-9 slice 1

Delivered: the pure domain kernel for deliverables 1, 2, 4 and 5, with the tests for AC1-AC4 under `internal/domain` and the event contract in `internal/events`.

| Deliverable or AC | Status | Reason |
|---|---|---|
| 1 BOM header/version/lines, effective dates, scrap, phantoms, by/co-products | domain only | `bom.go` covers resolution, scrap, phantoms, co/by-product exclusion; tables, store and API are slice 2. Formula (process) BOMs, `base_qty`/`yield_percent` scaling and the 5,000 line cap need the store: slice 2 |
| 2 Routing versions, operations, relations, times, requirements | domain only | `routing.go` covers lead time and link validation; requirement satisfaction (FR-9.5) needs resources: slice 2 |
| 3 Resources, groups, capabilities, work calendars with shifts | not started | schema and calendar generator are slice 2; `resource.changed` and `calendar.generated` payloads exist in `events.go` |
| 4 ECR/ECO workflow, where-used impact, release | domain only | state machine, release plan and `WhereUsed` exist; the Temporal ECO workflow lives in `services/workflow-ts`, which this wave does not own. Handoff: register the ECO workflow there using the `approve`/`reject` signals of the approval workflow. AC3 is proven with a fake `Approver` until then |
| 5 Explosion/implosion API, where-used, cost roll-up skeleton | domain only | `Explode`, `WhereUsed`, `RollupCost` exist; HTTP endpoints are slice 2; standard cost roll is wave 39 |
| 6 Engineering organizations, release structure, readiness, categories | not started | slice 2 (FR-9.14 to FR-9.18) |
| 7 BOM usage, alternative groups, discontinuation, serial/parameter effectivity, selection conditions | not started | slice 2 (FR-9.19 onward) |
| 8 Engineering documents, 21 CFR Part 11 signatures, eBOM import | not started | slice 2; depends on wave 8 document info records |
| AC1 DB-backed explosion timing (IX-PR-22) | slice 2 | the in-memory bound is asserted; the Postgres bound needs the store |
| AC3 Temporal test | substitute | domain test with a fake `Approver`; the vitest in `services/workflow-ts/test` is a handoff |
| AC6 vitest, tsc, OpenAPI/AsyncAPI, Helm lint | slice 2 | web module, specs and chart are not in this slice; `go vet` and `go test` pass |
| Migrations, RLS, outbox, idempotency, duties, metrics, health | slice 2 | arrive with store and API |
| Argo CD app, `passage_engineering` database, CNPG, `engineering-db` secret | handoff | `deploy/` is outside wave 9 ownership |
