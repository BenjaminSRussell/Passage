# tenant-restore

Point-in-time restore of one tenant (issue #48, P3a). The cluster is recovered to a timestamp into a scratch
database; only the chosen tenant's rows are copied back into the live database.

```
tenant-restore scratch-manifest --name passage-pg-restore --target-time 2026-11-09T10:30:00Z | kubectl apply -f -

export PASSAGE_RESTORE_SCRATCH_URL=postgres://...   # the recovered scratch database
export PASSAGE_RESTORE_LIVE_URL=postgres://...      # the live database, as the application role (RLS applies)
tenant-restore copy --tenant <uuid> --schema <schema>            # dry run
tenant-restore copy --tenant <uuid> --schema <schema> --apply    # commit
```

`scratch-manifest` prints a CNPG `Cluster` whose `bootstrap.recovery` reads the barman-cloud `ObjectStore` of
`deploy/multi-region` (`<source>-backup`) up to `--target-time`. The target must be in the past.

`copy` finds every table in `--schema` with a `tenant_id` column, orders them by foreign keys, and in one live
transaction (with `passage.tenant_id` set) upserts the tenant's scratch rows by primary key. A row that exists
in both and differs is overwritten with the restored values; a row missing from live is inserted. Rows that
exist only in live (created after the target time) are kept and reported as `live-only`. Other tenants' rows
and tables without `tenant_id` are never read or written. The dry run executes the same statements and rolls
back, so its counts and any constraint failure are exactly what `--apply` would hit. Any failure rolls back
every table.

The live role must be the application role: the tool refuses a superuser or BYPASSRLS role, a table without
row-level security and a table the role owns without FORCE. A primary key already held by another tenant, or a
unique collision on another column, aborts the restore. The scratch cluster has no superuser access; read it
as the owner role (secret `<scratch-name>-app`), the tool sets `passage.tenant_id` for its reads.

Tests need Postgres: `PASSAGE_TEST_DATABASE_URL` and `PASSAGE_TEST_APP_DATABASE_URL` (live database, admin and
application role) and `PASSAGE_TEST_RESTORE_DATABASE_URL` (a second database standing in for the scratch one).
They skip when unset.
