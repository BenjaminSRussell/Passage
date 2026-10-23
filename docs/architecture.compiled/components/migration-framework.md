# Migration Framework (AX / SAP data migration)

## Purpose & Boundaries
Wave 10 framework + master-data packs (FR-1.5; NFR-S5: 50k customers, 20k vendors, 100k items, 10k BOMs in < 30 min) and wave 49 transactional packs, parallel-run diff harness, cutover runbooks, coexistence adapters (FR-9.4; NFR-D6 100% control totals). Runs on Temporal + `platform.job` rows; bookkeeping in schema `migration` of the `integration` database (OQ-08). Moves data, not X++/ABAP.

## Interfaces
`POST /v1/integration/migration-runs` (202; User:`migration.run`), `GET /migration-runs/{id}` (counts, rejects, reconciliation), `POST /migration-runs/{id}:rerun` (idempotent), `POST /v1/integration/parallel-run-diffs` (202). Load path: public batch API (`POST ...:batch`, <= 1,000 items, `atomic:false`) or NDJSON import jobs — validation, RLS, sequences, outbox events and audit all apply; journals through `ledger.Post`; NO direct SQL into targets.

## Data Ownership
EN-064 `migration` schema: run (source_system ax2012|d365fo|sap_ecc|sap_s4; mode rehearsal|cutover|delta), pack_run, `xref` (PK (tenant_id, source_system, source_entity, source_key); content_hash; merged_into_target_id), control_total (IL), reject (IL), mapping (YAML DSL stored; version recorded on run), legacy_ref. Staging in object storage `{tenant-id}/migration/{run-id}/{source-table}/part-n` with SHA-256 manifest.

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this

## Dependencies
internal: platform (jobs, sequences), every target context's batch API, ledger (`journalentry.post.migration` privilege, `entry_type='migration_opening'`), integration. external: AX (restored SQL Server copy / BYOD / DMF) and SAP (SLT/ODP / file export) connectors (unconfirmed, OQ-07), Vault creds, Iceberg `legacy_ax_*`/`legacy_sap_*` read-only tables.

## Gotchas & Constraints
- Pack order: MD-ORG, MD-PARTY, MD-PRODUCT, MD-ENG, MD-FIN, MD-PRICE (wave 10), then TX-GL-OPEN, TX-OPEN-AP, TX-OPEN-AR, TX-INV-OPEN, TX-OPEN-PO, TX-OPEN-SO, TX-WIP, TX-FA, TX-HR, TX-PROJ, TX-BANK, HIST-LAKE (wave 49).
- ONLY `TX-GL-OPEN` posts to the ledger; subledger packs load with `origin='migration'` and no `ledger.Post`. Idempotency-Key = run-agnostic hash(source_system|source_entity|source_key|content_hash); target ids (UUIDv7) generated once and stored in xref BEFORE the API call.
- Migrated numbers: `number_sequence.mode='external'`; live sequences init to max(legacy)+1 at cutover. Never reuse source keys as ids; amounts scale > 6 or >= 10^14 rejected not rounded.
- Tie-out before go-live (AP=AP control, AR, inventory value, NBV, WIP) blocks cutover on any non-zero difference. Rollback only before cutover; after go-live forward correction only.
- SAP ECC KNA1/LFA1 matched via wave-7 dedup; review queue must be empty before customer/vendor pack loads. Delta cursor AX ModifiedDateTime / SAP AEDAT/CPUDT.
- AX/SAP table mappings come from public data dictionaries and MUST be verified against coverage oracles (not provided).

## Source Sections
- Data 13 (3468-3652): 13.1 framework, EN-064, 13.2 rules, 13.3 packs, 13.4 mappings, 13.5 special rules, 13.6 totals, 13.7 volumes; OQ-07..09 (3667-3669); Backend 5.17 migration rows (948-951)
