# Runbook: region failover (FR-50.5)

Alert: `PassageFailoverInProgress`. Target: RTO <= 1 h, RPO <= 5 min, zero committed-transaction loss.
Example pair: eu-west (PRIMARY) to eu-central (PASSIVE), `deploy/multi-region/values/`.

## Preconditions
1. Two distinct platform.global.admin holder confirmations within 5 min (`failover.two_person_window_min`, OPS-1702).
2. Target region lag: CNPG lag and MM2 lag below `FAILOVER_MAX_LAG_S` (300 s). Above it, the command
   is rejected (OPS-1701) unless `--accept-data-loss` is given with a second approver.
3. Target is in the same residency as the source (PRD 9.50.20: failover never crosses a residency boundary).

## Running it
`passage-ops failover --to eu-central --dry-run` prints the plan and what each step would do without changing anything. The real
command records the failover (OPS-1701/1702 apply; a failover that accepts data loss waits for `passage-ops failover approve --request <id>`
from a second operator), runs the steps below and reports each one; if it stops, fix the cause and `passage-ops failover resume --event <id>`.
`GET /v1/ops/failovers/<id>/scorecard` gives the RTO and RPO. Without the `ops.failover.execute` duty, `passage-ops break-glass open` gives a
logged, time-boxed window that another operator reviews. See `services/ops/README.md`.

## When the old region is lost
If the old primary's region cannot be reached, step 1 cannot fence its cluster and `passage-ops failover` stops there. Start the
failover with `--accept-data-loss --fence-unreachable "<reason, ticket>"`: a lost region means the target may miss its last
transactions, so a second operator approves the request (OPS-1702) as for any data loss. The runner still tries the fence first;
only if it fails does it record the fence step as DONE and forced (the reason and the error are in the step, `forced: true`, and the
scorecard shows `fenceForced` and fails a clean game day). `resume --fence-unreachable` does the same for a failover that stopped at
the fence. The cluster of the lost region is **not** fenced by this: commit the region file with `region.role=FENCED` at once (step 1,
last bullet) so Argo CD applies the fence and the NetworkPolicy the moment the region returns, and do not bring its applications up
until it is re-seeded as PASSIVE.

## What the fence does not cover
The 503 fence is in `platform/go/httpx`, so it covers the HTTP API of every Go service built on it. It does not stop Kafka consumers
and background workers (outbox relays, schedulers, the finreport and fulfillment consumers), which keep running against whatever
database they can still reach, and it does not cover finreport's share-link download route, which does not go through the fence
middleware's router. In a fenced region the database fence (CNPG `fencedInstances` and the NetworkPolicy) is what stops them: scale
the consumers and workers to zero in step 7 of the old region, as for the application pods.

## Steps (`passage-ops failover --to eu-central`)
1. **Fence the old primary.** Record the fence time and the target's lag (this is the RPO).
   - Revoke writes: `ALTER ROLE passage_app NOLOGIN` and terminate its sessions, if the old region answers.
   - Scale application pods to zero. The gateway returns 503, `Retry-After: 30` (BR-50.2, OPS-1703).
   - Render the old region with `region.role=FENCED`: every CNPG instance is fenced
     (`cnpg.io/fencedInstances: ["*"]`) and the fence NetworkPolicy closes its Postgres pods. If the old
     region is unreachable, commit the FENCED file anyway so Argo CD applies it the moment the region
     returns; this is what stops a returning primary from accepting writes.
2. **Promote.** Render the target with `region.role=PRIMARY`: the chart drops `spec.replica` and CNPG
   promotes the replica cluster's designated primary, with quorum-synchronous standbys in-region.
3. **Switch MirrorMaker.** Swap `mm2.sourceAlias` and `mm2.targetAlias` in both region files. MM2 renders
   only next to its target, so it stops in the new primary and starts in the old region once that is
   re-seeded (it has no target until then). Before committing the region files, run
   `python3 deploy/multi-region/tools/check_region_pair.py`: it fails on two PRIMARY files or an MM2
   direction that differs between the files or does not start at the new PRIMARY (BR-50.1).
4. **Activate Temporal** in the target region (multi-cluster replication).
5. **Flip DNS** (weight 0 to 100 on the target, TTL 60 s).
6. **Verify the ledger hash chain** per tenant from the last shared journal (BR-50.3). A break is
   Sev-1: keep user access blocked (OPS-1704) and page the ledger owner. Check gap-free sequences (BR-50.4).
7. Scale application pods up in the target; emit `dev.passage.ops.failover.completed.v1` and record a
   `failover_event` row.

## Record
Timeline, measured RPO (lag at fence time) and RTO (fence to DNS verified), `ledger_hash_ok`, `gapfree_ok`.

## Rollback
Before step 2 completes, remove the fence (render the old region PRIMARY again), restore `passage_app`
LOGIN and restore DNS. After promotion, do not reverse; use [failback](failback.md).
