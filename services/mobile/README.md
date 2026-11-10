# mobile (wave 32, PSG-32)

Mobile warehouse execution: device sessions with timeout and resume, role- and
site-based menus, versioned server-driven flows and their step runner, GS1-128
and DataMatrix scan parsing, and idempotent offline replay with a supervisor
conflict queue. Work-changing steps call the warehouse work API as the signed-in
user. Contracts: `api/openapi.yaml`, `events/asyncapi.yaml`. Gaps: `NOT-IMPLEMENTED.md`.

## Routes (`/v1/mobile`)

| Route | Duty |
| --- | --- |
| `POST/GET /sessions`, `/sessions/{id}:heartbeat`, `:end`, `:assign-work`, `POST /sessions/{id}/runs`, `.../runs/current/steps/{step}:submit`, `POST /scan:parse`, `POST /offline:replay`, `GET /menus/{id}`, `GET /events` | `warehouse.work.execute` (listing sessions, `:sweep` and `:force-signout` need `warehouse.work.supervise`) |
| `/devices`, `/menus`, `/flows`, `/flows/{id}`, `/flows/{id}:publish`, `/flows:install-core` | `warehouse.mobile-flow.admin` |
| `GET /offline/conflicts`, `POST /offline/conflicts/{device:seq}:resolve` | `warehouse.offline-conflict.resolve` |

## Rules worth knowing

- A session silent past the idle timeout is replaceable: the next user's login ends it as TAKEOVER and releases its work (AC-32.8). A returning STALE session keeps its work only if the warehouse still has it OPEN or ASSIGNED and no other ACTIVE session holds it.
- A published flow version never changes; runs stay pinned to theirs. An invalid graph is 422 MOB-0113; a stale `If-Match` is 412.
- Offline transactions carry `device_id:seq`. They apply once, in order. The first conflict parks (MOB-0107) and blocks the device's later transactions until a supervisor resolves it.
- Replay single-form statuses: 200 applied, 409 conflict, 422 rejected or sequence gap (MOB-0108) or key reuse, 429 behind an unresolved earlier transaction, 503 warehouse down or device clock more than 300 s off (MOB-0114).
- Error codes: MOB-0101 scan does not match, 0102 check digit, 0103 malformed or unknown AI, 0104 bad date, 0107 conflict, 0108 gap, 0113 invalid graph, 0114 clock skew, 0117 resumed notice, 0118 unmapped.

## Configuration

| Variable | Meaning |
| --- | --- |
| `DATABASE_URL` | required; Postgres, forced row-level security per tenant |
| `WAREHOUSE_URL` | required; base URL of the warehouse service |
| service signing key | required; read by `tenant.ServiceSignerFromEnv` (the warehouse must trust its public key) |
| `MIGRATE_ON_START` | `true` applies migrations at start |
| `MOBILE_IDLE_TIMEOUT_MIN` | 5 to 240, default 30 |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080` and `:9090` (`/metrics`) |

## Service grant

`mobile` may call the warehouse as the user with `warehouse.work.execute`
(`platform/go/tenant/service.go` and `policies/authz/data.json`).

## Events

`flow.published`, `session.started`, `session.ended`, `offline.conflict`,
`offline.resolved` (outbox, source `mobile`).

## Tests

```
PASSAGE_TEST_DATABASE_URL=postgres://localhost:5432/dbxt \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:passage_app@localhost:5432/dbxt \
go test ./...
```

Acceptance map: AC-32.1 `gs1` (`TestAC321...`), AC-32.3 `store/replay_test.go`
(`TestAC323...`), AC-32.4 store and api (`TestAC324...`), AC-32.6 replay and api
(`TestAC326...`), AC-32.8 session, store and api (`TestAC328...`). AC-32.2 is the
web Playwright spec; AC-32.5 and AC-32.7 are in `NOT-IMPLEMENTED.md`.
