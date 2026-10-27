# NOT-IMPLEMENTED: print (PSG-32, wave 32, slice 2)

Every PSG-32 deliverable or acceptance criterion that this service does not yet cover.
Status is either `later slice of PSG-32` (owned here, not built yet) or `handoff`
(needs code outside the wave's owned paths).

| Item | Source | Status | Reason |
| --- | --- | --- | --- |
| Item/GTIN label and GS1 carton shipping label templates (AI 01/21/10/17, AI 400/420) | §6.32.15, FR-32.7 | later slice of PSG-32 | Slice 1 ships the LP and location templates only (the `templateCode` enum). |
| PDF over IPP, PCL 6 device types, 300/600 dpi, media sizes | FR-32.13, §6.32.13 `label_default_dpi` | later slice of PSG-32 | Slice 1 models ZPL II over raw TCP 9100 only. |
| Browser printing of PDF documents when no device is routed (BR-32.12 fallback) | FR-32.13, §6.32.13 `browser_print_fallback` | handoff | Browser printing runs in the device or web client, not in this service. The routing rule that decides the fallback is built (see `POST /print/v1/routing-rules`). |
| Printer selection by flow step (step, user and location) | FR-32.7 | handoff | Flow steps belong to the mobile flow service, which is outside the owned paths. The job API accepts `documentType`, `zone` and `warehouseId`, so the caller can pass the step context. |
| 10-minute print dedupe window (BR-32.8, `print_job_dedupe_min`) | BR-32.8 | later slice of PSG-32 | A repeated Idempotency-Key never re-renders (the key is unique per tenant and does not expire), and "print again" creates a job with `reprintOf`. The per-tenant window after which the same key may print again is not implemented. |
| `print_retry_count` default of 3 | §6.32.10 `PRINT_RETRY=3`, §6.32.13 | later slice of PSG-32 | `PRINT_RETRY` is read (retries per printer after the first attempt, 0 to 10; the chart sets it per site deployment). The PRD conflicts with itself: FR-32.13 and AC-32.10 fail over after 2 attempts, which is `PRINT_RETRY=1`, while §6.32.10 and §6.32.13 default to 3. The default stays at 1 until the PRD is corrected. |
| Live printer probe for the "online" column of printer status | §6.32.14 | later slice of PSG-32 | `GET /print/v1/printers/status` and the web page report state, queue depth and last error from the last job each printer handled. No device is probed, so a printer that has not printed since it went offline still shows its last state. |
| Print queue retry and failover sweep every 15 s, and asynchronous dispatch with the SENT state | §6.32.16, §6.32.4 | later slice of PSG-32 | Dispatch runs inside `POST /print/v1/jobs`. A job whose process dies between insert and completion stays QUEUED; no sweep picks it up yet. |
| Printer edit, retire and move between queues; routing rule edit and delete | FR-32.13, §6.32.6 | later slice of PSG-32 | Only create and list exist, in the API and the web screens. |
| Template management screen and tenant templates (§6.32.6 "templates") | §6.32.6, FR-32.7 | later slice of PSG-32 | Templates are built in (`LP_LABEL`, `LOCATION_LABEL`); there is no template store or editor. |
| Scheduled job log purge (daily) | §6.32.19, FR-32.13 | later slice of PSG-32 | The purge is built: `POST /print/v1/jobs:purge` (WMS_PRINT_ADMIN) removes the tenant's finished jobs older than `PRINT_JOB_RETENTION_DAYS` (default 30). Nothing calls it on a schedule yet; a scheduler must call it once per tenant, because the call is tenant-scoped under RLS. |
| Per-printer failure metric for the "print failures > 5/15 min per printer" alert, and the alert rule | §6.32.10 | later slice of PSG-32 | `/metrics` on the admin port (9090, scraped by the chart's `ServiceMonitor`) exports service totals `print_jobs_printed_total`, `print_jobs_failed_total` and `print_jobs_purged_total`, without a printer label. The PrometheusRule belongs to the observability owner (handoff). |
| Mobile flow runtime calling `POST /print/v1/jobs` on step completion (AC-32.5) | AC-32.5 | handoff | The mobile flow runner is not in the owned paths. |
| Automation adapter (FR-32.12), RFID EPC ingest, voice adapter | FR-32.9, FR-32.12 | handoff | Owned by the wms automation and mobile services, not by this service. |
| Deploy: print service live (Argo CD application, image build, one pod per site network) | Ticket deploy line, §6.32.10 | handoff | Lives in `deploy/` and the CI pipeline, which the deployment-cicd owner controls. The chart lints and is ready for it. |
| Site network egress policy for TCP 9100 and IPP | §6.32.10, FR-32.13 | handoff | Network policy is owned by deployment-cicd and the site IT team. |
