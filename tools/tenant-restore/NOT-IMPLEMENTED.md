# tenant-restore: not implemented

| Item | Status | Notes |
|---|---|---|
| Recovery manifest for a scratch cluster at a timestamp | met | `TestScratchManifestRecoversToTheTargetTime`, `TestScratchManifestRejectsUnsafeOrNonsenseInput` |
| Copy one tenant's rows (RLS tenant column) from scratch into live, with a dry run | met | `TestRestoreTenantCopiesOnlyThatTenantsRows` (dry run leaves live unchanged and reports the same counts as apply; other tenants and tables without `tenant_id` untouched), `TestRestoreTenantIsIdempotent`, `TestRestoreTenantRollsBackEverythingOnFailure`, `TestRestoreTenantRejectsUnsafeInput`, `TestRunCopyDryRunPrintsCountsAndNeedsEnvironment` |
| Isolation does not rest on RLS alone | met | the copy refuses a superuser or BYPASSRLS live role, tables without RLS, and tables the live role owns without FORCE; `DO UPDATE` only touches rows of the same tenant and a primary key held by another tenant aborts the restore: `TestRestoreTenantRefusesRolesAndTablesThatRelyOnNothingButTheCopy`, `TestRestoreTenantAbortsOnCrossTenantPrimaryKeyCollision` |
| Self-referencing foreign keys, `GENERATED ALWAYS` identity columns, partitioned tables | met | rows are inserted parent-first, identity values are kept (`OVERRIDING SYSTEM VALUE`), partitions are copied through the parent: `TestRestoreTenantOrdersSelfReferencesAndKeepsIdentityValues`. Identity sequences are not advanced; the live sequence is already past the restored values |
| Scratch and live schemas that differ | met | only columns present in both are copied; a live-only column NOT NULL without default fails the insert: `TestRestoreTenantUsesTheColumnsBothDatabasesShareAndAbortsOnUniqueCollisions` |
| Unique-constraint collisions on non-primary-key columns | aborts | the whole restore rolls back with the database error (same test); resolve the live row first |
| Bulk load (COPY into a temp table, then INSERT ... SELECT) | later, on demand | rows are upserted one statement at a time; fine for a tenant's tables, slow for millions of rows |
| Restore drill against a real recovered cluster on production data | open | needs the cluster and bucket (infrastructure #213); the drill record is the go-live item 8 evidence |
| Deleting rows that exist only in live | later, on demand | rows created after the target time are kept and reported as `live-only`; removing them is a human decision |
| Tables with a foreign-key cycle, without a primary key, or whose triggers reject updates (hash-chained ledger tables) | not supported | the tool stops with an error before writing; restore those through the ledger's own procedure |
| Tables whose tenant column is not named `tenant_id` | not supported | `platform/go/dbx` fixes the name |
