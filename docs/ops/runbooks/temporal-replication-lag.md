# Runbook: Temporal replication lag (multi-cluster, FR-50.4)

Temporal runs multi-cluster replication between the primary and passive regions. Workflow histories
replicate asynchronously. During a failover, the passive cluster must hold the histories of in-flight
workflows (settlement sagas, intercompany sagas, game-day workflows, scheduled jobs).

Symptoms: replication task lag growing on the passive cluster, a namespace replication status other
than `REGISTERED` or a failover version mismatch, or a workflow that stops making progress after a
region change.

## 1. Check replication state
1. `tctl --namespace <ns> namespace describe`: confirm the active cluster and the replication clusters.
   There must be exactly one active cluster per namespace (the same single-primary rule as BR-50.1).
2. Check the replication task lag metric for the passive cluster. Target: under 30 s, the same bound as
   CNPG (FR-50.4). Above 60 s, the alert matches the Postgres replication alert threshold.
3. Check the history service pods and the Temporal persistence (Postgres) replication for the same
   window. Temporal persistence lags the same way the CNPG replica does.

## 2. Lag without a failover
1. Confirm the cross-region network path (same link as the Postgres replication).
2. Restart a stuck replication poller only after confirming the cluster is not in a failover. Restarts
   are safe for the poller; never restart the history shard owners in both regions.

## 3. Failover interaction
1. Do not promote Temporal before the Postgres promotion is complete. Temporal persistence points at the
   promoted database.
2. Failover step 4 ("activate Temporal") switches the namespace active cluster. After the switch, list
   workflows in `Running` state and compare the count with the one recorded at fence time (the
   game-day baseline manifest does not hold workflow counts, so record it in the failover record).
3. Workflows that were blocked on a task in the old region resume when a worker in the new region picks
   them up. Start workers in the new region before flipping DNS, so no task waits for a worker.

## 4. Verify
1. Namespace shows one active cluster and the expected replication clusters.
2. Running workflow count matches the one recorded at fence time, with no workflow stalled for more than its
   timeout window.
3. Sagas (settlement, intercompany) reach a terminal state or a documented pending state.

## Record
Replication lag at fence time (feeds RPO), workflow count before and after, and any saga that needed a
manual step.
