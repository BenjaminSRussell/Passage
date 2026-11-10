# NOT-IMPLEMENTED: mobile (PSG-32, wave 32)

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Real devices, Android shell, voice adapter, RFID/EPC ingest | FR-32.1, FR-32.9, FR-32.10 | human/infra | Hardware and vendor work. |
| Automation (AS/RS, conveyor) telegrams | FR-32.12 | handoff | Owned by the wms automation service and edge gateway. |
| Count, move, pack and cluster flow steps calling the warehouse | FR-32.4 | later slice | Only get-work, assign, confirm, exception and receive calls exist; those flows run as scan capture. |
| Reapply of a resolved conflict moving inventory | BR-32.4 | handoff | `offline.resolved.v1` carries the decision; the inventory consumer is not built. |
| Supervisor PIN override and its server check | FR-32.8, BR-32.7 | later slice | No PIN endpoint. |
| Scan error log (ignored scans), printer routing and label trigger | AC-32.7, AC-32.5 | later slice | Needs print service and device wiring. |
| OpenTelemetry spans, telemetry upload, rollup and purge jobs | FR-32.11 | later slice | Only `/metrics` counters exist. |
| Scheduled stale sweep per tenant | BR-32.6 | infra | `POST /sessions:sweep` works; a cron per tenant is not deployed. |
| SSE across pods | FR-32.2 | later slice | The hub is in-process; devices also poll the menu. |
| Gateway HTTPRoute, warehouse chart trusting the mobile public key in dev values | deploy | infra | Not wired in `deploy/`. |
| `services/mobile` in the CI lint matrix and image build | CI | infra | Lint cleanliness unverified; the Dockerfile builds locally only. |
| Four-eyes flow approval, flow diff and smoke scripts | BR-32.9 | later slice | Publish validates the graph only. |
