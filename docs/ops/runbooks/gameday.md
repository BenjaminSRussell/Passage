# Runbook: DR game day (FR-50.6)

Quarterly, first Tuesday 14:00 UTC (`gameday.cron`). Pass: RTO <= 1 h, RPO <= 5 min, zero committed loss.

1. Announce the window; freeze deploys.
2. Take baseline hashes: ledger chain head per tenant, row counts per domain, Kafka end offsets.
3. Inject failure: partition the primary region with a NetworkPolicy, then scale a node pool to zero.
4. On-call runs [failover](failover.md) with a timer.
5. Verify: chain head matches or exceeds baseline; gap-free sequences continuous; in-flight Temporal
   workflows resumed; reconciliation jobs green.
6. Run the 12-process smoke suite (`fx-smoke-12`) in the new primary.
7. Measure RTO and RPO. CDC lag must recover below 60 s within 15 min (AC-50.4).
8. Fail back in a planned window ([failback](failback.md)).
9. Post-game review; actions tracked to closure. Publish the game-day report (SOC 2 CC7.5 evidence).
