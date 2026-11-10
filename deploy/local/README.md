# Local stack

`make dev` (from the repository root) runs `tools/e2e/cmd/passage-dev up`:

1. Renders `.run/dev/realm-passage.json` (the production realm export with plain HTTP, no second factor, the web client redirecting to http://localhost:5173, and the users in `seed.json`) and `.run/dev/assignments.json` (the OPA data granting each seeded user their duties).
2. `docker compose -f deploy/local/compose.yaml up -d --wait`: Postgres (host port 15432), Kafka (9092), Keycloak 26 (8080, realm `passage`) and OPA (8181).
3. Builds and starts ledger, numbering, party, purchasing, receiving and batch (which serves `GET /v1/inbox` for My work, reading purchasing, party and ledger) on ports 18100 and up, each with its own database `passage_dev_<name>`, migrations on start, service-to-service signing keys and receiving's `PASSAGE_ALLOW_FAKES=1`. Seeds the ledger and chart accounts, the PARTY number sequence and a purchase order signing limit for each seeded user who can confirm purchase orders.
4. Starts `services/identity/cmd/devgateway` on 127.0.0.1:8088: verifies the Keycloak JWT, asks OPA about method and path, strips every inbound `X-Passage-*` header and sets them from the token claims, strips `/api` and proxies to the service named by the path (`PASSAGE_SVC_<NAME>_URL`; `PASSAGE_SVC_INBOX_URL` must point at batch, which owns `/v1/inbox`).
5. Starts the Vite dev server with `PASSAGE_OIDC_ISSUER`, `PASSAGE_OIDC_CLIENT_ID` and `PASSAGE_GATEWAY_URL`, so the app signs in and proxies `/api` to the gateway.

Logs are in `.run/dev/logs`. Visit `/auth/logout` to sign out and switch users.

Seeded users (dev only, never reuse these passwords): `admin` / `passage-admin-dev`, `clerk` / `passage-clerk-dev`. Every duty in `seed.json` must exist in `policies/authz/data.json`; `tools/e2e/devenv` tests that.

Without Docker: `cd tools/e2e && PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...` runs the same flow against the real binaries and, with `opa` on PATH, through the gateway.

Not proven on CI: the compose bring-up itself (images, Keycloak realm import, healthchecks) and the Playwright spec `apps/web/e2e/login.live.spec.ts` (`PASSAGE_E2E_LIVE_URL=http://localhost:5173`).
