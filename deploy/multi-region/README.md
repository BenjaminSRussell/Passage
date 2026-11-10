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
  - `backup.yaml`: the barman-cloud `ObjectStore` (bucket, credentials secret, `retentionPolicy`) and a daily
    `ScheduledBackup`; the `Cluster` archives WAL to the same store (`plugins`, `isWALArchiver`). PRIMARY and
    FENCED regions render it, PASSIVE does not. Restore one tenant to a point in time with
    [tools/tenant-restore](../../tools/tenant-restore/README.md).
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
- `gameday/collect.py` — the collector that writes those facts from a live region. The operator's spec names
  the ledger, row-count and sequence queries (their tables belong to other waves), the Kafka cluster the
  offsets are read from and the offsets command. Queries run through `psql` in read-only transactions with
  the libpq `PG*` environment. After failover, `--baseline baseline.json` reads the hash at each tenant's
  baseline sequence (`ledger.checkpoint_sql`), which `verify` needs. Tests: `gameday/test_collect.py`.
- `gameday/score.py` — game-day step 7 as a gate (AC-50.3): RTO (fence to DNS verified) and RPO (target lag at
  fence) from a timeline JSON against `failover.rtoTargetMinutes` / `failover.rpoTargetSeconds` of the chart
  (optionally merged with a region file), plus the `baseline.py verify --report` result. Tests: `gameday/test_score.py`.
- `gameday/local_drill.py` — the toolchain end to end on one machine: a real Postgres primary and streaming
  standby (`--mode sync|async`), a hash-chain writer, `collect.py`/`baseline.py` baseline, immediate stop of
  the primary as the fence, promotion, verify, `score.py`, and a count of acknowledged commits missing from the
  new primary (`--cut-replication` stalls the link first). It is not a region failover: no CNPG, Kafka, MM2,
  Temporal or DNS. Tests: `gameday/test_local_drill.py` (skipped without the PostgreSQL binaries).
- `chaos/` — Chaos Mesh `PodChaos` experiments for the PRD 9.50.9 chaos row: one Kafka broker of
  `deploy/kafka`'s cluster, and the primary of this chart's CNPG cluster, each with a 5 min recovery budget
  and a runbook link. Run them in the PRIMARY region during a campaign; `chaos/test_chaos.py` checks the
  selectors against `deploy/kafka/kafka.yaml` and `chart/values.yaml`. Chaos Mesh itself is not installed by
  this chart.
- `tools/expand_contract_lint.py` — OPS-1707. Fails a release whose new migrations drop or rename a column,
  table or view that this release's code or the previous release's (N-1, still running during the rolling
  deploy) references (BR-50.7). Tests: `tools/test_expand_contract_lint.py`.
- `tools/openapi_compat.py` — N / N-1 API compatibility (FR-50.7, AC-50.5). Compares the previous release's
  OpenAPI JSON with the new one and lists every change an N-1 client would notice: removed paths, operations,
  parameters, request media types, 2xx responses and response fields; new required parameters, bodies or
  request fields; request types or enums narrowed; response types or enums widened; response fields that
  stop being required. Additive changes pass, and renaming a path parameter is not a change. `allOf` members
  are merged before comparing; `oneOf`/`anyOf` are not compared and external `$ref` is an error. Input is
  JSON (convert YAML with `yq -o=json`). Exit 1 on a breaking change. Tests: `tools/test_openapi_compat.py`.
  Usage: `python3 deploy/multi-region/tools/openapi_compat.py --old n1.json --new n.json`.
- `tools/compat_evidence.py` — the release-level N/N-1 record: pairs `<service>.json` files of the N-1 and N
  directories, runs `openapi_compat` on each and writes one evidence file (schema
  `passage.ops.compat-evidence/v1`) with each service's breaking changes and warnings. A service published by
  N-1 and missing in N is breaking. Exit 1 when any service breaks. `docs/ops/tools/check_release_notes.py
  --compat-evidence` reads the file. Tests: `tools/test_compat_evidence.py`.
- `tools/check_region_pair.py` — the cross-file check `values.schema.json` cannot make (BR-50.1, PRD 9.50.13).
  It merges each region file over the chart defaults as Helm does and fails unless there are exactly two
  region files, exactly one is PRIMARY, both agree on the MirrorMaker 2 direction and it runs from the
  PRIMARY to the other region, the Kafka aliases name the pair, a PASSIVE file names the primary endpoint
  itself, and every region's failover, DNS TTL, two-person window, MM2 lag alert, burn-rate and license
  warning values sit in their ranges. Run it after every edit to `values/`, including failover step 3.
  Stdlib only (a strict parser for the YAML subset these files use). Exit 1 on a problem.
  Usage: `python3 deploy/multi-region/tools/check_region_pair.py`. Tests: `tools/test_check_region_pair.py`.
- Run tests: `python3 -m unittest discover -s deploy/multi-region/gameday`,
  `python3 -m unittest discover -s deploy/multi-region/tools` and
  `python3 -m unittest discover -s deploy/multi-region/chaos`.

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
- The CNPG barman-cloud plugin (and its cert-manager dependency) next to the operator, a bucket per region
  (`backup.destinationPath`, default `s3://passage-pg-backups-<region.id>/`) and the secret
  `backup.credentialsSecret` with `ACCESS_KEY_ID` and `ACCESS_SECRET_KEY`.
- Secrets copied from the peer region: `postgres.primary.replicationSecret`/`tlsSecret`,
  `kafka.remoteTrustSecret` and `kafka.remoteMm2Secret`.

## Targets
RTO <= 1 h, RPO <= 5 min, CNPG lag p95 < 30 s, MM2 lag alert at 100k messages, DNS TTL 60 s.
Cross-region synchronous ledger replication (`postgres.crossRegionSyncLedger`) is published to `passage-ops`
but not rendered: CNPG replica clusters are asynchronous.

## Not here
DNS provider modules, the `passage-ops` control plane, Temporal multi-cluster and object-store replication.
See `docs/ops/NOT-IMPLEMENTED.md`.
