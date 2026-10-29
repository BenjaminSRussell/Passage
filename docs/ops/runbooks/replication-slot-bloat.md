# Runbook: replication slot bloat

Alert: `PassageReplicationSlotBloat` (slot retains more than 1 GB of WAL for 15 min).

1. List slots and retained bytes: `SELECT slot_name, active, pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) FROM pg_replication_slots;`
2. An inactive slot with no consumer is the usual cause. Drop it only after confirming its consumer
   (Debezium connector or replica) is decommissioned.
3. An active slot that is lagging means the consumer is slow; go to the MM2 or CDC lag runbook.
4. Watch the disk on the primary; WAL retention can fill the volume and stop writes.
