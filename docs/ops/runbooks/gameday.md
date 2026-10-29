# Runbook: DR game day (FR-50.6)

Quarterly, first Tuesday 14:00 UTC (`gameday.cron`). Pass: RTO <= 1 h, RPO <= 5 min, zero committed loss.

1. Announce the window; freeze deploys.
2. Take baseline hashes: ledger chain head per tenant, row counts per domain, Kafka end offsets of the
   mirrored topics on the standby cluster, fiscal sequence ranges. Collect them with
   `python3 deploy/multi-region/gameday/collect.py --spec spec.json --out facts.json` (read-only; the spec
   names the queries and the standby Kafka cluster), then run
   `python3 deploy/multi-region/gameday/baseline.py capture --input facts.json --out baseline.json`;
   record the printed digest in the game-day ticket.
3. Inject failure: partition the primary region with a NetworkPolicy, then scale a node pool to zero.
4. On-call runs [failover](failover.md) with a timer.
5. Verify: collect the same facts in the new primary with
   `collect.py --spec spec.json --baseline baseline.json --out post.json` (reads the chain hash at each
   baseline sequence) and run `baseline.py verify --baseline baseline.json
   --observed post.json --expect-digest <recorded digest> --report report.json` (chain extended, rows and
   offsets not reduced, sequences gap-free over the baseline range). Then check that in-flight Temporal
   workflows resumed and reconciliation jobs are green.
6. Run the 12-process smoke suite (`fx-smoke-12`) in the new primary:
   `SMOKE_TOKEN=... python3 load/tools/smoke12.py --base-url <new primary> --report smoke.json`.
7. Measure RTO and RPO. CDC lag must recover below 60 s within 15 min (AC-50.4).
8. Fail back in a planned window ([failback](failback.md)).
9. Post-game review; actions tracked to closure. Publish the game-day report (SOC 2 CC7.5 evidence).
