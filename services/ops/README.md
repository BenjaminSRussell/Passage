# ops

Ops control plane, parts 1 and 2 (PSG-50, FR-50.5, FR-50.9, FR-50.12; issue #50 subtask 50-3). It is platform state, not tenant data:
the tables have no `tenant_id` and no RLS (allowlisted in `tools/rlscheck`), and every route needs an operator duty.

| Route | Duty | What it does |
|---|---|---|
| `GET /v1/ops/regions` | `ops.region.view` | regions with role (PRIMARY, PASSIVE, FENCED), lag and the lag limit |
| `PUT /v1/ops/regions/{name}/lag` | `ops.region.report` | the monitoring agent records a region's replication lag |
| `POST /v1/ops/failover` | `ops.failover.execute` | fences the primary and promotes the target in one transaction; `Idempotency-Key` is the event id |
| `GET /v1/ops/failover-requests/{id}` | `ops.region.view` | a failover that accepts data loss, waiting for or past its second approval |
| `POST /v1/ops/failover-requests/{id}:approve` | `ops.failover.execute` | the second operator applies the request; the approver is the verified caller, never the requester |
| `POST /v1/ops/regions/{name}:rejoin` | `ops.failover.execute` | FENCED to PASSIVE, only with `resyncVerified: true` |
| `GET /v1/ops/capacity/estimate` | `ops.region.view` | the capacity model of `docs/ops/capacity-model.md`, optionally priced |
| `GET /v1/ops/failovers/{eventId}` | `ops.region.view` | a failover with the steps recorded so far and whether it is complete |
| `PUT /v1/ops/failovers/{eventId}/steps/{action}` | `ops.failover.execute` | the runner reports a step result (DONE or FAILED); a DONE step is kept, a FAILED one is replaced by a later result |
| `GET /v1/ops/failovers/{eventId}/scorecard` | `ops.region.view` | RTO (fence start to DNS flip end) and RPO (lag at the fence) against `RTO_TARGET_MIN` / `RPO_TARGET_S`, hash chain verified, data loss accepted (AC-50.3) |
| `POST /v1/ops/break-glass`, `GET /v1/ops/break-glass`, `POST .../{id}:revoke`, `POST .../{id}:review` | `ops.breakglass.request`, `ops.region.view`, `ops.breakglass.request`, `ops.breakglass.review` | break-glass for `ops.failover.execute` (below) |
| `PUT /v1/ops/tenants/{id}/license`, `GET /v1/ops/tenants/{id}/license` | `ops.license.install`, `ops.region.view` | install a signed tenant licence under `If-Match`; read it with its VALID, GRACE or EXPIRED state (BR-50.8) |
| `GET /v1/ops/slo?window=24h` | `ops.region.view` | availability and p95 latency per service (Prometheus job) over 1h, 6h, 24h, 7d or 30d, against the targets, with `breachedSince` from the hourly check |
| `POST /v1/ops/tenants`, `GET /v1/ops/tenants`, `GET /v1/ops/tenants/{id}` | `ops.tenant.provision`, `ops.region.view`, `ops.region.view` | provision a tenant (residency check first, `Idempotency-Key`), list and read provisions with their steps |
| `PUT /v1/ops/tenants/{id}/provision-steps/{action}` | `ops.tenant.provision` | the provisioning runner reports realm, databases, kafka-acls, lakehouse or license as DONE or FAILED |
| `PUT /v1/ops/tenants/{id}/costs/{month}`, `GET` the same, `GET /v1/ops/costs?month=` | `ops.cost.record`, `ops.region.view`, `ops.region.view` | record a tenant's metered month (the statement is computed here), read it as JSON or CSV, list a month with the cluster total |
| `PUT /v1/ops/tenants/{id}/license/usage`, `GET /v1/ops/licenses` | `ops.license.report`, `ops.region.view` | the user-count reporter's active users; every licence with its state and counts |
| `GET /healthz/region` | public | 200 while this region is PRIMARY, else 503 `Retry-After: 30`; DNS health checks read it |

## Rules
- BR-50.1: PASSIVE to PRIMARY promotes and the former PRIMARY becomes FENCED in the same transaction; FENCED returns to
  PASSIVE only after a verified re-sync; never two PRIMARY (also a unique partial index on `ops_region`).
- OPS-1701: a target whose lag exceeds `FAILOVER_MAX_LAG_S` (300) is refused unless `acceptDataLoss` is set.
  OPS-1702: then the failover is not applied. The call answers 202 with a failover request (`ops_failover_request`), and a
  second operator approves it with their own call within 5 minutes (OPS-1714 after that). The approver is the verified
  caller of the approve route, never a body field, and never the requester (403 OPS-1702; the table also refuses it).
- Operator tenant only (OPS-1715): the ops routes act on platform-wide state, so they serve principals of
  `OPS_OPERATOR_TENANT_ID` only. The `ops.*` duties are `"operator_only"` in `policies/authz/data.json`: identity refuses
  to grant them in another tenant and leaves them out of other tenants' compiled assignments, and `authz.rego` grants them
  only in the bundle's `operator_tenant_id`.
- A failover is recorded in `ops_failover` with its ordered plan (fence, CNPG promote, MirrorMaker direction, Temporal,
  DNS, ledger hash chain check). A retry with the same event id returns the first outcome.

## Failover execution (part 2)
`passage-ops` (`cmd/passage-ops`, in the image as `/passage-ops`) is the operator's command line:

```
passage-ops failover --to eu-central [--accept-data-loss] [--dry-run] [--event <id>]
passage-ops failover approve --request <id>     # the second operator
passage-ops failover resume --event <id>
passage-ops break-glass open --reason REGION_LOSS --ticket INC-42 [--minutes 60] | list | revoke --id | review --id --outcome
passage-ops lag-report --region eu-central [--once]
```
It needs `OPS_API_URL` (the gateway) and `OPS_TOKEN`. A failover is recorded through the API first (roles flip in one transaction,
OPS-1701/1702 apply), then the ordered steps run with the credentials of the machine and each result is reported back. The
steps are interfaces in `internal/failover` with fakes in the tests: fence (CNPG `cnpg.io/fencedInstances` through the Kubernetes API),
promote (drop `spec.replica`, wait for a healthy primary), MirrorMaker (delete the KafkaMirrorMaker2 in the new primary), Temporal
(`temporal operator namespace update --active-cluster`), DNS (a provider-neutral webhook) and the hash-chain check (a command, for
example `deploy/multi-region/gameday/baseline.py verify`). Every call is idempotent, so a rerun with the same `--event`, or `resume`,
continues at the first step that is not DONE. `--dry-run` plans against the live region table and prints what each step would do.
Environment of the CLI: `OPS_KUBE_REGIONS` (JSON or `@file`: per region `server`, `tokenFile`, `caFile`), `OPS_CNPG_NAMESPACE`,
`OPS_CNPG_CLUSTER`, `OPS_KAFKA_NAMESPACE`, `OPS_TEMPORAL_ADDRESS`, `OPS_TEMPORAL_NAMESPACES`, `OPS_DNS_WEBHOOK_URL`, `OPS_DNS_WEBHOOK_TOKEN`,
`OPS_DNS_RECORD`, `DNS_TTL_S`, `OPS_CHAIN_VERIFY_CMD`.

Fenced region (BR-50.2, OPS-1703): every Go service built on `platform/go/httpx` answers 503 `Retry-After: 30` while its region is FENCED,
except `/healthz`, `/readyz`, `/healthz/region` and `/metrics`. It is off unless `global.regionFenceUrl` (env `PASSAGE_REGION_FENCE_URL`, the ops
`/healthz/region` of this region) or `PASSAGE_REGION_FENCE_FILE` is set; one background poll (default 5 s, `PASSAGE_REGION_FENCE_INTERVAL`) caches the
role, so no request waits on it, and an unreadable source keeps the last state. This service opts out (`httpx.DisableFence`) so it can run the rejoin.
`GET /healthz/region` reaches DNS health checks through the `passage-region` Gateway (`deploy/gateway/region-gateway.yaml`), which has no JWT.

Break-glass: an operator without `ops.failover.execute` but with `ops.breakglass.request` opens a window for themselves (15 to 120 minutes,
reason code, ticket). While it lasts the failover, rejoin, approve and step routes accept them; each use is written to `ops_break_glass_use` and
logged, and the approval rules still apply (the requester never approves their own request). One open window per operator (OPS-1717). After it
ends or is revoked, another operator with `ops.breakglass.review` reviews it (APPROVED or FLAGGED with a note; never the same operator, once);
the review is due 3 days after the window ends and `ops_break_glass_review_overdue` counts late ones. Duties are checked in this service from the
token's duties claim (the gateway only validates the JWT), so the window needs no identity change; the window is not a role assignment and does not show in identity's effective permissions.

Lag trust: a lag reading older than `OPS_LAG_STALE_S` (default 90 s, about three reporting intervals; 0 turns it off) is unknown (`lagStale`,
`lagSeconds` 1073741824) and refuses a failover without `acceptDataLoss`; with `OPS_REQUIRE_LAG_REPORTS=true` (the chart sets it with `lagReporter.enabled`) a
region that never reported is unknown too. A lost old region is failed over with `--accept-data-loss --fence-unreachable "<reason>"` (second operator
approval; the step is recorded `forced`); see the failover runbook.

Lag reporter: `passage-ops lag-report` reads `pg_stat_replication` (mode primary) or the standby's replay position (mode standby) and reports it with
`PUT /v1/ops/regions/{name}/lag`; a measurement that fails reports a value far above the limit (`--unknown-lag-s`, default 86400) so a broken reporter
blocks a failover instead of leaving a stale low number. The chart can run it (`lagReporter.enabled`).

## Part 3: SLO, provisioning, cost, licence checks and events
SLO report: the figures come from the shared HTTP series every service exposes, read from Prometheus (`PROMETHEUS_URL`) per scrape job:
requests and 5xx from `http_requests_total`, p95 from `http_request_duration_seconds`. Targets: `SLO_AVAILABILITY_TARGET` (0.999),
`SLO_LATENCY_P95_MS` (2000) and per service `SLO_LATENCY_P95_BY` (`{"ledger":500}`). Without `PROMETHEUS_URL` the report answers 503 OPS-1725.

Hourly checks (`OPS_CHECK_INTERVAL`, default 1h, every replica; each change is written once under a row lock): the licence check evaluates every
licence with the tenant's last reported full users and writes `license.changed` when the state moves (an install writes it too); the SLO check
(with `PROMETHEUS_URL`) evaluates the last hour and writes `slo.breached` for each service that turned breached (again only after it recovered).

Provisioning: `POST /v1/ops/tenants` checks the residency with `platform/go/residency` (the region must also be one this platform runs; OPS-1705
otherwise), then records the plan of `docs/ops/tenant-provisioning.md`. The runner performs each step and reports it; the license step is DONE
only once the tenant's licence is installed (OPS-1724), and the last DONE step marks the provision PROVISIONED and writes `tenant.provisioned`.
A provision running past 15 minutes is `overdue`.

Cost statements: `OPS_COST_RATES` holds the USD unit rates (`{"cpu_core_hours":"0.04",...,"overhead":"0.12"}`); the statement follows
`docs/ops/tools/cost_meter.py` (one rounding to cents, EUR from the unrounded total at the ECB month average).

Events (outbox, migration 0006, `events/asyncapi.yaml`): `dev.passage.ops.failover.completed.v1` (with the last DONE step of a failover),
`tenant.provisioned.v1`, `slo.breached.v1` and `license.changed.v1`. The platform-wide ones carry the operator tenant; the others the tenant they describe.

Web: the operations console (`apps/web/src/modules/ops`) shows failover status, break-glass windows, service levels and licences.

## Configuration
`DATABASE_URL`, `MIGRATE_ON_START`, `REGION_NAME` (the region this replica runs in), `OPS_REGIONS` (`us-east=PRIMARY,eu-west=PASSIVE`, read
only when the table is empty), `FAILOVER_MAX_LAG_S`, `RTO_TARGET_MIN` (60), `RPO_TARGET_S` (300), `OPS_LICENSE_PUBLIC_KEY` (base64 Ed25519; without it no licence installs), `OPS_OPERATOR_TENANT_ID` (required: identity's `IDENTITY_OPERATOR_TENANT_ID`). Migrations: `0001_init`,
`0002_grants`, `0003_failover_request`, `0004_steps_break_glass`, `0005_license`, `0006_ops_part3`. Part 3: `PROMETHEUS_URL`, `SLO_AVAILABILITY_TARGET`,
`SLO_LATENCY_P95_MS`, `SLO_LATENCY_P95_BY`, `OPS_COST_RATES`, `OPS_CHECK_INTERVAL` (chart `prometheusUrl`, `slo.*`, `costRates`, `checkInterval`).

## Test
`go test ./...`; the store and API tests need `PASSAGE_TEST_DATABASE_URL` (see `.github/postgres-test-setup.sql`) and skip without it.
The capacity tests pin the figures of `docs/ops/tools/capacity_estimate.py`.

Open items are in `NOT-IMPLEMENTED.md`.
