# compliance (PSG-48, wave 48, slice 1)

Compliance controls for Passage. Slice 1 ships segregation of duties (SoD): the rules engine, pre-save
simulation, evaluation of a user's effective roles with stored conflicts, and the mitigation and
risk-acceptance workflow, plus the compliance workspace screens in `apps/web/src/modules/compliance`.

## What it does now

- `internal/domain/sod.go` (pure): rule schema validation, the 11 seed rules of FR-48.4, `Detect`
  (duty patterns such as `*Post*`, distinct duties, retired rules ignored), `Simulate` and the BR-48.1
  conflict state machine: `Mitigate` (approver is not the conflicted user, reason and control required,
  expiry after now and within min(rule cap, tenant cap)), `Expire`, `Resolve`, `AcceptRisk` (OPEN LOW
  and MEDIUM only, with a reason).
- `internal/store` (Postgres, RLS through `dbx.WithTenantTx`): `sod_conflict` and the append-only
  `sod_mitigation` approval record. Every read and write first reopens mitigations whose expiry has
  passed (MITIGATED → OPEN). Events go to the outbox in the same transaction.
- HTTP (all under the gateway's `/api` prefix):

| Method and path | Duty | What |
|---|---|---|
| `GET /v1/sod/rules` | SoDReview | the seed rules |
| `POST /v1/sod/simulate` | SecurityAdmin | conflicts an assignment would add; `blocked` with `SOD-1101` when the resulting roles hold a HIGH conflict the service has not recorded as MITIGATED (new or existing, AC-48.3) and `sod.block_high` is on |
| `POST /v1/sod/evaluations` | SecurityAdmin | records the outcome for a user's effective roles: new conflicts OPEN (`sod.conflict.detected`), RESOLVED ones detected again reopened, conflicts no longer detected RESOLVED; repeating it changes nothing |
| `GET /v1/sod/conflicts` | SoDReview | filter by state, severity, user, rule; keyset paging with `cursor` |
| `GET /v1/sod/conflicts/{id}` | SoDReview | conflict with its mitigation history; `ETag` is the version |
| `POST /v1/sod/conflicts/{id}/mitigations` | SoDApprove | `If-Match` and `Idempotency-Key` required; the caller is the approver; emits `sod.mitigation.approved` |
| `POST /v1/sod/conflicts/{id}/accept-risk` | SoDAdmin | `If-Match` required; LOW and MEDIUM only |

- Events (`events/asyncapi.yaml`): `dev.passage.sod.conflict.detected.v1`, `dev.passage.sod.mitigation.approved.v1`.
- `GET /readyz` pings Postgres; admin `/metrics` counts simulations and blocked simulations.

Simulation reads the user's recorded conflict states; a caller cannot claim a mitigation in the request
(the draft's `mitigatedRuleIds` field is gone and an unknown field is a 400).

## Run and test

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/compliance
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://admin... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app... go test ./...
```

Without `PASSAGE_TEST_DATABASE_URL` the store and migration tests skip. With it they apply the migrations,
assert RLS on every tenant table, and, with `PASSAGE_TEST_APP_DATABASE_URL`, run the store as
`passage_app` (NOBYPASSRLS) on the real grants of `0003_grants.sql`, probe cross-tenant isolation and
check the append-only tables refuse UPDATE and DELETE.

Fixture `testdata/sod-50.json` (fx-sod-50) plants 50 users across the seed rules.
`TestDetect_PlantedFixtureNoFalseNegatives` asserts every planted conflict is detected and nothing else
fires; `TestEvaluate_PlantedFiftyReconcileWithRecomputation` stores them and reconciles the OPEN count and
the `conflict.detected` events with an independent recomputation (PRD §9.48.9). A rapid property test
asserts a role set holding both duties of any seed rule is never missed.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | required | Postgres (`passage_app` role, RLS enforced) |
| `MIGRATE_ON_START` | unset | `true` applies migrations at start (dev only) |
| `SOD_BLOCK_HIGH` | `true` | tenant policy `sod.block_high`; `false` reports without blocking |
| `SOD_MAX_MITIGATION_DAYS` | `365` | tenant policy `sod.max_mitigation_days`, 1..730 |
| `HTTP_ADDR` / `ADMIN_ADDR` | `:8080` / `:9090` | listeners |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `AUTH-0001` | 401 | missing or malformed tenant |
| `AUTH-0003` | 403 | missing duty |
| `COM-0001` | 400 | malformed JSON body or unknown field |
| `COM-0002` | 400 | request failed validation (`errors` lists fields) |
| `SOD-1101` | 200 (`code` in the simulate body) | assignment blocked by an unmitigated HIGH conflict |
| `SOD-1102` | 422 | self-approval of a mitigation refused |
| `SOD-1103` | 422 | rule does not allow mitigation |
| `SOD-1104` | 422 | conflict not in the required state |
| `SOD-1105` | 422 | reason, control or approver missing |
| `SOD-1106` | 422 | mitigation expiry not in the future or past the cap |
| `SOD-1107` | 422 | risk acceptance on a HIGH conflict |
| `SOD-1108` | 404 | no conflict with that id |
| `SOD-1109` | 412 / 428 | stale `If-Match` (412) or missing `If-Match` (428) |
| `SOD-1110` | 422 | `Idempotency-Key` reused with a different request |
| `SOD-1111` | 422 | the conflict's rule is no longer in the rule set |
| `COM-0500` | 500 | internal error |

## Deploy

`charts/compliance` (`helm lint` clean): Deployment, Service, optional ServiceMonitor; `DATABASE_URL`
from the `passage-compliance-db` secret; `SOD_BLOCK_HIGH` and `SOD_MAX_MITIGATION_DAYS` from values.
`Dockerfile` builds from the repository root.

## Wave reference

PSG-48: FR-48.4, FR-48.5 and BR-48.1 for the seed rule set; AC2, AC-48.2, AC-48.3, AC-48.4. Everything
else in the ticket is listed in `NOT-IMPLEMENTED.md`.
