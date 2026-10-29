# Runbook: fail-back to the original primary

Alert: `PassageRegionFenced` (info). Runs in a planned window, never during an incident.

A fenced former primary cannot follow the new primary in place: its timeline diverged at promotion. It is
re-seeded as a fresh replica, then switched back like a failover.

1. **Keep the diverged data.** Snapshot the fenced region's Postgres volumes. Transactions committed there
   after the last replicated LSN (the RPO window) are reconciled from this snapshot, never discarded.
2. **Re-seed.** Delete the fenced `Cluster` and its PVCs, then render the region with `region.role=PASSIVE`.
   Its `postgres.primary.host` already names the peer, so CNPG `pg_basebackup`s from the current primary.
3. **Verify re-sync** (BR-50.1: FENCED returns to PASSIVE only after re-sync is verified):
   `cnpg_pg_replication_lag` below 30 s, row counts and ledger hash heads equal to the current primary.
   MirrorMaker 2 now runs in this region (it is `mm2.targetAlias`) and its lag is below
   `mm2.lagAlertMessages`.
4. **Switch back** in the window with the [failover](failover.md) steps in the other direction.
5. Confirm zero loss: row counts and ledger hash heads match the pre-fail-back baseline.
6. Record a `failover_event` with `initiated_by` the fail-back operator.
