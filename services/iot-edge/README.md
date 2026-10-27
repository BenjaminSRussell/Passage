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

Everything else is in [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

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
not wired). Catalog SF-0008 (sequence gap) and SF-0018/SF-0019 (certificate and
rollback alerts) belong to the shopfloor service and the edge enrollment slice.

## Dependencies

Platform module only (`httpx`, `ids`), plus `rapid` for tests. No new dependencies.
