# projects

Project operations for Passage: project master and hierarchy, stages, WBS and CPM scheduling,
POC and EVM arithmetic, billing selection rules and posting-profile resolution. Wave 41, ticket PSG-41.

**Slice 1 (this build)** exposes the project master over HTTP (create with sub-project inheritance,
read, list, stage transitions), persists it with RLS and an outbox event per change, and ships the pure
domain rules that later wave-41 slices wire into persistence and APIs. See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md) for what
is still missing and why.

## Layout

```
cmd/projects/            entrypoint: dbx.Connect → (dbx.Migrate if MIGRATE_ON_START) → httpx.NewRouter → Register → httpx.Serve
internal/domain/         pure rules, no I/O: project validation, sub-project inheritance and stages (FR-41.1),
                         POC (BR-41.1/41.2), EVM (FR-41.10), CPM forward and backward pass over working calendars
                         with late dates, slack and critical path (FR-41.2), posting-profile fallback (FR-41.5),
                         invoice-proposal selection predicate and "why not billed" (BR-41.3), finish blockers (BR-41.7),
                         retention amount and release trigger (BR-41.10), monthly interest (BR-41.12)
internal/store/          Postgres repository (pgx); every call runs in dbx.WithTenantTx (RLS); writes outbox rows
internal/events/         event type constants and payloads (events/asyncapi.yaml)
internal/api/            HTTP handlers, DTOs, validation, problem mapping
migrations/              0001_init.sql (project + RLS), 0002_outbox.sql (outbox, consumer inbox, idempotency), migrations.go (embed)
api/openapi.yaml         OpenAPI 3.1 for every endpoint
events/asyncapi.yaml     AsyncAPI 3 for every event published
charts/projects/         Helm chart (deployment, service, optional ServiceMonitor)
Dockerfile               distroless image, built from the repository root (`docker build -f services/projects/Dockerfile .`)
testdata/                poc-worked-example.csv: BR-41.1/41.2 worked example (period 1 to 3)
```

Web module: `apps/web/src/modules/projects/`: projects overview (counts by stage), all-projects list
(stage and type filters, load more), new project / sub-project form, and project detail with stage moves.

## Endpoints (slice 1)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/projects` | ProjMaintain | `Idempotency-Key` required (≤ 255 chars); 201 created, 200 on replay with `Idempotent-Replayed: true`. A sub-project inherits omitted group, customer and contract from its parent (FR-41.1) |
| GET | `/v1/projects` | ProjInquire | `stage`, `projectType`, `limit` (1–100), `cursor` |
| GET | `/v1/projects/{id}` | ProjInquire | `ETag: "<version>"` |
| POST | `/v1/projects/{id}/transitions` | ProjManage | body `{"to": "..."}`; `If-Match: "<version>"` required |

`GET /healthz` is the kernel liveness check; `GET /readyz` pings the database (503 when it is down).
Browser calls use `/api/v1/projects/...`; the BFF rewrites them to `/v1/projects/...`.

Stage moves: CREATED → IN_PROCESS; IN_PROCESS ⇄ ON_HOLD; IN_PROCESS → FINISHED. FINISHED is terminal.

## Run

```
DATABASE_URL=postgres://passage_app:...@localhost:5432/passage_projects \
MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/projects
```

`MIGRATE_ON_START` is for dev only; stage and prod run migrations as the Argo PreSync Job with the owner role.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./internal/store/...   # DB-backed tests
```

- `internal/domain`: table-driven and property tests (pgregory.net/rapid). No database needed.
- `internal/api`: handler tests over an in-memory store that mirrors the Postgres version and idempotency rules.
- `internal/store`: tenant and legal-entity RLS isolation, sub-project inheritance, idempotency replay
  (including concurrent requests with one key), duplicate ids, version conflicts, filters and paging, and the
  outbox CloudEvent. Skipped without `PASSAGE_TEST_DATABASE_URL`; also needs `PASSAGE_TEST_APP_DATABASE_URL`
  (uses `testkit.DBWithApp`, so the app role has no BYPASSRLS).

Web: `npx vitest --run` and `npx tsc --noEmit` from `apps/web` (module tests in `module.test.tsx`).

Chart: `helm lint charts/projects`.

## Dependencies

Only the platform module and what it already pulls in (chi, pgx/v5, shopspring/decimal, google/uuid, rapid for tests).
No new third-party modules.

## Error codes

Problem details are RFC 9457 (`application/problem+json`), `type` = `https://passage.dev/problems/<code>`.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Request failed validation; `errors` names the fields (includes a missing `Idempotency-Key`, and a sub-project still incomplete after inheriting from its parent) |
| COM-0428 | 428 | `If-Match` (project version) missing on a stage move |
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Caller lacks the route's duty (kernel `authz`), or the project's legal entity is outside the caller's scope |
| COM-0500 | 500 | Unexpected error; detail hidden (kernel `httpx.Fail`) |
| PROJ-NOT-FOUND | 404 | No project with that id in the tenant |
| PROJ-CREATE-001 | 409 | A project with this id already exists |
| PROJ-PARENT-001 | 422 | Parent project does not exist, or is outside the caller's legal-entity scope |
| PROJ-PARENT-002 | 422 | Sub-project conflicts with its parent: another customer or another legal entity (FR-41.1; service-specific, not in §8.41.18) |
| conflict_version | 412 | `If-Match` does not match the stored version (someone else changed the project) |
| idempotency_key_reuse | 422 | Same `Idempotency-Key` sent with a different body |
| PROJ-STAGE-001 | 422 | Stage move not allowed (FR-41.1) |
| PROJ-STAGE-002 | 422 | Domain rule (BR-41.7): project cannot be FINISHED; raised by `domain.CanFinish`, not yet wired to the API (see NOT-IMPLEMENTED) |
| PROJ-POST-001 | — | Domain rule (FR-41.5): no posting profile at any fallback level; raised by `domain.Resolve`, no HTTP path yet |
| PROJ-WBS-001 | — | Domain rule (FR-41.2): dependency cycle; raised by `domain.Forward`, no HTTP path yet |
| PROJ-RET-001 | — | Domain rule (BR-41.10): release before its trigger without override; raised by `domain.CheckRelease`, no HTTP path yet |

Other wave-41 codes from PRD §8.41.18 are not raised yet; they arrive with the slices that implement their rules.

## Events

`dev.passage.projects.project.created.v1` and `dev.passage.projects.project.updated.v1` are written to the outbox
in the same transaction as the change, with `subject` = project id and `actor` = the caller's user id
(see `events/asyncapi.yaml`).

## Wave reference

PRD §8.41 (`docs/prd/phase-8-projects-people-service.md`), ticket PSG-41, depends on PSG-40.
Rules implemented in the domain: BR-41.1, BR-41.2, BR-41.3 (base predicate), BR-41.7 (rule only), BR-41.10 (amount
and trigger), BR-41.12 (charge), FR-41.1 (stages and inheritance, wired to the API), FR-41.2 (CPM), FR-41.5, FR-41.10.
