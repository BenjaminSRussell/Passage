# ops

Ops control plane, part 1 (PSG-50, FR-50.5, FR-50.9; issue #50 subtask 50-3). It is platform state, not tenant data:
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

## Configuration
`DATABASE_URL`, `MIGRATE_ON_START`, `REGION_NAME` (the region this replica runs in), `OPS_REGIONS` (`us-east=PRIMARY,eu-west=PASSIVE`, read
only when the table is empty), `FAILOVER_MAX_LAG_S`, `OPS_OPERATOR_TENANT_ID` (required: identity's `IDENTITY_OPERATOR_TENANT_ID`). Migrations: `0001_init`,
`0002_grants`, `0003_failover_request`.

## Test
`go test ./...`; the store and API tests need `PASSAGE_TEST_DATABASE_URL` (see `.github/postgres-test-setup.sql`) and skip without it.
The capacity tests pin the figures of `docs/ops/tools/capacity_estimate.py`.

Open items are in `NOT-IMPLEMENTED.md`.
