# Passage

Passage is an open-architecture **ERP** (enterprise resource planning) system: the software a company runs its finance, purchasing, inventory, manufacturing, sales and HR on. It is being built from scratch to replace Microsoft Dynamics AX / D365 and SAP ECC / S/4HANA, in **50 sequential waves**. Every wave ships something that can be tested and deployed.

**Status:** early build. Most domains exist as Go services with tests and a web screen, but there is no single "start everything" command yet, and the API gateway and login (Keycloak) are not wired. See [roadmap/README.md](roadmap/README.md) for where each wave stands.

## What's in the repo

| Path | What it is |
|---|---|
| `apps/web` | React web app (Vite). One module per domain under `src/modules/` |
| `services/<domain>` | One backend service per business area (ledger, payables, inventory, ...). Mostly Go; `-ts` and `-py` suffixes are TypeScript and Python |
| `platform/go` | Shared Go kernel: HTTP, tenancy, auth, database, outbox, events |
| `policies/authz` | OPA/Rego authorization policy and duty catalog |
| `deploy` | Kubernetes, Argo CD, OpenTofu and admission policies |
| `docs` | PRD, architecture, ADRs, conventions, runbooks |
| `roadmap` | The 50-wave plan (`waves.json` is the source of truth) |

Stack ([ADR-0001](docs/adr/)): Kubernetes · Go, TypeScript, Python · Postgres per domain · Kafka + Debezium · Temporal · Keycloak + OPA · React.

## Run it locally

**Prerequisites:** Go 1.25+, Node 24 (or 22.22+), Python 3.11+, and Docker (or a local Postgres 16+).

### 1. The web app (no backend needed)

```sh
cd apps/web
npm ci
npm run dev        # http://localhost:5173
```

The shell, navigation and screens render. Calls to `/api/*` are proxied to `localhost:8080`, so data loads only when a service is running there (step 2).

### 2. One backend service

Each service is a standalone binary that needs a Postgres database. Ledger is shown here; the others follow the same pattern (`services/<name>/cmd/<name>`).

```sh
docker run -d --name passage-pg -p 5432:5432 -e POSTGRES_PASSWORD=passage postgres:17

cd services/ledger
DATABASE_URL='postgres://postgres:passage@localhost:5432/postgres?sslmode=disable' \
MIGRATE_ON_START=true go run ./cmd/ledger
# API on :8080, health/admin on :9090
```

There is no gateway yet, so local requests must carry the identity headers it would normally set:

```sh
curl localhost:9090/readyz
curl -H 'X-Passage-Tenant: 00000000-0000-0000-0000-000000000001' \
     -H 'X-Passage-User: dev' \
     -H 'X-Passage-Duties: ledger.journal.read' \
     localhost:8080/api/v1/ledger/ledgers/00000000-0000-0000-0000-0000000000aa
# → 404 "ledger not found": the service, database and auth are working
```

Duty names live in [policies/authz/data.json](policies/authz/data.json). Services trust these headers, so **never expose a service outside your machine** until the gateway lands ([#82](https://github.com/BenjaminSRussell/Passage/issues/82)).

### 3. A local Kubernetes cluster (optional)

`make deploy-local` creates a k3d cluster with Argo CD, Postgres and a sample service. It needs Docker, k3d, OpenTofu, kubectl and about 16 GB of RAM. See [docs/runbooks/delivery/deploy-local.md](docs/runbooks/delivery/deploy-local.md).

## Test

```sh
make vet           # go vet, every Go module
make test-go       # Go unit tests
make test-ts       # web + TypeScript tests (vitest)
make test-e2e      # Playwright + axe (run `npx playwright install chromium` once)
make test-py       # Python services
```

Database tests skip unless you point them at Postgres. They use a superuser plus a non-superuser `passage_app` role so that row-level security is actually exercised:

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

## Working the plan

Each wave is a GitHub issue labeled `wave` and `phase-N` ([project board](https://github.com/users/BenjaminSRussell/projects/5)). A wave closes when all of its proof boxes are checked and its Argo CD app is healthy in `stage`. To change the plan, edit `roadmap/waves.json`, then run `python3 scripts/build_roadmap.py --github`.
