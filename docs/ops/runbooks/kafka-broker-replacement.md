# Runbook: Kafka broker replacement (FR-50.3)

Platform: Strimzi cluster `passage` in namespace `kafka` (`deploy/kafka/kafka.yaml`), one node pool
`dual-role` with 3 nodes that are both brokers and KRaft controllers, `min.insync.replicas=2`, producers
use `acks=all`. FR-50.3 sizes production at 6 brokers; that change belongs to the `deploy/kafka` owner
(`docs/ops/NOT-IMPLEMENTED.md`). Pods are `passage-dual-role-<n>`.

Symptom sources: under-replicated partitions, produce errors (`NotEnoughReplicas`), MM2 lag
(`PassageMirrorMaker2LagHigh`), or a broker pod stuck in `CrashLoopBackOff` or with a failed disk.

Rule: never take a second node down while one is unhealthy. With 3 nodes and `min.insync.replicas=2`,
one loss keeps every partition writable; a second loss blocks produces to the affected partitions and
also costs the KRaft controller quorum (2 of 3), so no leader election happens until a node returns.
No event is lost while produces are blocked: the outbox rows stay in each service's Postgres and the
relay publishes them once the ISR recovers. Business writes continue; only event delivery waits.

## 1. Assess (before touching anything)
1. `kubectl get pods -n kafka -l strimzi.io/pool-name=dual-role` and
   `kubectl get kafka passage -n kafka -o jsonpath='{.status.conditions}'`: note the nodes that are not Ready.
2. List under-replicated partitions from a healthy node:
   `kubectl exec -n kafka passage-dual-role-0 -- bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --under-replicated-partitions`
   and partitions below the in-sync minimum with `--under-min-isr-partitions`.
3. If any partition is under min ISR, produce is blocked for it: go to section 3 at once.
4. If two or more nodes are unhealthy, stop: open a Sev-2, do not start reassignments, and escalate to the
   platform owner. Recover a node (restart a crashed pod, reattach a disk) before replacing anything.

## 2. Stop the bleeding
1. Do not pause application writes and do not scale the Debezium connectors to zero: they retry, and
   pausing them only grows the outbox backlog.
2. Do not restart healthy nodes. A rolling restart while partitions are under-replicated removes the
   last in-sync replica of some of them.

## 3. Replace the node
1. Delete the failed node's PVC only after confirming the disk is unrecoverable (`deleteClaim: false`
   keeps it otherwise). Then delete the pod; Strimzi recreates it with a new PVC and the same node ID,
   and it re-replicates its partitions from the leaders.
2. Wait for the pod to rejoin. `--describe --under-replicated-partitions` returns empty.
3. If the node comes back with a different ID (node pool scaled down and up), move its partitions with
   `kafka-reassign-partitions.sh`, using a plan generated from `--topics-to-move-json-file`. Throttle
   replication (`--throttle 50000000`, bytes/s) so consumers keep their latency, and run `--verify`
   when done, which also removes the throttle.

## 4. Verify
1. `kafka-topics.sh --describe` shows ISR size >= 2 for every partition of the `passage.*` topics, and
   `--under-min-isr-partitions` is empty.
2. Produce a test message with `kafka-console-producer.sh --producer-property acks=all` to a scratch
   topic created with replication factor 3; it succeeds. Delete the topic.
3. MM2 lag returns below `mm2.lagAlertMessages` (100,000 messages) within 15 minutes.
4. Record the incident timeline. A broker loss is chaos-test evidence (no failed postings, recovery
   under 5 min).

## Rollback
None needed for a pod restart. If a reassignment raises latency, cancel it with
`kafka-reassign-partitions.sh --cancel` and let the original replicas catch up.
