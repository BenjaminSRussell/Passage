# NOT-IMPLEMENTED — iot-edge (PSG-25 / Wave 25) slices 1 to 3 (shopfloor side: slice 14)

Gaps in the edge connector against PRD §4.25 (FR-25.9, FR-25.19, BR-25.14, BR-25.15) and
the PSG-25 deliverable D9. The shopfloor service's gaps are in
[../shopfloor/NOT-IMPLEMENTED.md](../shopfloor/NOT-IMPLEMENTED.md).

Slice 3 adds the pure policy rules: the versioned configuration document, tag normalization,
the write-safety guard and readback check, the certificate renewal decision, and ring rollout
with rollback. The wire protocols, store, and transport are still open. Normalization refuses
non-finite readings and count deltas beyond 2^53, so every event it returns can be encoded
as JSON and published.

| Item | Status | Reason |
|---|---|---|
| OPC-UA subscriptions (`gopcua`), security modes None/Sign/SignAndEncrypt, Basic256Sha256 and Aes128_Sha256_RsaOaep | later slice of PSG-25 | Protocol adapters are the next slice. The domain layer is protocol-agnostic so the adapter can feed `Sequencer`, `CounterDelta` and `EdgeConfig.Normalize` directly. |
| MQTT subscription (3.1.1/5.0, TLS, QoS 1, per-edge credentials) | later slice of PSG-25 | Same as OPC-UA. |
| Persistent SQLite ring (24 h, survives restart and reboot) | later slice of PSG-25 | Slice 1 has the in-memory `domain.Buffer` with the same rules. The SQLite store is an infrastructure adapter, added once the store choice is approved for the edge image. The publisher must use `Peek` and `Ack` (Wave 25 proof): `Drain` removes payloads before the broker acknowledges them, so a broker restart would lose them (`TestPublisher_BrokerRestartLosesNothingWithPeekAck`). |
| Kafka idempotent producer, topic `shopfloor.machine-events`, ingest through the shopfloor gateway over HTTPS/2 or Kafka TLS | later slice of PSG-25 | Needs the Kafka client decision (`@confluentinc/kafka-javascript` is the TS choice; the Go client is not yet approved) and the ingest gateway. |
| Heartbeat every 30 s with buffer fill and per-machine status | built (25-3), identity open | `internal/runtime` calls `POST edge-nodes/{id}:heartbeat` at the interval the server answers with (default 30 s) and reports buffer fill, per-machine state and the applied configuration version. It authenticates with a bearer token file (dev); the mTLS client is 25-4. |
| Token enrollment and mTLS client certificate issuance (cert-manager, 90-day) | connector side built (25-4); the cluster issuer is open | Needs the platform certificate issuer and the enrollment endpoint. Enrollment must also set the shopfloor node's `connectorSubject` from the issued certificate: since slice 14 the configuration pull and heartbeat accept only the identity bound to the node, and that binding is set by hand until then. The renewal decision (BR-25.14, `DecideCert`) is built; the renewal call and the SF-0018 page are not. |
| Versioned configuration: the document, validation and apply rule are built (`EdgeConfig`, `NextApplied`); the runtime pulls a newer version when the heartbeat answer names one and applies it (25-3) | built (25-3) | The shopfloor `GET edge-nodes/{id}/config` endpoint exists since slice 14 (versioned, validated like `EdgeConfig`); the connector does not pull it yet. |
| Ringed rollout and automatic rollback: ring order, 24 h promotion and the 5 min rollback rule are built (`NextRing`, `CanPromote`, `RollbackDue`); A/B binary swap and the fleet API are not | later slice of PSG-25 | Needs heartbeat and the fleet API. The chart pins `ring` as a label only. |
| Allowlisted write-back for control recipe setpoints: guard and readback check are built (`CheckWrite`, `ReadbackMatches`); transport and per-write audit row are not | later slice of PSG-25 | Needs control recipes (D6) and the OPC-UA client. |
| Local admin page for diagnostics on localhost | partial | `/healthz` and, with the runtime on, `/metrics` (dropped-event, seq-gap, send-failure and buffer metrics) exist on the loopback admin address. The diagnostics page is not built. |
| Static single binary for Windows and Linux industrial PCs | later slice of PSG-25 | Build targets and signing are in the deployment pipeline (not in this owned path). |
| Signed OCI image (amd64/arm64) | handoff | Image build and cosign signing belong to the deployment-cicd pipeline (not in this owned path). Chart references `ghcr.io/benjaminsrussell/passage/iot-edge`. |
| Liveness and readiness probes in the chart | handoff | The admin endpoint is bound to loopback, so kubelet probes cannot reach it. Needs a probe design (for example an exec probe or a node-local sidecar), agreed with the deployment owner. |
| `Service` and `ServiceMonitor` in the chart | not applicable | The connector accepts no inbound traffic, so there is no Service and no ServiceMonitor. Metrics come with the metrics endpoint in a later slice. |
| Enrollment and mTLS secret mounts in the chart | later slice of PSG-25 | Added with enrollment. |
| Edge certificate expiry alert SF-0018 and rollback alert SF-0019 | later slice of PSG-25 | The decisions are built (`DecideCert` and `RollbackDue`). Raising the alerts belongs to the shopfloor service and the rollout job, which do not exist yet. |
| Sequence gap replay from the buffer by seq range (runbook "seq gap") | later slice of PSG-25 | The gap is reported by `Sequencer.Accept`. Replay needs the SQLite store and the ingest path. |
| Process message readback deviation (BR-25.15) | later slice of PSG-25 | The decision (`ReadbackMatches`) and category `deviation` are built. Raising the process message needs write-back (above) and the shopfloor process-message path. |
| `EdgeWriteAudit` table and the OPC-UA server trust list (FR-25.19 data model) | later slice of PSG-25 | `EdgeNode` and `EdgeConfigVersion` with the publish, pull and heartbeat endpoints exist in the shopfloor service since slice 14; the audit and trust list do not. `NextApplied` is the rule the applied-version column will enforce. |
| Edge fleet screens: write audit | later slice of PSG-25 | Nodes, configuration versions and rings have screens since slice 14 (`/shopfloor/edge-nodes`); the write audit needs `EdgeWriteAudit`. |
| Edge buffer hours setting (1–168 h per edge node) | later slice of PSG-25 | The ring is fixed at 24 h until the setting comes with the config document. |

## Requirements questions

| Item | Question |
|---|---|
| Ring promotion window | FR-25.19 names the rings (canary → ring 1 → all) but no promotion rule. `CanPromote` uses 24 h of healthy heartbeats as a placeholder; the requirements owner should set the window (and whether promotion is manual). |
| Setpoint limits (BR-25.15) | BR-25.15 checks the value against "the instruction's limits" (process instruction characteristic limits, FR-25.15). Until control recipes exist the limits come from the write allowlist entry in the config document. Confirm that the allowlist limits stay as an outer bound once instruction limits exist. |
| Readback tolerance | BR-25.15 does not say how close a readback must be. `ReadbackMatches` takes an absolute tolerance from the caller; the default per node type is open. |

## Runtime (25-3)

`internal/runtime` runs the connector: a `Source` produces readings, the runtime turns a count reading into the delta
since the previous one (`CounterDelta`, rollover included), gives each event the machine's next seq (reserved in blocks
of 1000 in `EDGE_SEQ_STATE_FILE`, so a restart resumes above everything issued and costs a gap, not dropped events),
normalizes it through the configuration, applies the stale check, and pushes the CloudEvent to the buffer. The publisher
sends batches of up to 500 to `POST /v1/shopfloor/machine-events` with `Peek` and `Ack`, backing off 1 s doubling to
30 s while the server is unreachable; a 4xx answer that is not auth or rate limiting drops the batch and counts it
(`ErrRejected`) so an invalid event cannot block the queue. Tests cover the 10,000 events in a minute run with a broker
outage and a lost answer (nothing lost, the resend de-duplicated by seq), counter rollover, restart seq resume, the
rejected batch, heartbeat plus configuration application and a simulator end to end. Enable it with
`EDGE_SHOPFLOOR_URL`, `EDGE_NODE_ID`, `EDGE_SOURCE=simulator` (chart `shopfloor.*`).

| Item | Status | Reason |
|---|---|---|
| OPC-UA and MQTT `Source` adapters | open | The runtime takes any `Source`; only the simulator exists. Real adapters need `gopcua` and an MQTT client approved for the edge image and a real machine or broker to test against (#213). |
| Sequence gap replay by seq range | open | The server's gaps are counted (`iotedge_seq_gaps_total`) and logged; replaying from the buffer needs the persistent ring. |
| Persistent ring | open | Events wait in memory (capacity `IOT_BUFFER_CAPACITY`, default 200,000); a connector restart loses what was unsent. Only the sequence state is on disk. |
| Kafka producer | open | Events go to the shopfloor ingest over HTTPS, the option FR-25.9 names; Kafka stays with the client decision above. |

## Enrollment and certificate (25-4)

`internal/runtime/certs.go`: on first start with `EDGE_CERT_DIR` and `EDGE_ENROLLMENT_TOKEN_FILE` the connector makes an ECDSA P-256
key on the host, sends only a certificate request with the one-time token, and installs the certificate it gets (key 0600, files
replaced atomically). It then talks mTLS: every handshake reads the current certificate, so a renewal needs no restart. `Beat`
applies `DecideCert`: it renews with a new key inside 30 days of expiry (the server keeps the same identity), remembers a failed
renewal so seven days of failures log SF-0018, reports the serial and expiry in each heartbeat, and an expired certificate stops
publishing while events keep buffering. Tests run against a TLS test server with a test CA (enroll, mTLS use, renewal, reload,
refused token, BR-25.14 actions, expired certificate). The shopfloor side and its gaps are in
[../shopfloor/NOT-IMPLEMENTED.md](../shopfloor/NOT-IMPLEMENTED.md). Open: a rollback of a bad certificate (the old pair is
replaced in place), enrollment on Windows service installs (no installer is built), and the gateway terminating the client CA.
