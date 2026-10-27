# iot-edge

Plant edge connector: OPC-UA and MQTT machine data into Kafka as CloudEvents
(`dev.passage.shopfloor.machine.event.v1`, BR-25.4). Wave reference: **PSG-25 / Wave 25**,
PRD section 4.25 (FR-25.9, FR-25.19).

## Slice 1 (this build): connector core

- `internal/domain` (pure, no I/O):
  - `CounterDelta`: 32-bit absolute counter deltas with rollover (BR-25.4).
  - `Sequencer`: per-machine sequence acceptance. Duplicates and seq ≤ last seen are
    dropped; events older than 7 days are dropped as stale; a seq jump reports the
    missing `Gap` (SF-0008 input).
  - `MachineEvent` validation and `Envelope`: the CloudEvent with id `<machineId>-<seq>`
    and source `urn:passage:iot-edge:<site>:<edgeId>`.
  - `Buffer`: store-and-forward ring with age and capacity limits and fill percentage
    (FR-25.9, 80% warning).
- `cmd/iot-edge`: 12-factor configuration validated at startup (exits 2 when invalid) and
  a loopback-only admin endpoint serving `/healthz` from the platform router. No metrics
  endpoint yet.
- `charts/iot-edge`: Helm chart for the plant node. It has no Service: the connector
  accepts no inbound traffic, by design.

## Slice 3: edge policy rules

Pure decision logic the connector and the fleet will run. No I/O yet.

- `EdgeConfig` (FR-25.19): the versioned document of machines, tag mappings and the write
  allowlist. `Validate` checks it (unique machines and tags, known kinds, write rules with
  finite limits and min <= max). `NextApplied` lets only a newer valid version replace the applied one.
- `Normalize` (FR-25.9, BR-25.4): maps a raw tag sample to a validated `MachineEvent`
  through its tag mapping. Count deltas must be whole numbers within 2^53, and count and
  measurement values must be finite (JSON cannot carry NaN or Inf).
- `CheckWrite` (BR-25.15): a setpoint is written only when its node is allowlisted, the
  value is inside the node's limits (NaN refused), and a control recipe is in execution.
  Refusals wrap `ErrWriteRefused` and name SF-0017. `ReadbackMatches` gives the deviation
  decision for the readback.
- `DecideCert` (BR-25.14): ok, renew (under 30 days), alert (renewal failing 7 days),
  expired (publishing stops, buffering continues). `PublishAllowed` is the publish gate.
- Rings and rollback (FR-25.19): `NextRing` (canary, ring1, all), `CanPromote` (24 h
  healthy, a placeholder the PRD does not set; see NOT-IMPLEMENTED.md) and `RollbackDue`
  (no heartbeat within 5 minutes of an upgrade).

## Configuration

| Variable | Default | Rule |
|---|---|---|
| `EDGE_SITE` | none | required |
| `EDGE_ID` | none | required |
| `EDGE_ADMIN_ADDR` | `127.0.0.1:8081` | must bind loopback |
| `IOT_BUFFER_HOURS` | `24` | integer 1 to 168 |
| `PASSAGE_ENV` | `dev` | |
| `SERVICE_NAME` | `iot-edge` | |

## Run and test

```
EDGE_SITE=plant-1 EDGE_ID=edge-7 go run ./cmd/iot-edge
go vet ./... && go test ./...
```

Domain tests are table-driven, with `pgregory.net/rapid` properties for counter rollover
(increments sum exactly across wraps), no loss or duplicates in sequence replay, and buffer
capacity and drain bounds. The connector needs no database, so `go test ./...` runs
everywhere.

## Error codes

Configuration errors are plain messages on stderr at startup (exit 2). Validation of
machine events returns `ErrInvalidEvent` (no catalog code yet; the dropped-event metric is
not wired). Configuration documents return `ErrInvalidConfig`. A refused write returns
`ErrWriteRefused` and carries SF-0017. Catalog SF-0008 (sequence gap) and SF-0018/SF-0019
(certificate and rollback alerts) belong to the shopfloor service and the edge enrollment
slice; the decisions behind SF-0018 and SF-0019 are in `DecideCert` and `RollbackDue`.

## Dependencies

Platform module only (`httpx`, `ids`), plus `rapid` for tests. No new dependencies.
