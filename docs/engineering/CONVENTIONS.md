# Passage engineering conventions

Read this before writing any code. Every wave agent is bound by it; the reviewer checks against it.

## Repository layout

```
platform/go/            shared Go kernel (httpx, dbx, tenant, authz, events, outbox, money, ids, testkit) — DO NOT EDIT from a wave
services/<name>/        one Go module per bounded context (standalone go.mod with a replace to ../../platform/go)
  cmd/<name>/main.go    entrypoint: dbx.Connect → dbx.Migrate → httpx.NewRouter → register → httpx.Serve
  internal/domain/      entities, invariants, pure functions (no I/O)
  internal/store/       Postgres repositories (pgx), RLS-aware
  internal/api/         HTTP handlers, request/response DTOs, validation
  internal/events/      event type constants + payload structs
  migrations/           NNNN_name.sql, applied in order by dbx.Migrate
  api/openapi.yaml      OpenAPI 3.1 for every endpoint the service exposes
  events/asyncapi.yaml  AsyncAPI 3 for every event the service publishes/consumes
  charts/<name>/        Helm chart (Chart.yaml, values.yaml, templates/deployment.yaml, service.yaml)
  README.md             what the service does, how to run, wave reference
services/<name>-ts/     TypeScript services (Temporal workflows, integrations) — pnpm, vitest
services/<name>-py/     Python services (forecasting, OR-Tools, evals) — uv/pytest
apps/web/               React shell (Vite + TS). Modules live in apps/web/src/modules/<module>/ and are auto-discovered
apps/<other>/           Other front-ends (shopfloor, warehouse-mobile, portals) — separate Vite apps
deploy/                 helm umbrella, argocd apps, terraform/tofu modules, k3d config
roadmap/ownership.json  wave → directories it owns. A wave edits ONLY its own directories.
```

## Ownership rule (hard)

`roadmap/ownership.json` maps each wave to the directories it owns. An agent working wave N creates and edits files only under those paths. It never edits `platform/` (owned by the kernel, wave 0 — raise a handoff row instead), another wave's service, `apps/web/src/shell`, root config, or another wave's chart. If it needs something from another wave that does not exist yet, it codes against the interface described in that wave's PRD section and ships a small in-package fake behind an interface — never a cross-directory edit.

## Go services

- Go 1.26+, module path `github.com/BenjaminSRussell/passage/services/<name>`; `replace github.com/BenjaminSRussell/passage/platform => ../../platform/go`.
- Allowed dependencies: the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid). Anything else needs a one-line justification in the service README.
- Every HTTP handler: decode → validate → `tenant.FromContext` → store/domain → `httpx.JSON` or `httpx.Problem`. Errors are RFC 9457 problem details with a stable `code` (e.g. `LED-0001`) listed in the service README.
- Every state change that other contexts care about is written to the outbox in the same transaction (`outbox.Enqueue(ctx, tx, evt)`). Event types are `dev.passage.<context>.<entity>.<verb>.v1`.
- All tables carry `tenant_id uuid not null` first and enable row-level security (`dbx.RLSPolicy` template in migrations, plus `dbx.LegalEntityScopePolicy` on tables that carry `legal_entity_id`). Money is `numeric(20,6)` + `currency char(3)`; never float.
- Idempotent commands accept `Idempotency-Key` and store it (`idempotency_keys` table from `dbx.IdempotencyDDL`).
- Tests: `internal/domain` has pure unit tests (table-driven; property tests with `pgregory.net/rapid` are encouraged — it is in the platform go.mod). Store/API tests use `testkit.DB(t)` and are skipped when `PASSAGE_TEST_DATABASE_URL` is unset. `go test ./...` must pass with and without the database.
- `go vet ./...` clean. No `init()` side effects. No globals except constants.

## TypeScript

- Node 20+, TypeScript strict, ESM. Front-end: React 18, react-router, @tanstack/react-query, zod. Tests: vitest + @testing-library/react.
- Do not add npm dependencies to `apps/web` (shared lockfile). A module that needs a library not present documents it in its README under "Needs" for the shell owner (wave 5).
- A web module is `apps/web/src/modules/<module>/module.ts` exporting `{ id, title, routes: RouteObject[], nav: NavItem[] }`; the shell discovers it via `import.meta.glob`. Components under the same folder. API clients under `api/` using `fetch` with the `/api/v1/...` paths from the service's OpenAPI.

## Python

- Python 3.11+, `pyproject.toml`, pytest, `hypothesis` for property tests; FastAPI for HTTP. Only numeric/ML/optimization work lives in Python.

## Helm / deploy

- Every service ships `charts/<name>` with: image repository/tag values, `DATABASE_URL` from a secret, liveness/readiness on `/healthz`/`/readyz`, resources, a ServiceMonitor toggle. `helm lint` must pass.

## Definition of done for a wave build (what the reviewer checks)

1. Every deliverable bullet in `roadmap/waves.json` for the wave has code, or a `NOT-IMPLEMENTED.md` in the service root listing exactly what is missing and why.
2. `go test ./...` / `pnpm test` / `pytest` pass in every owned directory; `go vet` clean.
3. OpenAPI and AsyncAPI files exist and match the handlers/events in code.
4. Migrations apply cleanly on an empty database (verified when a DB is available) and include RLS.
5. Chart lints. README explains run/test and lists error codes.
6. No edits outside owned directories (`git status` is the check).
