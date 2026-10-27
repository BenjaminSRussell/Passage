# NOT-IMPLEMENTED — iot-edge (PSG-25 / Wave 25) slice 1

Gaps in the edge connector against PRD §4.25 (FR-25.9, FR-25.19, BR-25.14, BR-25.15) and
the PSG-25 deliverable D9. The shopfloor service's gaps are in
[../shopfloor/NOT-IMPLEMENTED.md](../shopfloor/NOT-IMPLEMENTED.md).

| Item | Status | Reason |
|---|---|---|
| OPC-UA subscriptions (`gopcua`), security modes None/Sign/SignAndEncrypt, Basic256Sha256 and Aes128_Sha256_RsaOaep | later slice of PSG-25 | Protocol adapters are the next slice. The domain layer is protocol-agnostic so the adapter can feed `Sequencer` and `CounterDelta` directly. |
| MQTT subscription (3.1.1/5.0, TLS, QoS 1, per-edge credentials) | later slice of PSG-25 | Same as OPC-UA. |
| Tag mapping and normalization from raw node or topic values to `MachineEvent` | later slice of PSG-25 | Needs the configuration document from the shopfloor service (below). |
| Persistent SQLite ring (24 h, survives restart and reboot) | later slice of PSG-25 | Slice 1 has the in-memory `domain.Buffer` with the same rules. The SQLite store is an infrastructure adapter, added once the store choice is approved for the edge image. |
| Kafka idempotent producer, topic `shopfloor.machine-events`, ingest through the shopfloor gateway over HTTPS/2 or Kafka TLS | later slice of PSG-25 | Needs the Kafka client decision (`@confluentinc/kafka-javascript` is the TS choice; the Go client is not yet approved) and the ingest gateway. |
| Heartbeat every 30 s with buffer fill and per-machine status | later slice of PSG-25 | Needs the heartbeat endpoint on the shopfloor service and the mTLS client. |
| Token enrollment and mTLS client certificate (cert-manager, 90-day, renew at 30 days) | later slice of PSG-25 | Needs the platform certificate issuer and the enrollment endpoint. BR-25.14 renewal alerting is also here. |
| Versioned configuration pulled from the shopfloor service | later slice of PSG-25 | Needs the `EdgeConfigVersion` API. The shopfloor service does not expose it yet. |
| Ringed rollout (canary → ring1 → all), A/B binary swap, automatic rollback after 5 min of failed heartbeat | later slice of PSG-25 | Needs heartbeat and the fleet API. The chart pins `ring` as a label only. |
| Allowlisted write-back for control recipe setpoints with per-write audit and readback (BR-25.15) | later slice of PSG-25 | Needs control recipes (D6). |
| Local admin page for diagnostics on localhost | partial | `/healthz` exists on the loopback admin address. The diagnostics page and a `/metrics` endpoint (dropped-event, seq-gap and buffer metrics) are not built. |
| Static single binary for Windows and Linux industrial PCs | later slice of PSG-25 | Build targets and signing are in the deployment pipeline (not in this owned path). |
| Signed OCI image (amd64/arm64) | handoff | Image build and cosign signing belong to the deployment-cicd pipeline (not in this owned path). Chart references `ghcr.io/benjaminsrussell/passage/iot-edge`. |
| Liveness and readiness probes in the chart | handoff | The admin endpoint is bound to loopback, so kubelet probes cannot reach it. Needs a probe design (for example an exec probe or a node-local sidecar), agreed with the deployment owner. |
| `Service` and `ServiceMonitor` in the chart | not applicable | The connector accepts no inbound traffic, so there is no Service and no ServiceMonitor. Metrics come with the metrics endpoint in a later slice. |
| Enrollment and mTLS secret mounts in the chart | later slice of PSG-25 | Added with enrollment. |
| Edge certificate expiry alert SF-0018 and rollback alert SF-0019 | later slice of PSG-25 | Raised by the shopfloor service and the rollout job, which do not exist yet. |
| Sequence gap replay from the buffer by seq range (runbook "seq gap") | later slice of PSG-25 | The gap is reported by `Sequencer.Accept`. Replay needs the SQLite store and the ingest path. |
| Process message readback deviation (BR-25.15) | later slice of PSG-25 | Needs write-back (above). |
