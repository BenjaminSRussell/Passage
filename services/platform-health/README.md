# platform-health (hello-ledger)

Wave 1 placeholder deployable that proves the delivery path: merge, CI, signed image, Argo CD, running pod behind TLS. One Go module, one image (`hello-ledger`).

## Run and test

```
cd services/platform-health
go vet ./... && go test ./...
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/hello-ledger
```

Store tests run only when `PASSAGE_TEST_DATABASE_URL` is set, against a role without BYPASSRLS.
Local cluster: `make -C deploy deploy-local` (see `docs/runbooks/delivery/deploy-local.md`).

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR` | default `:8080` |
| `ADMIN_ADDR` | default `:9090`, serves `/metrics` only |

## API

`POST /v1/hello-ledger/entries` (optional `Idempotency-Key`), `GET /v1/hello-ledger/entries`, `GET /healthz`, `GET /readyz` (database ping, 503 when down), `GET /metrics` on the admin port. `POST /v1/platform/releases` (operator tenant only; duty per environment and action, optional `Idempotency-Key`) records a promote or rollback and `GET /v1/platform/releases?service=&env=` (duty `View platform releases`) lists them. Contract: `api/openapi.yaml`.

## Releases

`platform_release` is append-only and `platform_environment` (seeded dev, stage, prod) is read-only for `passage_app`. Releases are platform-level, not customer data: rows are stored and read under the nil system tenant server-side, so any caller holding `View platform releases` sees every release.

Writes are accepted only when the caller's tenant equals `PLATFORM_OPERATOR_TENANT_ID`; when it is unset every write is 403. Each row stores the caller's tenant and user (`recorded_by_tenant`, `recorded_by_user`) for audit.

The handler checks the duty by environment and action because route-level OPA cannot see the body:

| Request | Duty |
|---|---|
| promote to dev or stage | `Maintain platform releases` |
| promote to prod | `Approve production promotion` |
| any rollback | `Rollback environment` |

Duty separation: dev and stage promotion, prod promotion and rollback are three distinct duties, so the credential CI records with can be granted `Maintain platform releases` alone while `Approve production promotion` and `Rollback environment` stay with named humans. `actor` (the recording user) in the list response is returned only to the operator tenant; other tenants get it empty.

`Idempotency-Key` is scoped per caller tenant. Replaying a key with the same payload returns the original row (201, no new event); a different payload is 409 `COM-0409`.

Each new release writes `dev.passage.platform.release.promoted.v1` or `dev.passage.platform.release.rolledback.v1` to the `outbox` table in the same transaction. The event fires when the release is recorded, not on Argo sync Healthy; Argo Healthy confirmation stays live-only on #213.

Recording happens after merge, not when the manifest is edited: `python3 deploy/scripts/stage_release.py record --env <env> [--since <rev>]` reads the merged manifest (environment, service, digest, git sha), derives promote or rollback from Git history and POSTs it. The `record-release` workflow (`.github/workflows/record-release.yml`) runs it on pushes to `main` that touch `deploy/envs/**`, in the `platform-release` GitHub Environment (restricted to `main`; it holds `PASSAGE_PLATFORM_API_TOKEN`) with a non-cancelling concurrency group. `--since` is the git sha of the registry's newest release for the environment, falling back to the flag (the push's `before`) when the registry is empty, so a cancelled or skipped run never leaves a manifest gap that later 422s a rollback. The client never follows redirects, so the token cannot leave the configured host. With `PASSAGE_PLATFORM_API_URL` set a failed recording exits 3; unset, it only warns. The URL must be https unless the host is `localhost` or `127.0.0.1`. The idempotency key is the SHA-256 of environment, service, digest, git sha and action, so a CI re-run replays instead of duplicating.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, `message` not 1 to 280 characters, or an invalid release (service, environment, action, digest, unregistered environment) |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | missing duty, caller is not the platform operator tenant, or release writes are not configured |
| COM-0409 | 409 | `Idempotency-Key` reused for a different release |

## Metrics

`hello_ledger_entries_created_total`, `hello_ledger_entries_failed_total`, `hello_ledger_releases_recorded_total`, `hello_ledger_releases_failed_total`, `hello_ledger_http_requests_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
