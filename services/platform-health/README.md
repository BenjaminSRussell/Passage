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

Recording is an audit record of what was deployed, not the approval. The approval for a prod promotion or rollback is the reviewed pull request that merges the change to `deploy/envs/**` on protected `main`. The CI recording credential therefore holds `View platform releases` (to read the registry cursor), `Maintain platform releases`, `Approve production promotion` and `Rollback environment`, and that credential exists only in the `platform-release` GitHub Environment, which is restricted to `main`. Humans holding these duties can still record by hand. `actor` (the recording user) in the list response is returned only to the operator tenant; other tenants get it empty.

`Idempotency-Key` is scoped per caller tenant. Replaying a key with the same payload returns the original row (201, no new event); a different payload is 409 `COM-0409`.

Each new release writes `dev.passage.platform.release.promoted.v1` or `dev.passage.platform.release.rolledback.v1` to the `outbox` table in the same transaction. The event fires when the release is recorded, not on Argo sync Healthy; Argo Healthy confirmation stays live-only on #213.

Recording happens after merge, not when the manifest is edited: `python3 deploy/scripts/stage_release.py record --env <env> [--since <rev>]` reads the merged manifest history, derives promote or rollback from Git, and POSTs one release per commit that changed the environment's digest, oldest first, each with that commit's sha as `gitSha`. The `record-release` workflow (`.github/workflows/record-release.yml`) runs it on pushes to `main` that touch `deploy/envs/**`, in the `platform-release` GitHub Environment with a 15 minute timeout and a non-cancelling concurrency group. The cursor is the `gitSha` of the registry's newest release for the environment whose sha is a 40-hex commit and an ancestor of HEAD (rows recorded by hand for commits not on main are skipped; the flag, the push's `before`, applies only when the registry is empty), so a failed or cancelled run is resumed by the next run without skipping a digest change; a failed POST stops the walk and exits 3. The cursor must be a 40-hex sha that is an ancestor of the checked-out HEAD. If HEAD is an ancestor of the cursor (re-running an older workflow run) nothing is recorded and the run exits 0; if no row qualifies the run exits 3. A commit that pins the placeholder or an unreadable manifest is skipped with a warning and never recorded. The client never follows redirects and runs cosign without the API token in its environment, so the token cannot leave the configured host. Exit codes: 0 nothing to do (or recorded, or the API URL is unset and it only warns); 2 usage, configuration (the URL must be https unless the host is `localhost` or `127.0.0.1`) or Git-walk errors; 3 registry or API failures (unreachable, non-2xx including 422, bad cursor) or a `--since` that is not a readable commit with a readable manifest. The client follows the listing's `X-Next-Cursor` header (`?cursor=<release id>`, 100 rows per page) until it is absent. The idempotency key is the SHA-256 of environment, service, digest, git sha and action, so a CI re-run replays instead of duplicating.

One-time setup, not done by code, before merging this workflow: create the `platform-release` environment (Settings, Environments) with a deployment branch rule of `main` only, add `PASSAGE_PLATFORM_API_TOKEN` as an environment secret and `PASSAGE_PLATFORM_API_URL` as an environment variable, and confirm no repository-level secret of the same name exists. A workflow that names a missing environment auto-creates it unprotected, which would expose the token to any branch workflow that names it.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, `message` not 1 to 280 characters, or an invalid release (service, environment, action, digest, unregistered environment) |
| COM-0500 | 500 | internal error, detail hidden |
| AUTH-0001 | 401 | missing or malformed tenant (kernel header middleware) |
| AUTH-0003 | 403 | missing duty, caller is not the platform operator tenant, or release writes are not configured |
| COM-0409 | 409 | `Idempotency-Key` reused for a different release |

## Metrics

`hello_ledger_entries_created_total`, `hello_ledger_entries_failed_total`, `hello_ledger_releases_recorded_total` (new rows only), `hello_ledger_releases_replayed_total` (idempotent replays), `hello_ledger_releases_failed_total`, `hello_ledger_http_requests_total`. No Prometheus client dependency: counters are stdlib atomics rendered as text.
