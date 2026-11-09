# tenant-restore: not implemented

| Item | Status | Notes |
|---|---|---|
| Recovery manifest for a scratch cluster at a timestamp | met | `TestScratchManifestRecoversToTheTargetTime`, `TestScratchManifestRejectsUnsafeOrNonsenseInput` |
| Copy one tenant's rows (RLS tenant column) from scratch into live, with a dry run | met | `TestRestoreTenantCopiesOnlyThatTenantsRows` (dry run leaves live unchanged and reports the same counts as apply; other tenants and tables without `tenant_id` untouched), `TestRestoreTenantIsIdempotent`, `TestRestoreTenantRollsBackEverythingOnFailure`, `TestRestoreTenantRejectsUnsafeInput`, `TestRunCopyDryRunPrintsCountsAndNeedsEnvironment` |
| Restore drill against a real recovered cluster on production data | open | needs the cluster and bucket (infrastructure #213); the drill record is the go-live item 8 evidence |
| Deleting rows that exist only in live | later, on demand | rows created after the target time are kept and reported as `live-only`; removing them is a human decision |
| Tables with a foreign-key cycle, without a primary key, or whose triggers reject updates (hash-chained ledger tables) | not supported | the tool stops with an error before writing; restore those through the ledger's own procedure |
| Tables whose tenant column is not named `tenant_id` | not supported | `platform/go/dbx` fixes the name |
