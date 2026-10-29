# Runbook: MirrorMaker 2 lag

Alert: `PassageMirrorMaker2LagHigh` (sum of `kafka_mm2_records_lag` > `mm2.lagAlertMessages`, default
100,000, for 10 min). MM2 runs in the target (passive) region, namespace `kafka`.

1. Check the MM2 connectors: `kubectl get kafkamirrormaker2 passage-mm2 -n kafka -o yaml`
   (status conditions). A failed connector restarts automatically; a repeated failure is a config or auth
   issue (the `passage-mm2` KafkaUser in either region, or a stale `kafka.remoteMm2Secret` copy).
2. Find the lagging topics: `topk(10, sum by (topic) (kafka_mm2_records_lag))`.
3. Check the target cluster brokers and disk. Under-replicated partitions mean `min.insync.replicas=2`
   may block produce; see [Kafka broker replacement](kafka-broker-replacement.md).
4. Check throughput against the 60k msgs/s peak and `kafka_mm2_replication_latency_ms`. Scale
   `mm2.replicas` only after the cause is known.
5. Offset translation: consumers reconnect at translated offsets after failover. Verify with the
   checkpoint topic before declaring recovery.
