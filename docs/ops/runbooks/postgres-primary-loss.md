# Runbook: Postgres primary loss (FR-50.2, FR-50.5)

Symptoms: the `passage-pg-rw` service (CNPG cluster `passage-pg` in `passage-data`) has no endpoint,
`cnpg_collector_up` drops, or the application readiness probe (`/readyz`, `SELECT 1` under 500 ms) fails
for every service in the region.

The primary cluster runs 1 primary and 2 standbys (`postgres.instances: 3`) with
`synchronous_standby_names = ANY 1` (`postgres.synchronous`). A single
pod loss is handled by CloudNativePG automatically: a synchronous standby is promoted and no committed
transaction is lost. This runbook covers the case where the automatic promotion does not complete or
the whole region is affected.

## 1. Single pod loss (automatic)
1. `kubectl cnpg status passage-pg -n passage-data`. Confirm a new primary is elected and the `rw`
   service points to it.
2. Services reconnect through the pooler (`passage-pg-pooler-rw`). Expect a failed-request window of
   seconds. Check that the outbox backlog (unpublished outbox rows per service) drains within 5 minutes.
3. Do not restart the lost pod by hand while CNPG is recovering it. Wait for it to rejoin as a replica.

## 2. Primary lost and no automatic promotion (within the region)
1. Fence the region's writes before promoting anything, as in [failover](failover.md) step 1:
   `ALTER ROLE passage_app NOLOGIN` and terminate its sessions on any instance that still answers, then
   scale application pods to zero.
2. Pick the most advanced synchronous standby (`pg_stat_replication`, highest `replay_lsn`).
3. Promote it with `kubectl cnpg promote passage-pg <instance> -n passage-data`. Record the LSN.
4. `ALTER ROLE passage_app LOGIN` on the new primary and scale pods back up. Run the readiness check.
5. Rebuild the lost node as a replica from the new primary (CNPG does this when the pod is recreated).

## 3. Whole region lost
Go to [failover](failover.md). The RPO is the lag of the passive region at fence time (target <= 5 min,
gated by `FAILOVER_MAX_LAG_S` = 300 s). Asynchronous cross-region streaming is the source of that gap.
`cnpg.cross_region_sync.ledger` (`postgres.crossRegionSyncLedger`) would make the ledger RPO zero, but
the chart only publishes the flag to `passage-ops`: CNPG replica clusters replicate asynchronously, so
until that handoff is built every tenant has the asynchronous RPO.

## 4. Verify after any primary change
1. Ledger hash chain head per tenant matches the last known head or extends it (BR-50.3).
2. Fiscal sequence numbers are contiguous across the promotion point (BR-50.4). When a baseline was
   captured, `deploy/multi-region/gameday/baseline.py verify` checks both.
3. `pg_stat_statements` top-50 has no new query above 1 s.

## Record
LSN at promotion, time of loss, time of recovery, and any writes rejected during the window. Write these
into the incident record; they feed the RPO measurement in the game-day report.
