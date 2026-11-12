# NOT-IMPLEMENTED: mobile (PSG-32, wave 32)

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Real devices, Android shell, voice adapter | FR-32.1, FR-32.9, FR-32.10 | human/infra | Hardware and vendor work. The server-side RFID ingest and the other EPC schemes are built in the warehouse service (see its NOT-IMPLEMENTED.md). |
| Automation (AS/RS, conveyor) telegrams | FR-32.12 | built, vendor profile open | The edge runtime dials a configured PLC, sends the warehouse's transport orders as MOVE telegrams and forwards the PLC's events to the warehouse ingest, which confirms the transport work (iot-edge `Automation`, warehouse `/v1/warehouse/automation/*`). Tested against a simulated PLC; a real vendor's telegram layout needs a PLC (#213). |
| Count, move, pack and cluster flow steps calling the warehouse | FR-32.4 | later slice | Only get-work, assign, confirm, exception and receive calls exist; those flows run as scan capture. |
| Reapply of a resolved conflict moving inventory | BR-32.4 | handoff | `offline.resolved.v1` carries the decision; the inventory consumer is not built. |
| Supervisor PIN override and its server check | FR-32.8, BR-32.7 | later slice | No PIN endpoint. |
| Scan error log (ignored scans), printer routing and label trigger | AC-32.7, AC-32.5 | later slice | Needs print service and device wiring. |
| OpenTelemetry spans, telemetry upload, rollup and purge jobs | FR-32.11 | later slice | Only `/metrics` counters exist. |
| Scheduled stale sweep per tenant | BR-32.6 | infra | `POST /sessions:sweep` works; a cron per tenant is not deployed. |
| SSE across pods | FR-32.2 | later slice | The hub is in-process; devices also poll the menu. |
| Gateway HTTPRoute, warehouse chart trusting the mobile public key in dev values | deploy | infra | Not wired in `deploy/`. |
| `services/mobile` image build | CI | infra | Lint runs in the CI matrix (`golangci-lint`); the Dockerfile builds locally only. |
| Four-eyes flow approval, flow diff and smoke scripts | BR-32.9 | later slice | Publish validates the graph only. |
| GS1 application identifiers beyond the BR-32.1 subset | BR-32.1 | later slice | The AI table is the BR-32.1 subset: 00, 01, 02, 10, 11 to 13, 15 to 17, 20, 21, 30, 37, 240, 241, 250, 251, 400, 410 to 414, 420, 91 to 99 and the 310x to 325x measure families. Any other AI is MOB-0103. |
| GS1 symbologies other than GS1-128, GS1 DataMatrix and GS1 QR | BR-32.1 | later slice | Only symbology ids `]C1`, `]d2` and `]Q3` are parsed as GS1. Any other id (for example `]E0` EAN-13, `]A0` Code 128, `]e0` GS1 DataBar, `]J1` GS1 composite) is read as a plain value with the id removed; GS1 DataBar and composite data are not parsed. |
| Per-tenant cap on event-stream connections and warehouse filtering of events | FR-32.2 | later slice | Every device of a tenant receives every `flow.published`; the number of open streams per tenant is not limited. |
