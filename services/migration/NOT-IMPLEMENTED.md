# Not implemented in PSG-10 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1: 50k customers, 20k vendors, 100k items, 10k BOMs load in under 30 minutes | partial | the engine is built and a scaled-down 1,000/400/2,000/200 AX-shaped set reconciles 100% in `pipeline_test.go`; the timed full-size run needs the staging store, connectors and production loader (slice 2) |
| AC4: demo manufacturer migrated from a synthetic AX extract | slice 2 of PSG-10 | needs connectors, the load API and target contexts' batch endpoints |
| Deliverable 1: extract (ODBC from AX SQL, SAP RFC/OData) and Postgres staging schema | slice 2 of PSG-10 | `Extractor` is an interface with an in-memory fake; AX/SAP extraction method is an open architecture question (OQ-07) |
| Deliverable 1: map, validate, load, reconcile | built | `internal/domain` mapping, pipeline and reconcile |
| Deliverable 2: AX 2012/D365 F&O connectors; deliverable 3: SAP ECC/S4 connectors | slice 2 of PSG-10 | `tools/connectors` not created; table mappings must be verified against coverage oracles that are not provided |
| Deliverable 4: reconciliation | partial | row counts, order-independent hash total, sampled field compare and the exception queue with re-run are built; control totals per pack and tie-outs need the target contexts |
| Deliverable 5: dry run, idempotent re-run, run ledger with lineage | built in memory | persistence in `migration.run`, `xref`, `reject` tables is slice 2 |
| Deliverable 6: entity copy, signed configuration packages, incremental jobs, data templates | slice 2 of PSG-10 | line budget; incremental jobs need Temporal (wave 4) behind an interface |
| Deliverable 7: coverage-closure mapping packs | slice 2 of PSG-10 | depends on connectors and verified table mappings |
| Postgres store, `migrations/0001_init.sql`, RLS, outbox | slice 2 of PSG-10 | line budget |
| HTTP API (`POST /v1/integration/migration-runs`, `GET /migration-runs/{id}`, `:rerun`, `parallel-run-diffs`), `api/openapi.yaml`, duty `migration.run` | slice 2 of PSG-10 | no public endpoint in this slice |
| `events/asyncapi.yaml` | slice 2 of PSG-10 | the architecture lists no events produced or consumed; none are emitted |
| `cmd/migration`, Dockerfile, Helm chart and connector sub-charts, `/healthz`, `/readyz`, `/metrics` | slice 2 of PSG-10 | no process in this slice |
| Web module `apps/web/src/modules/migration/` | slice 2 of PSG-10 | line budget |
| Argo CD Application, Kubernetes Secret `migration-db`, database `passage_integration`, CI wiring | handoff | `deploy/` and CI are outside wave 10 ownership |
