# audit

Append-only field-level audit log for Passage (wave 5). Changes arrive through `POST /v1/audit/changes`, which is the in-process stand-in for a wave-4 Debezium/Kafka consumer (`internal/cdc.Consumer`). Each row is hash-chained; updates and deletes are rejected by a trigger.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/audit`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL`. RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/audit/changes` | `audit.write` |
| `GET /v1/audit/records/{entity}/{recordId}` | `audit.read` |
| `POST /v1/audit/alert-rules` | `audit.write` |
| `GET /v1/audit/alerts` | `audit.read` |
| `POST /v1/audit/reads` | `audit.read-log.write` (service-only: `hr` holds it through its service grant) |
| `GET /v1/audit/reads` | `audit.read-log.view` |
| `GET /v1/audit/reads:export?format=csv\|ndjson` | `audit.read-log.export` |
| `GET /v1/audit/reads:verify` | `audit.read-log.export` |
| `GET /v1/audit/reads:break-glass?status=pending\|reviewed\|all` | `audit.read-log.review` |
| `POST /v1/audit/reads/{id}:review` | `audit.read-log.review` |
| `POST /v1/audit/texts/expand` | authenticated |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Event: `dev.passage.audit.change.recorded.v1`.

Wave 3 entity metadata is not imported; `entity` is an opaque name. Wave 4 Kafka is not imported; a future consumer calls `cdc.Consumer.Apply`.

## PHI read-access log (#606, HIPAA overlay)

HIPAA audit controls ask who looked at protected health information, not only who changed it. A service that returns a record with `phi` fields (see `services/metadata`) records the read with `POST /v1/audit/reads`: `entity`, `recordId`, `fields[]`, `purpose` (required, a reason code or free text up to 500 characters), and optionally `traceId` (default: the request id) and `occurredAt` (default: now, at most five minutes ahead). The reader is the verified caller of the request (the `act` of a service token), never the body: a body that sets `userId` is refused with AUD-0002. `sourceService` is the verified service that recorded the read. `platform/go/phiaudit` is the client and the fake for tests.

`phi_read` (migration `0003_phi_read.sql`) is per-tenant, row-level secured and append-only: a trigger rejects update, delete and truncate, and `passage_app` holds only `SELECT` and `INSERT`. Each row carries `prev_hash` and `hash` (SHA-256 over a JSON array of the previous hash and the row, so no value can shift into its neighbour); appends are serialised per tenant with an advisory lock so the chain cannot fork. An edited, removed or reordered row breaks the chain at that position.

`GET /v1/audit/reads` lists rows in chain order, filtered by `entity`, `recordId`, `userId`, `from` (inclusive) and `to` (exclusive), `limit` (1 to 500, default 100) and `cursor` (the `nextCursor` of the previous page). `GET /v1/audit/reads:export?format=csv|ndjson` streams the tenant's rows (same filters) for an auditor. Before streaming it verifies the whole chain from the genesis hash and reports the verdict in the headers `X-Passage-Read-Chain-Verified`, `-Entries`, `-Head` and, when broken, `-Broken-At`; `GET /v1/audit/reads:verify` returns the same verdict as JSON. `-Entries` and `-Head` describe the whole chain that was verified, not the filtered rows. The body's completeness is in the trailers `X-Passage-Read-Export-Rows` and `X-Passage-Read-Export-Complete: true`, sent only after the last row; if the export fails once rows are streaming, the connection is aborted, so a body without the completion trailer is incomplete. An auditor keeps the head hash from each export: a later chain that does not contain it was cut short (a chain alone cannot show that rows were removed from its end). CSV prefixes a cell that starts with `=`, `+`, `-`, `@`, tab or carriage return with an apostrophe so a spreadsheet does not run it as a formula; NDJSON is lossless, so recompute hashes from it. Read-log rows are not published as events: they would copy PHI access metadata onto the bus.

**Break-glass review (HIPAA emergency access).** A read recorded with the reserved reason code `BREAK-GLASS` as its purpose (`BREAK-GLASS` alone, or followed by `:` or a space and the reason) is emergency access: in `hr`, a person without `hr.worker.inquire` reads another worker's benefits under `hr.benefits.break-glass`, a duty kept for emergencies. `GET /v1/audit/reads:break-glass` lists those reads oldest first (`status=pending`, the default, is the review queue; `reviewed`; `all`; `limit`, `cursor` as above), and `POST /v1/audit/reads/{id}:review` with `{"outcome": "APPROVED" | "FLAGGED", "note": "..."}` records a second person's review, once; FLAGGED needs a note. The reviewer is the verified caller and may not be the reader (AUD-0005, as for a read that is not break-glass or is already reviewed; two racing reviews meet at the primary key and the loser gets AUD-0005). The review is a row in `phi_read_review` (migration `0004_phi_read_review.sql`): row-level secured, append-only (trigger; `passage_app` holds `SELECT` and `INSERT`), keyed by the read, so the `phi_read` chain is untouched and still verifies.

Metrics: `audit_phi_reads_recorded_total`, `audit_phi_reads_failed_total`, `audit_phi_read_exports_total`, `audit_phi_break_glass_reviews_total`. To accept `hr` service tokens set `serviceIdentity.publicKeys` in the chart (`PASSAGE_SERVICE_NAME=audit`, `PASSAGE_SERVICE_PUBLIC_KEYS`).

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body or missing change fields |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| AUD-0001 | 422 | domain rejection (invalid change, checkout rules) |
| AUD-0002 | 422 | a read-log request named the reader (`userId`); the reader is the verified caller |
| AUD-0003 | 400 | invalid read-log filter: time range, `limit`, `cursor` or export `format`, or break-glass `status` |
| AUD-0004 | 404 | a break-glass review named a read this tenant's log does not hold |
| AUD-0005 | 409 | a break-glass review that may not be made: the read is not break-glass, is already reviewed, or the reviewer is the reader |
| COM-0001 | 400 | read-log entry failed validation (missing or over-long `purpose`, `fields`, `entity`, `recordId`; `occurredAt` in the future) |

Metrics: `audit_changes_applied_total`, `audit_changes_failed_total`.
