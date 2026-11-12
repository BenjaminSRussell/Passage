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

## Runtime (25-3)

`internal/runtime` is the running connector: source readings to counter deltas to events with seq, through the buffer
to the shopfloor ingest with `Peek` and `Ack`, plus heartbeat, configuration pull and the `/metrics` endpoint. See
NOT-IMPLEMENTED.md for what is open (OPC-UA and MQTT adapters, persistent ring, Kafka).

## MFS automation adapter (wave 32, FR-32.12)

`internal/mfs` is the adapter for AS/RS, conveyor and sorter controllers (PLCs), run on the plant
edge node:

- `telegram.go`: Passage's telegram profile v1 (fixed-width ASCII frames `STX LLLL body ETX`; types
  MOVE, ARRV, DIVT, ERRR, ALIV, ACKN, NACK), the codec with validation (load units are SSCCs with a
  checked GS1 mod-10 digit), and a `Framer` that cuts a TCP stream into frames and resynchronises after
  noise or a corrupted frame.
- `link.go`: a reliable session over any `net.Conn`. `Send` numbers a telegram, waits for ACKN, resends
  with the same sequence, and reports a NACK or no answer. Received ARRV/DIVT/ERRR telegrams are
  handed to the handler once (a repeat is acknowledged again but not redelivered), a handler error
  is NACKed busy so the PLC resends, malformed telegrams are NACKed, sequence gaps are reported, and a
  peer silent for three keep-alive intervals ends `Run` with `ErrLinkDown`.
- `events.go`: PLC telegrams to `dev.passage.warehouse.automation.event.v1` events (arrived, diverted,
  fault) and a warehouse transport order to a MOVE telegram.

Tests run the link against a fake PLC over `net.Pipe`. `internal/mfs/sim.go` is a simulated PLC (it accepts a MOVE
and reports the arrival, a fault or a divert) used by the tests and the demo plant. `internal/runtime/automation.go`
dials the PLC (`EDGE_PLC_ADDR`), sends the warehouse's pending transport orders as MOVE telegrams, reports each as
SENT or REFUSED, and forwards the PLC's events to the warehouse ingest (`POST /v1/warehouse/automation/events`).
Each link session has a name that travels with its events: the PLC numbers telegrams from 1 again after every
reconnect, so the warehouse de-duplicates by station, session and sequence, not by sequence alone.
Mapping a vendor's telegram profile onto this one is open (NOT-IMPLEMENTED.md).

## Adapters, buffer and writers (#25)

- `EDGE_SOURCE=opcua`: subscribes to the nodes the applied configuration maps (a tag's address is its node id,
  `ns=2;s=Press1.Count`). `EDGE_OPCUA_ENDPOINT`, `EDGE_OPCUA_SECURITY_POLICY` and `_MODE` (None only when `PASSAGE_ENV` is dev,
  refused elsewhere; a secured channel needs `EDGE_OPCUA_CERT_FILE` and `_KEY_FILE`), `EDGE_OPCUA_USER` with
  `EDGE_OPCUA_PASSWORD_FILE` (refused over None in every environment), `EDGE_OPCUA_INTERVAL_MS`.
- `EDGE_SOURCE=mqtt`: subscribes to the topics the configuration maps. A message is a bare number or text, or
  `{"value": ..., "ts": "<RFC 3339>"}`. `EDGE_MQTT_BROKER` (host:port), `EDGE_MQTT_TLS` (required outside dev),
  `EDGE_MQTT_CA_FILE`, `EDGE_MQTT_USER`, `EDGE_MQTT_PASSWORD_FILE`, `EDGE_MQTT_CLIENT_ID`.
- `EDGE_WRITER`: `simulator`, `opcua` (pcs-opcua recipes) and `mqtt` (pcs-mqtt recipes), comma separated.
- `EDGE_BUFFER_FILE`: persistent event buffer (an append-only journal); unsent events survive a restart.
- `EDGE_PLC_ADDR`, `EDGE_PLC_STATION`, `EDGE_WAREHOUSE_URL`, `EDGE_WAREHOUSE_TOKEN_FILE`: the PLC link above.

## Configuration

| Variable | Default | Rule |
|---|---|---|
| `EDGE_SITE` | none | required |
| `EDGE_ID` | none | required |
| `EDGE_ADMIN_ADDR` | `127.0.0.1:8081` | must bind loopback |
| `IOT_BUFFER_HOURS` | `24` | integer 1 to 168 |
| `PASSAGE_ENV` | `dev` | |
| `SERVICE_NAME` | `iot-edge` | |
| `EDGE_SHOPFLOOR_URL` | none | switches the runtime on; https (http only when `PASSAGE_ENV=dev`) |
| `EDGE_NODE_ID` | none | the shopfloor edge node UUID; required with the URL |
| `EDGE_SOURCE` | none | `simulator`, `opcua` or `mqtt`; required with the URL |
| `EDGE_TOKEN_FILE` | none | bearer token file for dev, until enrollment issues certificates |
| `EDGE_SEQ_STATE_FILE` | none (memory) | sequence state; use a persistent path |
| `IOT_BUFFER_CAPACITY` | `200000` | events in the in-memory ring, 1000 to 5000000 |
| `EDGE_SIM_MACHINES` | `2` | simulator machines `SIM-1..n` on tags `sim/<n>/count` and `sim/<n>/state`, 1 to 50 |

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

Platform module (`httpx`, `ids`), `github.com/gopcua/opcua` (MIT) for the OPC-UA adapter and its in-process test server, `rapid` for tests. The MQTT client and test broker are written in `internal/mqtt` (no dependency).
