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

- A session silent past the idle timeout is replaceable: the next user's login ends it as TAKEOVER and releases its work (AC-32.8). A session idle past the timeout (swept to STALE or not) that comes back, by heartbeat or login, keeps its work only if the warehouse confirms it is still OPEN or ASSIGNED and no other ACTIVE session holds it. When the warehouse cannot be reached the session keeps its work; the response says `relocked` and `reason`. A session idle past the timeout cannot submit a step or take work (409 `MOB-SESSION-STALE`, refused before any warehouse call) until it has beaten; the session version moves only when its status does.
- A keyed command claims its Idempotency-Key in a short transaction, runs holding no database connection, then stores its answer. A repeat while it runs is 409 `MOB-IN-PROGRESS` with `Retry-After`; a claim left by a crash is retryable after 60 s; a failed command releases its key. The claim carries an owner token, so a runner whose claim was retaken can neither store over nor delete it. A repeat of a key with another body is 422 `idempotency_key_reuse`, also when answered from the saved run. The saved run also keeps the last command's key and answer, so a retry after the key's own record was lost repeats that answer instead of 409 `MOB-STEP`.
- A published flow version never changes; runs stay pinned to theirs. An invalid graph is 422 MOB-0113; a stale `If-Match` is 412.
- Offline replay needs the caller's open session on the device (`sessionId`, or the caller's only open session there when omitted); another user's or an unknown device is 403 MOB-NOT-OWNER.
- Offline transactions carry `device_id:seq`. They apply once, in order. The first conflict parks (MOB-0107) and blocks the device's later transactions until a supervisor resolves it.
- Replay applies each transaction as its own: a command whose warehouse operation the caller lacks the duty for is REJECTED (`AUTH-0003` in its reason) and the rest of the batch still applies; a rejection never blocks later transactions, an unresolved conflict does.
- Offline queue ownership: the device tags each queued entry with `userId`, the user who captured it. After a takeover, an entry whose tag is not the signed-in user, or whose key another user recorded, is refused per transaction as `NOT_OWNER` (`MOB-NOT-OWNER`, 403 in the single form) and is never applied under the new user's identity. A pending entry of a user who never returns holds the device's queue; a supervisor with `warehouse.offline-conflict.resolve` closes it with `POST /offline/conflicts/{device:seq}:resolve` action `reject` (it is not listed in the conflict queue, so the key comes from the device). Client contract: on `MOB-NOT-OWNER` keep the queue, sign in again as the user who queued it (possible once the device's session is idle or ended), and resend.
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

`mobile` may call the warehouse with `warehouse.work.execute`,
`warehouse.inquiry.view` and `warehouse.receipt.register`
(`platform/go/tenant/service.go` and `policies/authz/data.json`). The service
token carries the grant's duties, never the user's, and the warehouse trusts
them, so the client itself checks that the signed-in user holds the duty of each
operation (`work.execute` for assign, confirm and exception, `receipt.register`
for receive) before it calls, on the step path and on replay, and fails closed
without a principal; a user without the duty gets 403 `AUTH-0003`. The one
exception is `Client.ReadWork`, the work read behind a session re-lock: the
service's own bookkeeping, whose body never reaches the user.

## Events

`flow.published`, `session.started`, `session.ended`, `offline.conflict`,
`offline.resolved` (outbox, source `mobile`).

## Tests

```
PASSAGE_TEST_DATABASE_URL=postgres://localhost:5432/dbxt \
PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app:passage_app@localhost:5432/dbxt \
go test ./...
```

Acceptance map: AC-32.1 `gs1` (`TestAC321...`), AC-32.3 `internal/replay` (`replay_test.go`)
(`TestAC323...`), AC-32.4 store and api (`TestAC324...`), AC-32.6 replay and api
(`TestAC326...`), AC-32.8 session, store and api (`TestAC328...`). AC-32.2 is the
web Playwright spec; AC-32.5 and AC-32.7 are in `NOT-IMPLEMENTED.md`.
