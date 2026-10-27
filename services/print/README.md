# print

Passage print service (wave 32, PSG-32). One per site network. It renders ZPL label
templates, sends them to printers (ZPL II and PCL 6 over raw TCP 9100, PDF over IPP), fails over between the devices of
a queue, and resolves document routing rules. Printers on site networks are reached only
from this pod; nothing connects inbound to the cluster.

## What it does

- **Printer registry** (`POST/GET /print/v1/printers`): device code, device type, dpi, media
  size, host, port, queue and failover rank. `ZPL_II` devices take label media (`2X1_IN`,
  `3X1_IN`, `4X2_IN`, `4X6_IN`, default `4X6_IN`); `PDF_IPP` and `PCL_6` take sheets (`A4`,
  `LETTER`, default `A4`). A database constraint (`0004`) enforces the pairing. Codes are unique per tenant, and a failover rank is unique
  within its queue. A registration without a port gets 9100 for `ZPL_II` and `PCL_6` and 631
  for `PDF_IPP`.
- **Document routing** (`POST/GET /print/v1/routing-rules`): a document type goes to a queue
  by the most specific matching scope. Precedence is user, then zone, then warehouse, then
  the document-type default. Ties go to the lowest `sequence` (BR-32.12).
- **Print jobs** (`POST /print/v1/jobs`, `GET /print/v1/jobs/{id}`): give a `queueCode`, or a
  `documentType` (with optional `zone`/`warehouseId`) and the rules pick the queue. A label job
  renders ZPL from a built-in template and goes to the queue's `ZPL_II` devices; a PDF job
  (`format: PDF`, the document base64 in `documentBase64`, at most 2 MiB, starting `%PDF-`)
  goes to the queue's `PDF_IPP` devices. Devices are tried in failover order (`PRINT_RETRY` + 1
  attempts each, two by default; devices of the other type are skipped, and a queue with none
  of the right type fails with MOB-0112), and the job ends PRINTED or FAILED. The PDF bytes are
  stored in `print_job.document` for reprints and are never returned or listed.
  The LP label's SSCC barcode is GS1-128: FNC1, AI 00, then the 18 digits. Completion writes `dev.passage.print.job.completed.v1` to the outbox
  in the same transaction.
- **Templates**: `LP_LABEL`, `LOCATION_LABEL`, `ITEM_LABEL` (gtin, optional lot, expiry
  `YYMMDD` and serial) and `GS1_CARTON_LABEL` (sscc and shipToName, optional poNumber and
  shipToPostal; 4 x 6 in). The two GS1 labels compose their element string from the fields
  (AI 01, 10, 17, 21 and AI 00, 400, 420; `internal/domain/gs1label.go`). Check digits, dates
  (`DD=00` is the month end, BR-32.10), lengths and GS1 set 82 are checked before render, and
  FNC1 (`>8`) follows every variable-length element that another element follows. An
  all-numeric string starts in Code 128 subset C, anything else in subset B.
- **Device transports** (`internal/transport`): `Router` picks the transport by device type.
  `ZPL_II` and `PCL_6` are written to raw TCP. `PDF_IPP` posts an RFC 8010 Print-Job
  (`application/pdf`) to `http://<host>:<port>/ipp/print` and fails on any IPP status above
  `0x00FF`. A PDF payload never goes to a raw TCP device and a non-PDF payload never goes to
  an IPP device (`ErrWrongDocumentFormat`). No job carries PCL yet, so `PCL_6` devices are
  never offered a job; see NOT-IMPLEMENTED.md.
- **Reprints**: `reprintOf` reuses the stored ZPL or PDF and records the source job.
- **Job log** (`GET /print/v1/jobs?status=&queueCode=&printerCode=&limit=`): newest first (§6.32.14).
- **Printer status** (`GET /print/v1/printers/status`): per printer, the state from its last job
  (IDLE, BUSY, READY, ERROR), the queue depth (QUEUED jobs on its queue) and the last error
  (§6.32.14). No device is probed.
- **Retention purge** (`POST /print/v1/jobs:purge`, WMS_PRINT_ADMIN): deletes the tenant's
  PRINTED and FAILED jobs older than `PRINT_JOB_RETENTION_DAYS`; QUEUED jobs are kept (§6.32.19).
  The service also runs it daily at 01:00 UTC for each tenant in `PRINT_PURGE_TENANT_IDS`
  (`internal/jobs`); a failing tenant is logged and retried the next day without stopping the
  others. Both paths add to `print_jobs_purged_total`.
- **Metrics** on the admin port (`ADMIN_ADDR`, default `:9090`, `GET /metrics`):
  `print_jobs_printed_total`, `print_jobs_failed_total`, `print_jobs_purged_total`.
- Multi-tenant: every table, the outbox included, carries `tenant_id` with ENABLE and FORCE
  row-level security (`0001`, `0002`); `0003` adds the partial index for queue depth; `0004`
  adds media sizes and PDF job payloads.

The web screens (printers with media size, printer status, routing rules, print job log,
print a label or a PDF document, job detail with "print again") live in `apps/web/src/modules/mobile-flows`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_print
export MIGRATE_ON_START=true     # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080           # default
export ADMIN_ADDR=:9090          # default, serves /metrics
export PRINT_RETRY=1             # retries per printer before failover, 0-10 (default 1)
export PRINT_JOB_RETENTION_DAYS=30  # job log retention, 1-365 (default 30)
export PRINT_PURGE_TENANT_IDS=   # comma-separated tenant UUIDs for the daily purge (empty: off)
go run ./cmd/print
```

An invalid `PRINT_RETRY`, `PRINT_JOB_RETENTION_DAYS` or `PRINT_PURGE_TENANT_IDS` stops the
process at startup.
`PRINT_RETRY` counts retries after the first attempt, so the default of 1 gives the two
attempts per printer of FR-32.13; the PRD's "default 3" conflicts with that (see
NOT-IMPLEMENTED.md).

Health: `GET /healthz` (liveness), `GET /readyz` (database ping). Contract: `api/openapi.yaml`.
Events: `events/asyncapi.yaml`.

## Test

```sh
go vet ./... && go test ./...
```

Domain tests (`internal/domain`) are pure and table-driven, with rapid property tests for
failover and for the GS1 label round trip (compose, then parse back). The ZPL golden test
compares the LP label with `testdata/golden/lp-label.zpl`; the item label is compared byte
for byte in `gs1label_test.go`. Transport tests decode the IPP request independently against
an `httptest` server and send PCL 6 to a local TCP listener. `migrations/rls_test.go` is the
static RLS check: every table has `tenant_id`, ENABLE and FORCE row-level security and the
`passage.tenant_id` policy. API tests use an in-memory store. Store tests (`internal/store`) run against Postgres through
`platform/go/testkit` and are skipped without `PASSAGE_TEST_DATABASE_URL`; the RLS test also
needs `PASSAGE_TEST_APP_DATABASE_URL` (a role without BYPASSRLS).

## Error codes

| Code | HTTP | Meaning |
| --- | --- | --- |
| PRN-0001 | 400 | Request or template data invalid |
| PRN-0002 | 400 | Template code is not built in |
| PRN-0003 | 409 | Printer code already registered |
| PRN-0004 | 409 | Job already completed (terminal) |
| PRN-0005 | 404 | Print job not found |
| PRN-0006 | 409 | The queue already has a printer at this failover rank |
| MOB-0111 | 201 (job FAILED) | Printer unreachable after the attempts |
| MOB-0112 | 422 / 201 (job FAILED) | No routing rule matches the document, or the queue has no devices |
| idempotency_key_reuse | 422 | Same Idempotency-Key with a different request body |

Problem responses are RFC 9457 with `type` `https://passage.dev/problems/<code>`.

## Dependencies

Only the platform kernel (`platform/go`) and its transitive dependencies (chi, pgx/v5).
