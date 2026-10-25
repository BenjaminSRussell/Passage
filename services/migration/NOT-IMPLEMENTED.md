# Not implemented in PSG-10

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 1: map, validate, load, reconcile | built | `internal/domain` mapping, pipeline and reconcile; tested in `mapping_test.go`, `pipeline_test.go`, `reconcile_test.go` |
| Deliverable 1: extract from AX SQL (ODBC) and SAP (RFC/OData) | partial | `tools/connectors` reads decoded flat-file extracts through `RowSource`; no ODBC, RFC or OData driver is added because the extraction method is open architecture question OQ-07 and new dependencies need a plan decision |
| Deliverable 1: Postgres staging schema | not built | rows stream from the extractor through the pipeline; the architecture's staging in object storage with a SHA-256 manifest needs the object store, not yet provisioned |
| Deliverable 2: AX 2012 and D365 F&O connectors | partial | table specs for DirParty, CustTable, VendTable, InventTable, EcoResProduct, BOM, Route in `tools/connectors/ax.go`; column lists unverified against coverage oracles that are not provided |
| Deliverable 3: SAP ECC and S/4 connectors | partial | table specs for KNA1, LFA1, MARA, MAST, STKO, PLKO in `tools/connectors/sap.go`; unverified, flat-file only |
| Deliverable 4: reconciliation | partial | row counts, order-independent hash total, sampled field compare, exception queue and queue re-run are built; per-pack control totals and tie-outs need the target contexts |
| Deliverable 5: dry run, idempotent re-run, run ledger with lineage | built | `Pipeline.Run` with `DryRun`, xref in `migration.xref`, ledger in `migration.run_ledger`, `parentRunId` on re-runs; control_total, reject, pack_run, mapping and legacy_ref tables of EN-064 are not created (rejects live in the run report) |
| Deliverable 6: copy between legal entities, signed configuration packages, recurring incremental jobs, data templates | slice 3 of PSG-10 | line budget; recurring jobs need Temporal (wave 4) behind an interface; data templates can reuse `tools/importexport` |
| Deliverable 7: coverage-closure mapping packs | slice 3 of PSG-10 | needs verified table mappings |
| Real batch-API `TargetLoader` and the extractor wiring in `cmd/migration` | slice 3 of PSG-10 | needs target contexts' `:batch` endpoints (PSG-7, PSG-8, PSG-9); `api.NotConfigured` makes `POST /migration-runs` answer 501 until then |
| AC1: 50k customers, 20k vendors, 100k items, 10k BOMs in under 30 minutes | partial | `TestScale_FullSizeAXDatasetLoadsReconcilesAndRerunsWithoutChanges` proves the in-memory pipeline reconciles 100% in seconds; xref lookups and writes are batched (1000 per statement) and `TestPostgres_FullSizeLoadMeetsTimeBudgetAndRerunsUnchanged` measures 180k rows through the Postgres xref path when `PASSAGE_TEST_DATABASE_URL` is set; the real loader is unmeasured |
| AC4: demo manufacturer migrated from a synthetic AX extract | partial | the synthetic AX dataset loads and reconciles through the pipeline against `MemLoader`; live target loading waits on the real loader |
| Asynchronous execution of runs | wave 4 | runs execute inside the request handler on a context detached from the client (35-minute deadline) so a disconnect or the 30-second router timeout cannot abandon a half-loaded run, and the 202 is returned after completion; Temporal and `platform.job` rows are not present |
| `POST /v1/integration/parallel-run-diffs` | wave 49 | route answers 501 `MIG-PARALLEL-RUN-DEFERRED` |
| Events | none | the architecture lists no events produced or consumed; `events/asyncapi.yaml` is empty on purpose |
| Tracing | none | no tracing helper exists in the kernel or any Go service |
| Web module `apps/web/src/modules/migration/` | slice 3 of PSG-10 | line budget |
| `tools/connectors` in `make test-go` and CI | handoff | the Makefile discovers modules under `platform/go` and `services` only; the root Makefile and CI are outside wave 10 ownership |
| Argo CD Application, Kubernetes Secret `migration-db`, database `passage_integration`, Vault references for source credentials | handoff | `deploy/` is outside wave 10 ownership |
