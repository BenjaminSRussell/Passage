# NOT-IMPLEMENTED: print (PSG-32, wave 32, slice 1)

Every PSG-32 deliverable or acceptance criterion that this service does not yet cover.
Status is either `later slice of PSG-32` (owned here, not built in slice 1) or `handoff`
(needs code outside the wave's owned paths).

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Item/GTIN label and GS1 carton shipping label templates (AI 01/21/10/17, AI 400/420) | §6.32.15, FR-32.7 | later slice of PSG-32 | Slice 1 ships the LP and location templates only (the `templateCode` enum). |
| PDF over IPP, PCL 6 device types, 300/600 dpi, media sizes | FR-32.13, §6.32.13 `label_default_dpi` | later slice of PSG-32 | Slice 1 models ZPL II over raw TCP 9100 only. |
| Browser printing of PDF documents when no device is routed (BR-32.12 fallback) | FR-32.13, §6.32.13 `browser_print_fallback` | handoff | Browser printing runs in the device or web client, not in this service. The routing rule that decides the fallback is built (see `POST /print/v1/routing-rules`). |
| Printer selection by flow step (step, user and location) | FR-32.7 | handoff | Flow steps belong to the mobile flow service, which is outside the owned paths. The job API accepts `documentType`, `zone` and `warehouseId`, so the caller can pass the step context. |
| 10-minute print dedupe window (BR-32.8, `print_job_dedupe_min`) | BR-32.8 | later slice of PSG-32 | Slice 1 relies on the kernel idempotency key (24 h). The per-tenant window is not implemented. |
| Per-site `print_retry_count` parameter (default 3) | §6.32.13, FR-32.7 | later slice of PSG-32 | The retry count is not yet read from configuration. |
| Printer status and queue depth inquiry (online, queue depth, last error) | §6.32.14 | later slice of PSG-32 | The job log (`GET /print/v1/jobs`) and its web page are built; a per-printer status read model is not. |
| Print queue retry and failover sweep every 15 s, and asynchronous dispatch with the SENT state | §6.32.16, §6.32.4 | later slice of PSG-32 | Dispatch runs inside `POST /print/v1/jobs`. A job whose process dies between insert and completion stays QUEUED; no sweep picks it up yet. |
| Printer edit, retire and move between queues; routing rule edit and delete | FR-32.13, §6.32.6 | later slice of PSG-32 | Only create and list exist, in the API and the web screens. |
| Template management screen and tenant templates (§6.32.6 "templates") | §6.32.6, FR-32.7 | later slice of PSG-32 | Templates are built in (`LP_LABEL`, `LOCATION_LABEL`); there is no template store or editor. |
| 30-day job log retention purge | §6.32.19 | later slice of PSG-32 | No purge job yet. |
| `ServiceMonitor` template | §6.32.10, charts convention | later slice of PSG-32 | `serviceMonitor.enabled` is in values but no template renders it. Metrics live on the admin port (9090), which the chart does not yet expose. |
| Mobile flow runtime calling `POST /print/v1/jobs` on step completion (AC-32.5) | AC-32.5 | handoff | The mobile flow runner is not in the owned paths. |
| Automation adapter (FR-32.12), RFID EPC ingest, voice adapter | FR-32.9, FR-32.12 | handoff | Owned by the wms automation and mobile services, not by this service. |
| Deploy: print service live (Argo CD application, image build, one pod per site network) | Ticket deploy line, §6.32.10 | handoff | Lives in `deploy/` and the CI pipeline, which the deployment-cicd owner controls. The chart lints and is ready for it. |
| Site network egress policy for TCP 9100 and IPP | §6.32.10, FR-32.13 | handoff | Network policy is owned by deployment-cicd and the site IT team. |
