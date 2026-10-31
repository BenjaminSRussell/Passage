# batch

Wave 04 event backbone, approval workflow API, partitioned batch runner and notify service. Temporal workers live in `services/workflow-ts`. Kafka/Debezium/Apicurio/Temporal cluster manifests live under `deploy/kafka` and `deploy/temporal`.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/batch`. Test: `go vet ./... && go test ./...`; store tests need `PASSAGE_TEST_DATABASE_URL`. RLS proof also needs `PASSAGE_TEST_APP_DATABASE_URL` as `passage_app` (NOSUPERUSER NOBYPASSRLS).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |
| `PRODUCT_URL` | base URL of the product service; setting it starts the run dispatcher and then requires `PASSAGE_SERVICE_NAME=batch` and `PASSAGE_SERVICE_SIGNING_KEY` (or `_FILE`), else startup fails |
| `DISPATCH_INTERVAL`, `DISPATCH_CONCURRENCY` | scan period (default `5s`) and runs executed at once per replica (default `4`) |
| `DISPATCH_MAX_RUN_ATTEMPTS`, `DISPATCH_RETRY_AFTER` | claims a run may use before the dispatcher leaves it alone (default `5`) and the wait before an `error` run is retried (default `1m`) |

### Notification senders

| Variable | Meaning |
|---|---|
| `NOTIFY_SMTP_ENABLED`, `NOTIFY_WEBHOOK_ENABLED` | `true` enables an adapter; startup fails if its credentials are missing |
| `NOTIFY_SECRETS_DIR` | when set, credentials are read from files in it only (no env fallback); an empty file counts as missing |
| `NOTIFY_SMTP_FROM`, `NOTIFY_WEBHOOK_URL` | non-secret settings; the webhook URL must be `https` |
| `NOTIFY_SMTP_TLS` | `starttls` (default; refuses servers that do not advertise it), `implicit` (port 465), or `none` (host exactly `localhost`, `127.0.0.1` or `::1`) |
| `NOTIFY_ALLOW_INSECURE` | dev only: `true` permits `http` webhooks and `none` TLS to any other host |
| `NOTIFY_DISTRIBUTE_INTERVAL` | how often the distributor claims queued `email`/`webhook` messages (default `5s`); it runs only when an adapter is enabled |

Secrets are read once at startup, so an ESO rotation needs a pod restart: set `notify.reloader=true` to add the `reloader.stakater.com/auto` annotation to the Deployment (requires Stakater Reloader in the cluster). The `ExternalSecret` uses `external-secrets.io/v1`, needs `notify.externalSecret.environment`, and reads `<pathPrefix>/<key>` with `property: value` from the `ClusterSecretStore` named by `notify.externalSecret.storeName`.

The distributor claims queued messages across tenants with `notify_claim_messages` (`FOR UPDATE SKIP LOCKED`, a 2 minute send lease), retries a failed send after 1m, 5m, 15m, 1h (five attempts in all, `domain.NextAttempt`), then marks it `failed`. The webhook gets the message id as `Idempotency-Key` and SMTP as the `Message-ID`, so a receiver can drop duplicates. If a send's lease expires with the outcome unrecorded, the message is failed as `delivery outcome unknown` and never resent automatically; `POST /v1/notify/messages/{id}/retry` re-queues it. Metrics are `notify_distribute_*` on the admin port. Like the run dispatcher, it needs the migrating role to be a superuser or BYPASSRLS (see `notify_distribute_can_scan`).

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

Product jobs: `jobs.ProductJobs.Run(ctx, handler, runID)` hosts `product.explode` and `product.group-change`. It first claims the run: the run must belong to a job with that handler and be `executing` (or `error`, to resume) and not leased by another worker (`batch_run.claimed_until`, renewed by every recorded page and before every attempt, `RunLease` 10 minutes); otherwise it returns `ErrRunNotActive` and touches nothing, so `canceled`, `canceling`, `hold`, `ended` and `waiting` runs are never started or overwritten, and a cancel mid-run stops the worker at its next checkpoint and returns without trying to finish the run. The claim stamps a fresh `lease_token` and every later write is fenced on it, so a stalled worker whose lease expired cannot write over its successor. Params come from `batch_run.params` (explode needs `{"masterId","selected"}`; invalid params end the run in `failed`). Calls go to the product HTTP API through `jobs.ProductClient` and never touch the product database. Each page is at most 500 items. Every page is recorded in `batch_run_pages` and `batch_run.rows_done` follows the sum of completed pages; a failed page is retried up to `MaxAttempts` (a 4xx from product is not retried), recorded as `failed`, and ends the run with status `error`, or `failed` (terminal, never retried) when product rejected the request (4xx) or the params are invalid. Finishing uses a context that survives cancellation. Re-running the same run id resumes after the last completed page. Both product calls are idempotent, so a crash between a product call and its checkpoint repeats that call safely; explode stores product's `remaining` in the page cursor, so the next call credits any variants the lost call created. A crash on the very first page (page 0), before any checkpoint, cannot be credited, and a retry of page 0 can leave `rows_done` short by the variants the lost call created.

Starting a run: `POST /v1/batch/jobs/{code}/runs` accepts `params`, validated against the job's handler (`product.explode`: `{"masterId": uuid, "selected": {axis: [values]}}`, unknown keys refused; `product.group-change` and any handler without a schema: none) and stored in `batch_run.params`. Every run is bound to one legal entity (`batch_run.legal_entity_id`, never NULL): the body's `legalEntityId` when it lies in the caller's scope (or the caller holds the all-scope duty), else the caller's only one; a caller with none, several or all legal entities must name one (422). A product run also records its creator (`triggered_by`) and is refused with 403 unless the creator holds the product duties the run will use: for explode `product.variants.explode` or `product.write`, for group-change (`product.released.read` or `product.read`) and `product.jobs.write`; saving a job with a product handler needs the same. The route-scoped duties are stored on the run (`service_duties`) and are the only duties its service token can carry. There is no live identity lookup in this service, so that stored snapshot is the authority at dispatch time: a creator whose duties are later revoked does not stop a run already created. Runs created before migration 0008 have no legal entity or duties, are visible only at all-scope, and are never dispatched.

Dispatcher: `jobs.Dispatcher` scans every `DISPATCH_INTERVAL` through the `SECURITY DEFINER` function `batch_dispatchable_runs` (the only cross-tenant read; `passage_app` may execute it; its `search_path` is pinned to `pg_catalog`, the migration schema and `pg_temp`; the migrating role must be a superuser or `BYPASSRLS`, since `batch_run` forces row security, and at start the dispatcher calls `batch_dispatch_can_scan()` and logs an error and raises `batch_dispatch_scan_blind` to 1 when it is not) for `executing` runs of the two product handlers whose lease is free or expired, plus `error` runs older than `DISPATCH_RETRY_AFTER`, each with fewer than `DISPATCH_MAX_RUN_ATTEMPTS` claims (`batch_run.attempts`, bumped by every claim). Every scan first moves runs that used all their attempts to the terminal `failed` status (`batch_fail_exhausted_runs`; log `dispatch attempts exhausted`, metric `batch_dispatch_exhausted_total`). It runs up to `DISPATCH_CONCURRENCY` of them at once, each as a principal built from the run row (tenant, the run's legal entity, the creating user). Any number of replicas may run a dispatcher: the claim lease and lease token admit one worker per run, and the loser logs `batch run not taken`. On shutdown the context is cancelled, in-flight runs record their page and finish as `error` (retried after `DISPATCH_RETRY_AFTER`), and the process waits for them before closing the pool. Metrics: `batch_dispatch_inflight`, `batch_dispatch_started_total`, `batch_dispatch_ended_total`, `batch_dispatch_failed_total`, `batch_dispatch_refused_total`, `batch_dispatch_scan_errors_total`; logs carry `runId`, `tenantId`, `handler` and `correlationId` (`batch-run-<runId>`, also sent to product as the request id).

Service principal: `HTTPProductClient` signs every call with `tenant.ServiceSigner` (service `batch`, audience `product`, 60 second token, fresh `jti` per attempt) carrying the run's legal entity and the creating user as `act` and, per call, only that call's duty from the run's stored snapshot (`ServiceSigner.ApplyDuties`, still capped by the grant); it never forwards user identity headers and refuses to call (`ErrRejected`) without a signer, a principal, a single legal entity or an acting user. The grant in `tenant.ServiceGrantTable` and `policies/authz/data.json` `service_grants` is `batch -> product: product.variants.explode, product.released.read, product.jobs.write`, route-scoped duties product accepts on exactly its explode, released-products and group-change routes next to its broad `product.write` and `product.read`; product verifies the token in `tenant.Middleware` (`PASSAGE_SERVICE_NAME=product`, `PASSAGE_SERVICE_PUBLIC_KEYS=batch[:kid]=<base64>`).

Grants: `passage_app` may update only `status`, `ended_at`, `error`, `rows_done`, `started_at`, `claimed_until`, `lease_token` and `attempts` on `batch_run` (`params`, `legal_entity_id`, `service_duties` and `triggered_by` are insert-only; a restrictive row-security policy rejects a NULL legal entity and shows NULL rows at all-scope only).

Reserved handler: `metadata.usage` (Wave 03 collector, scheduled here when metadata enqueues it). The metadata registry publish and extend paths write `dev.passage.metadata.entity.published.v1` to its outbox; this service catalogs that type.

CDC: migration `0004_cdc_publication` creates the `passage_outbox_batch` publication. `0011_cdc_role_grants` gives the `debezium_cdc` role SELECT on `outbox` when that role exists. The migrating role needs CREATE on the database and ownership of `outbox`; the Debezium connector in `deploy/kafka` must start after migrations 0004 and 0011. The `passage_platform` database comes from the `deploy/postgres` chart (Argo app `batch-postgres`). The CDC password is per database (Vault `<prefix>/<release>-postgres/cdc-password`) and each connector reads its own `debezium-cdc-<service>` Secret. `0006`/`0011` grant only if `debezium_cdc` exists when they run: if the service migrated before the database role existed, delete that row from `schema_migrations` and restart. Rotation steps are in `deploy/postgres/README.md`.

Metrics: `batch_submitted_total`, `batch_failed_total`, and the dispatcher metrics above. Extra Go dependency: `github.com/go-chi/chi/v5` (HTTP router, same as every other service).
