# Wave 10 AC1 with the real loader

The AC1 volumes loaded by `target.Loader` over HTTP into running party, product and engineering services, read back through their detail routes as `service:migration-loader` (`*.import.read`) and reconciled by the pipeline, then re-run with no change. Produced by `scripts/real-load.sh` (`cmd/realload`).

**Result: PASS**

- Commit 5d9e2c74bb617702a1d18612271b3c1d4b90266d-dirty, host vm, started 2026-11-04T21:50:04Z
- Postgres 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1), services built from this tree on 4 CPUs, one process each, serving as passage_app (RLS on); party numbers from the numbering service; engineering with PASSAGE_ENV=dev (no audit sink, unused by BOM imports); PASSAGE_SERVICE_REPLAY_CACHE_SIZE=2000000
- Cross-reference: postgres migration.xref; loader concurrency 8
- Volumes: 50000 customers, 20000 vendors, 100000 items, 10000 BOMs (70000 parties under the roles)
- First loads with read-back reconciliation: 7m46.903s; whole run with the re-runs: 9m24.903s (budget 30m0s)

| entity | rows | load + reconcile | status | created | rejected | reconciliation | re-run unchanged | re-run |
|---|---|---|---|---|---|---|---|---|
| party | 70000 | 3m2.211s | succeeded | 70000 | 0 | OK | 70000 | 26.273s |
| customer | 50000 | 1m1.83s | succeeded | 50000 | 0 | OK | 50000 | 19.799s |
| vendor | 20000 | 22.48s | succeeded | 20000 | 0 | OK | 20000 | 7.502s |
| item | 100000 | 3m2.997s | succeeded | 100000 | 0 | OK | 100000 | 39.694s |
| bom | 10000 | 17.383s | succeeded | 10000 | 0 | OK | 10000 | 4.497s |
