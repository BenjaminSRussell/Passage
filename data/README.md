# data (wave 46 lakehouse and semantic layer)

Configuration and models for the Passage data platform. Owned by wave 46
(PSG-46). The Go service that enforces metric definitions and reconciliation is
in `../services/analytics`.

```
cdc/connector.template.json   Debezium Postgres connector, one per domain DB (FR-46.1)
dbt/                          dbt project: core.* current state, mart.* marts (FR-46.5)
cube/                         Cube data model and queryRewrite scope mirror (FR-46.6, FR-46.7)
```

## How the pieces fit

1. Debezium reads each domain database with `pgoutput`. Passage tables live
   in `public`, so Debezium names topics `cdc.<domain>.public.<table>`; the
   `route` transform rewrites that to `cdc.<domain>.<table>` (FR-46.1). Every
   Passage primary key starts with `tenant_id`, so the message key is
   `(tenant_id, primary key)`. Placeholders: `DOMAIN`, `DOMAIN_DB_HOST`,
   `CDC_DB_USER`, `TABLE_INCLUDE_LIST` (for the ledger:
   `public.journal_line,public.journal_entry,public.main_account,public.ledger`;
   never `public.outbox`, which the outbox connector relays) and
   `SCHEMA_REGISTRY_URL`. The password comes from a properties file mounted
   from Vault through the Kafka Connect file config provider. The heartbeat
   query needs an `analytics_heartbeat` table in each domain database, which
   the Atlas migration generator adds.
2. The Iceberg sink writes `raw.<domain>.<table>` (FR-46.2) and renames the
   Debezium metadata fields (`__op`, `__lsn`, `__source_ts_ms`) to `_op`,
   `_source_lsn`, `_source_ts`, adding `_ingested_at` and `late`. It is not in
   this tree; see `services/analytics/NOT-IMPLEMENTED.md`.
3. dbt builds the `core` current-state tables for the ledger (`journal_line`,
   `journal_entry`, `main_account`, `ledger`) with `macros/latest_state.sql`:
   per key, the change with the highest `_source_lsn` across all raw rows wins,
   so redelivered or late changes cannot overwrite newer ones (FR-46.4), and
   deletes are removed from the current table (BR-46.2). Incremental runs
   recompute keys that changed in the last 3 days of ingestion.
4. `mart.fin_journal_line` joins line, entry, ledger and account into the only
   monetary source: signed amount (debit positive) in the ledger currency,
   the entry's posting date and period, and the account category. COGS
   accounts are named per tenant in `seeds/account_category_map.csv`, because
   the ledger has no COGS account type.
5. dbt tests: unique and not-null keys, accepted values, the entry-to-line
   relationship, every journal entry nets to zero in the mart, and raw live
   keys equal core rows.
6. Cube reads `mart.fin_journal_line` through `cube/model`. `cube/cube.js`
   adds the legal-entity filter from the caller's security context to every
   cube a query touches and rejects unscoped queries and unmapped cubes.

## Run and test

```
node --test cube/                      # queryRewrite row-level security tests
cd dbt && dbt deps && dbt build        # needs a profile named passage_trino
```

`services/analytics/internal/domain/cube_contract_test.go` (run by `go test`)
checks that `cube.js` `SCOPE_COLUMNS` matches the Go scope mirror and that the
Cube measures and dimensions match the governed metric catalog.

dbt profiles are environment config and are not stored in this repository.

## Not in this tree

Iceberg sink, schema-registry compatibility rules, Trino and ClickHouse
deployment, OpenMetadata, SCD2 history for master data, and the remaining dbt
marts (sales, procurement, inventory, production, projects, HR). See
`services/analytics/NOT-IMPLEMENTED.md`.
