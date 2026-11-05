# Passage

Passage is an open-architecture **ERP** (enterprise resource planning) system: the software a company runs its finance, purchasing, inventory, manufacturing, sales and HR on. It is being built from scratch to replace Microsoft Dynamics AX / D365 and SAP ECC / S/4HANA, in **50 sequential waves**. Every wave ships something that can be tested and deployed.

**Status:** early build. Most domains exist as Go services with tests and a web screen, but there is no single "start everything" command yet, and the API gateway and login (Keycloak) are not wired. See [roadmap/README.md](roadmap/README.md) for where each wave stands.

## What's in the repo

| Path | What it is |
|---|---|
| `apps/web` | React web app (Vite). One module per domain under `src/modules/` |
| `services/<domain>` | One backend service per business area (ledger, payables, inventory, ...). Mostly Go; `-ts` and `-py` suffixes are TypeScript and Python |
| `platform/go` | Shared Go kernel: HTTP, tenancy, auth, database, outbox, events |
| `policies/authz` | OPA/Rego authorization policy |
| `deploy` | Kubernetes, Argo CD, OpenTofu and admission policies |
| `docs` | PRD, architecture, ADRs, conventions, runbooks |
| `roadmap` | The 50-wave plan (`waves.json` is the source of truth) |

Stack ([ADR-0001](docs/adr/0001-platform-stack.md)): Kubernetes · Go, TypeScript, Python · Postgres per domain · Kafka + Debezium · Temporal · Keycloak + OPA · React.

## Run it locally

**Prerequisites:** Go 1.26+, Node 24.15+ (or 22.22.2+), Python 3.11+, and Docker.

### 1. The web app

```sh
cd apps/web
npm ci
npm run dev        # http://localhost:5173
```

The shell, navigation and screens render. API convention: the browser calls `/api/v1/<svc>/...`, services serve `/v1/<svc>/...`, and the edge strips `/api`. In dev the Vite proxy ([src/shell/devProxy.ts](apps/web/src/shell/devProxy.ts)) does that: it forwards `/api/v1/<svc>/...` to `http://localhost:8080/v1/<svc>/...` and adds the identity headers the gateway would set.

Every service listens on :8080 by default, so run one at a time or give each its own `HTTP_ADDR` and point the proxy at it with `PASSAGE_SVC_<NAME>_URL` (`<NAME>` is the path segment upper-cased, `-` as `_`; `PASSAGE_SVC_DEFAULT_URL` replaces the :8080 fallback). Set these in the shell or in `apps/web/.env.local`:

```sh
PASSAGE_SVC_LEDGER_URL=http://localhost:8081   # ledger started with HTTP_ADDR=:8081
PASSAGE_DEV_TENANT=00000000-0000-0000-0000-000000000001
PASSAGE_DEV_LEGAL_ENTITY=00000000-0000-0000-0000-0000000000e1
PASSAGE_DEV_USER=dev
PASSAGE_DEV_DUTIES=ledger.journal.read,ledger.journal.post   # comma-separated; each service lists its duties in internal/api/api.go
```

`PASSAGE_DEV_DUTIES` defaults to empty, so any duty-guarded call returns 403 until you set the duties the service checks. The same list is given to the shell's modules so UI that needs a duty (for example `org.hierarchy.write` for the hierarchy designer's edit mode) is shown; restart `vite` after changing it. Gated nav entries and routes are hidden for duties you lack; set `PASSAGE_DEV_SHOW_ALL_NAV=1` to show every nav entry and open every page in `vite` without changing the duties sent to services.

The gateway ([#82](https://github.com/BenjaminSRussell/Passage/issues/82)) is expected to do the same: its HTTPRoutes match `/api/v1/<svc>` and rewrite to `/v1/<svc>` (strip `/api`), and set the `X-Passage-*` headers from the verified token.

### 2. One backend service

Each service is a standalone binary that needs its own Postgres database. Ledger is shown here. The others follow the same pattern (`services/<name>/cmd/<name>`, with a database of their own), one at a time, since they all listen on :8080.

```sh
docker run -d --name passage-pg -p 5432:5432 -e POSTGRES_PASSWORD=passage postgres:17
docker exec passage-pg createdb -U postgres ledger

cd services/ledger
DATABASE_URL='postgres://postgres:passage@localhost:5432/ledger?sslmode=disable' \
MIGRATE_ON_START=true LEDGER_PERIOD_CHECK=disabled go run ./cmd/ledger
# API on :8080, health/admin on :9090
```

There is no gateway yet, so requests must carry the identity headers it would normally set:

```sh
curl localhost:9090/readyz
curl -H 'X-Passage-Tenant: 00000000-0000-0000-0000-000000000001' \
     -H 'X-Passage-User: dev' \
     -H 'X-Passage-Duties: ledger.journal.read' \
     localhost:8080/v1/ledger/ledgers/00000000-0000-0000-0000-0000000000aa
# → 404 "ledger not found": the service, database and auth are working
```

Each service declares the duty names it checks at the top of its `internal/api/api.go` (ledger's are in [services/ledger/internal/api/api.go](services/ledger/internal/api/api.go)). Services trust these headers, so **never expose a service outside your machine** until the gateway lands ([#82](https://github.com/BenjaminSRussell/Passage/issues/82)). When you're done: `docker rm -f passage-pg`.

### 3. Local Kubernetes (optional)

`make deploy-local` brings up a k3d cluster with Argo CD, Postgres and a sample service. Prerequisites (about 16 GB RAM, ports 80/443 free) are in [docs/runbooks/delivery/deploy-local.md](docs/runbooks/delivery/deploy-local.md).

## Test

```sh
make vet           # go vet, every Go module
make test-go       # Go unit tests
make test-ts       # web + TypeScript tests (vitest)
make test-e2e      # Playwright + axe (run `npx playwright install chromium` once)
make test-py       # Python services
```

`test-ts` and `test-py` expect each package's dependencies to be installed first: `npm ci` in each TypeScript package, and `pip install -e '.[test]'` in each `services/*-py`.

Database tests skip unless you point them at Postgres. They use a superuser plus a non-superuser `passage_app` role, so row-level security is actually exercised:

```sh
docker exec passage-pg psql -U postgres -c "CREATE ROLE passage_app LOGIN PASSWORD 'passage_app'"
export PASSAGE_TEST_DATABASE_URL='postgres://postgres:passage@localhost:5432/postgres?sslmode=disable'
export PASSAGE_TEST_APP_DATABASE_URL='postgres://passage_app:passage_app@localhost:5432/postgres?sslmode=disable'
make test-go
```

## Learn more

- [docs/PRD.md](docs/PRD.md): what Passage does and why
- [docs/architecture.md](docs/architecture.md): how it fits together
- [docs/engineering/CONVENTIONS.md](docs/engineering/CONVENTIONS.md): coding rules for services
- [apps/web/docs/design-system.md](apps/web/docs/design-system.md): UI design system
- `services/<name>/NOT-IMPLEMENTED.md`: what each service still lacks
- [roadmap/README.md](roadmap/README.md) and the [project board](https://github.com/users/BenjaminSRussell/projects/5): the 50-wave plan, one issue per wave. To change it, edit `roadmap/waves.json`, then run `python3 scripts/build_roadmap.py --github`.
