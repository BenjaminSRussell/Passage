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
  (two attempts each; PDF and PCL devices in the queue are skipped), and ends PRINTED or FAILED.
  The LP label's SSCC barcode is GS1-128: FNC1, AI 00, then the 18 digits. Completion writes `dev.passage.print.job.completed.v1` to the outbox
  in the same transaction.
- **Reprints**: `reprintOf` reuses the stored ZPL and records the source job.
- **Job log** (`GET /print/v1/jobs?status=&queueCode=&printerCode=&limit=`): newest first (§6.32.14).
- Multi-tenant: every table, the outbox included, carries `tenant_id` with ENABLE and FORCE
  row-level security (`0001`, `0002`).

The web screens (printers, routing rules, print job log, print label, job detail with "print
again") live in `apps/web/src/modules/mobile-flows`.

## Run

```sh
export DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_print
export MIGRATE_ON_START=true     # dev only; stage and prod run migrations as a PreSync Job
export HTTP_ADDR=:8080           # default
go run ./cmd/print
```

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
