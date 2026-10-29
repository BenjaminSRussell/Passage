# Multi-region (active/passive) — PSG-50, wave 50

Passage runs one PRIMARY region and one PASSIVE region per residency pair; both regions of a pair sit
inside the same residency. Exactly one region is PRIMARY
(BR-50.1). The passive region holds a CNPG replica cluster streaming from the primary and the MirrorMaker 2
that mirrors every Passage topic into it. DNS fails over with a 60 s TTL. Active/active is out of scope.

## Layout
- `chart/` — Helm chart `passage-multiregion`, one release per region:
  - `cnpg-cluster.yaml`: CNPG `Cluster` plus a transaction-mode PgBouncer `Pooler` (FR-50.2).
    PRIMARY writes with `synchronous: any 1, dataDurability: required` in-region; PASSIVE is a replica
    cluster (`replica.enabled`, async, `verify-full` TLS); FENCED keeps its spec with every instance
    fenced (`cnpg.io/fencedInstances: ["*"]`), so it takes no writes.
  - `mirrormaker2.yaml`: the `passage-mm2` SCRAM `KafkaUser` (every region) and, in the target region only,
    `KafkaMirrorMaker2` with offset translation, group offset sync, identity topic names, JMX metrics and a
    `PodMonitor`. Resources go to `kafka.namespace`, where the Strimzi cluster and its CA secret live.
  - `fence.yaml`: in a fenced region, a NetworkPolicy closing the Postgres pods to everything but their own
    cluster and the CNPG operator.
  - `region-config.yaml`: `REGION_*`, `FAILOVER_MAX_LAG_S`, `DNS_*`, `MM2_*`, `CNPG_SYNC_QUORUM` for `passage-ops`.
  - `prometheusrule.yaml`: availability burn-rate recording rules, then replication, MM2, failover, SLO burn
    (multi-window, BR-50.6) and license alerts; each alert carries `runbook_url`.
  - `values.schema.json`: role and provider enums and the PRD 9.50.13 ranges (`helm lint` enforces them).
- `values/eu-west.yaml` (PRIMARY) and `values/eu-central.yaml` (PASSIVE): the reference pair. Both are EU
  regions because failover never crosses a residency boundary (PRD 9.50.20); other residencies get their
  own pair from the same chart.
- `test_chart.py`: render tests per role (`python3 -m unittest discover -s deploy/multi-region`).

## Game day and release tools
- `gameday/baseline.py` — fx-gameday. `capture` writes a digest-sealed baseline manifest (ledger chain head per
  tenant, row counts, Kafka end offsets of the standby cluster, fiscal sequence ranges) before failure injection
  and prints the digest to record in the game-day ticket. `verify --expect-digest` checks the post-failover
  facts: the chain was extended and the hash at the baseline sequence matches (BR-50.3), rows and offsets did
  not go down and were read from the same cluster, sequences are gap-free and cover the whole baseline range
  (BR-50.4). Exit 0 only when every check passes. Tests: `gameday/test_baseline.py`.
- `tools/expand_contract_lint.py` — OPS-1707. Fails a release whose new migrations drop or rename a column,
  table or view that this release's code or the previous release's (N-1, still running during the rolling
  deploy) references (BR-50.7). Tests: `tools/test_expand_contract_lint.py`.
- Run tests: `python3 -m unittest discover -s deploy/multi-region/gameday` and
  `python3 -m unittest discover -s deploy/multi-region/tools`.

## Render and lint
```
helm lint deploy/multi-region/chart -f deploy/multi-region/values/eu-central.yaml
helm template passage-mr deploy/multi-region/chart -f deploy/multi-region/values/eu-central.yaml
python3 -m unittest discover -s deploy/multi-region
```

## Region roles
| Role | Postgres | MirrorMaker 2 | Fence |
|---|---|---|---|
| PRIMARY | read-write, quorum-sync standbys | only if it is `mm2.targetAlias` (after a failover) | no |
| PASSIVE | replica cluster of `postgres.primary.host` | yes when it is `mm2.targetAlias` | no |
| FENCED | all instances fenced | as above | NetworkPolicy on the Postgres pods |

## Failover in one line
`passage-ops failover --to eu-central` (control plane, handoff). It fences the old primary, promotes the replica
cluster by rendering the target as PRIMARY, swaps `mm2.sourceAlias`/`targetAlias`, activates Temporal, flips
DNS and verifies the ledger hash chain. Procedure: [docs/ops/runbooks/failover.md](../../docs/ops/runbooks/failover.md).

## Prerequisites outside this chart
- CNPG operator in `fence.operatorNamespace`, Strimzi watching `kafka.namespace`, Prometheus Operator CRDs.
- A cross-region reachable endpoint for `postgres.primary.host` and an external TLS listener on the peer's
  Kafka (`deploy/kafka` exposes only internal listeners today).
- Secrets copied from the peer region: `postgres.primary.replicationSecret`/`tlsSecret`,
  `kafka.remoteTrustSecret` and `kafka.remoteMm2Secret`.

## Targets
RTO <= 1 h, RPO <= 5 min, CNPG lag p95 < 30 s, MM2 lag alert at 100k messages, DNS TTL 60 s.
Cross-region synchronous ledger replication (`postgres.crossRegionSyncLedger`) is published to `passage-ops`
but not rendered: CNPG replica clusters are asynchronous.

## Not here
DNS provider modules, the `passage-ops` control plane, Temporal multi-cluster and object-store replication.
See `docs/ops/NOT-IMPLEMENTED.md`.
