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

## Restore drill

```
export PASSAGE_RESTORE_SCRATCH_URL=...  PASSAGE_RESTORE_LIVE_URL=...
tenant-restore drill --tenant <uuid> --schema <schema> --target-time 2026-11-09T10:30:00Z --rto 1h --min-rows 1000 --record drill.json
```

Runs the tenant copy from the recovered scratch database as a dry run (nothing is written; every constraint is exercised) and
prints a JSON drill record: duration, tables checked, rows restored, what would be inserted or updated, live-only rows, and
`status` PASS or FAIL with the reasons. It fails when the restore errors, when fewer than `--min-rows` rows (default 1) were
restored, or when it ran longer than `--rto` (default 1 h). The command exits non-zero on FAIL and still prints and writes the
record, which is the evidence for go-live checklist item 8 and the input of `docs/ops/runbooks/backup-drill-failure.md`.

## Masked clone

```
export PASSAGE_CLONE_SOURCE_URL=...   # where the tenant is read
export PASSAGE_CLONE_TARGET_URL=...   # the non-production database, as the application role
tenant-restore clone --tenant <uuid> --schema <schema> --masks masks.json            # dry run
tenant-restore clone --tenant <uuid> --schema <schema> --masks masks.json --apply
```

The same copy as `copy`, with columns replaced on the way. `masks.json`:
`{"salt": "per-clone secret", "columns": {"party_vendor.name": "hash", "*.email": "email", "*.phone": "digits", "party_contact.note": "null", "party_vendor.id": "keep", "party_vendor.status": "keep"}}`.
Rules: `hash` (salted, same length, equal inputs stay equal), `email` (valid-looking address at example.invalid), `digits`
(digits replaced, shape kept), `redact`, `null` (any column type), `keep` (copied as is). A key is `table.column` or `*.column`.
The configuration is an allowlist: every text, json, jsonb, array or xml column of the tenant's tables needs a rule (`keep`
included), and a clone with a column left without one is refused before any row is read, so a column added later is never copied
unmasked by accident. A key column (primary key, `tenant_id`) can only be `keep`; a rule on a key column, wildcard or explicit,
is an error. The text rules mask text only: one that meets another type stops the clone instead of copying the value. The salt is
at least 8 characters. The source and target must be different databases: after connecting, the tool compares what each server
says it is (`pg_control_system()` system identifier and `current_database()`), not the URLs, and refuses a match.
Which columns are personal or commercial is the operator's decision; the tool only enforces that every column was decided.

Tests need Postgres: `PASSAGE_TEST_DATABASE_URL` and `PASSAGE_TEST_APP_DATABASE_URL` (live database, admin and
application role) and `PASSAGE_TEST_RESTORE_DATABASE_URL` (a second database standing in for the scratch one).
They skip when unset.
