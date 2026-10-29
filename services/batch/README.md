# batch

Wave 04 event backbone, approval workflow API, partitioned batch runner and notify service. Temporal workers live in `services/workflow-ts`. Kafka/Debezium/Apicurio/Temporal cluster manifests live under `deploy/kafka` and `deploy/temporal`.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/batch`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL`. RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app` (NOSUPERUSER NOBYPASSRLS).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/workflows/definitions`, `POST /v1/workflows/instances`, instance actions | `workflow.write` |
| `GET /v1/workflows/instances/{id}` | `workflow.read` |
| `GET /v1/workitems`, `POST /v1/workitems/{id}/actions` | `workflow.approve` |
| `POST /v1/batch/jobs`, `POST /v1/batch/jobs/{code}/runs`, `POST /v1/events/replay` | `batch.write` |
| `GET /v1/batch/jobs/{code}`, `GET /v1/batch/runs/{id}` | `batch.read` |
| `POST /v1/notify/templates`, preferences, retry | `notify.write` |
| `GET /v1/notify/messages` | `notify.read` |
| `POST /v1/notify/messages` | `notify.send` |
| `GET /v1/events/topics`, schemas, AsyncAPI | `events.read` |
| `POST /v1/events/schemas/{type}` | `events.write` |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | public |

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

OPA duties `Maintain workflows`, `Approve workflow`, `Maintain batch jobs`, `View batch history`, `Maintain message templates`, `Maintain notification settings`, `View outbound messages` and `Send notifications` authorize the `/v1/workflows`, `/v1/workitems`, `/v1/batch` and `/v1/notify` paths.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body or failed field validation |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| WFL-0401 | 409 | recall after an approval |
| WFL-0408 | 409 | job is on hold |
| WFL-0409 | 409 | exclusive job already running |
| WFL-0410 | 400 | submitter cannot approve their own document |
| EVT-0401 | 409 | event schema is not BACKWARD_TRANSITIVE |
| NTF-0406 | 400 | template uses an unknown placeholder |
| NTF-0407 | 400 | template render failed |

`internal/jobs.Runner` splits a finite row range into deterministic partitions. Each row effect, idempotency marker and checkpoint is committed in one store transaction. The 1,000,000-row test kills the first run at row 400,000 and resumes without duplicates.

Product jobs: `jobs.ProductJobs` hosts `product.explode` (params `{"masterId","selected"}`) and `product.group-change`. They call the product HTTP API through `jobs.ProductClient` (`HTTPProductClient` forwards the caller's tenant, legal entity, user and duties headers and the request id; the principal must hold `product.write` for explode and `product.read` plus `product.jobs.write` for group-change) and never touch the product database. Each page is at most 500 items (`POST .../variants:explode` with `limit`, `GET /v1/product/released-products?after=&limit=` feeding `POST /v1/product/jobs/group-change`). Every page is recorded in `batch_run_pages` (items, succeeded, skipped, cursor, next cursor, failure text) and `batch_run.rows_done` follows the sum of completed pages; a failed page is retried up to `MaxAttempts` (a 4xx from product is not retried), recorded as `failed`, and ends the run with status `error`. Re-running the same run id resumes at the page after the last completed one, and a finished run does nothing; both product calls are idempotent (explode skips existing variants, group-change is pure), so a crash between the product call and its checkpoint repeats at most one page. `passage_app` may update only `status`, `ended_at`, `error` and `rows_done` on `batch_run`.

Reserved handler: `metadata.usage` (Wave 03 collector, scheduled here when metadata enqueues it). The metadata registry publish and extend paths write `dev.passage.metadata.entity.published.v1` to its outbox; this service catalogs that type.

CDC: migration `0004_cdc_publication` creates the `passage_outbox_batch` publication. The migrating role needs CREATE on the database and ownership of `outbox`; the Debezium connector in `deploy/kafka` must start after migration 0004.

Metrics: `batch_submitted_total`, `batch_failed_total`. Extra Go dependency: `github.com/go-chi/chi/v5` (HTTP router, same as every other service).
