# print

Passage print service (wave 32, PSG-32). One per site network. It renders ZPL label
templates, sends them to printers over raw TCP 9100, fails over between the devices of
a queue, and resolves document routing rules. Printers on site networks are reached only
from this pod; nothing connects inbound to the cluster.

## What it does

- **Printer registry** (`POST/GET /print/v1/printers`): device code, device type, dpi, host,
  port, queue and failover rank. Codes are unique per tenant, and a failover rank is unique
  within its queue.
- **Document routing** (`POST/GET /print/v1/routing-rules`): a document type goes to a queue
  by the most specific matching scope. Precedence is user, then zone, then warehouse, then
  the document-type default. Ties go to the lowest `sequence` (BR-32.12).
- **Print jobs** (`POST /print/v1/jobs`, `GET /print/v1/jobs/{id}`): give a `queueCode`, or a
  `documentType` (with optional `zone`/`warehouseId`) and the rules pick the queue. The job
  renders ZPL from a built-in template, is sent to the queue's ZPL II devices in failover order
  (`PRINT_RETRY` + 1 attempts each, two by default; PDF and PCL devices in the queue are
  skipped), and ends PRINTED or FAILED.
  The LP label's SSCC barcode is GS1-128: FNC1, AI 00, then the 18 digits. Completion writes `dev.passage.print.job.completed.v1` to the outbox
  in the same transaction.
- **Reprints**: `reprintOf` reuses the stored ZPL and records the source job.
- **Job log** (`GET /print/v1/jobs?status=&queueCode=&printerCode=&limit=`): newest first (§6.32.14).
- **Printer status** (`GET /print/v1/printers/status`): per printer, the state from its last job
  (IDLE, BUSY, READY, ERROR), the queue depth (QUEUED jobs on its queue) and the last error
  (§6.32.14). No device is probed.
- **Retention purge** (`POST /print/v1/jobs:purge`, WMS_PRINT_ADMIN): deletes the tenant's
  PRINTED and FAILED jobs older than `PRINT_JOB_RETENTION_DAYS`; QUEUED jobs are kept (§6.32.19).
- **Metrics** on the admin port (`ADMIN_ADDR`, default `:9090`, `GET /metrics`):
  `print_jobs_printed_total`, `print_jobs_failed_total`, `print_jobs_purged_total`.
- Multi-tenant: every table, the outbox included, carries `tenant_id` with ENABLE and FORCE
  row-level security (`0001`, `0002`); `0003` adds the partial index for queue depth.

The web screens (printers, printer status, routing rules, print job log, print label, job
detail with "print again") live in `apps/web/src/modules/mobile-flows`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_print
export MIGRATE_ON_START=true     # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080           # default
export ADMIN_ADDR=:9090          # default, serves /metrics
export PRINT_RETRY=1             # retries per printer before failover, 0-10 (default 1)
export PRINT_JOB_RETENTION_DAYS=30  # job log retention, 1-365 (default 30)
go run ./cmd/print
```

An invalid `PRINT_RETRY` or `PRINT_JOB_RETENTION_DAYS` stops the process at startup.
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
failover. The ZPL golden test compares the LP label with `testdata/golden/lp-label.zpl`. API
tests use an in-memory store. Store tests (`internal/store`) run against Postgres through
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
