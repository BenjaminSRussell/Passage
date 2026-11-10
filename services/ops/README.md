# ops

Ops control plane, part 1 (PSG-50, FR-50.5, FR-50.9; issue #50 subtask 50-3). It is platform state, not tenant data:
the tables have no `tenant_id` and no RLS (allowlisted in `tools/rlscheck`), and every route needs an operator duty.

| Route | Duty | What it does |
|---|---|---|
| `GET /v1/ops/regions` | `ops.region.view` | regions with role (PRIMARY, PASSIVE, FENCED), lag and the lag limit |
| `PUT /v1/ops/regions/{name}/lag` | `ops.region.report` | the monitoring agent records a region's replication lag |
| `POST /v1/ops/failover` | `ops.failover.execute` | fences the primary and promotes the target in one transaction; `Idempotency-Key` is the event id |
| `POST /v1/ops/regions/{name}:rejoin` | `ops.failover.execute` | FENCED to PASSIVE, only with `resyncVerified: true` |
| `GET /v1/ops/capacity/estimate` | `ops.region.view` | the capacity model of `docs/ops/capacity-model.md`, optionally priced |
| `GET /healthz/region` | public | 200 while this region is PRIMARY, else 503 `Retry-After: 30`; DNS health checks read it |

## Rules
- BR-50.1: PASSIVE to PRIMARY promotes and the former PRIMARY becomes FENCED in the same transaction; FENCED returns to
  PASSIVE only after a verified re-sync; never two PRIMARY (also a unique partial index on `ops_region`).
- OPS-1701: a target whose lag exceeds `FAILOVER_MAX_LAG_S` (300) is refused unless `acceptDataLoss` is set.
  OPS-1702: that needs `approvedBy`, a second PlatformAdmin other than the caller.
- A failover is recorded in `ops_failover` with its ordered plan (fence, CNPG promote, MirrorMaker direction, Temporal,
  DNS, ledger hash chain check). A retry with the same event id returns the first outcome.

## Configuration
`DATABASE_URL`, `MIGRATE_ON_START`, `REGION_NAME` (the region this replica runs in), `OPS_REGIONS` (`us-east=PRIMARY,eu-west=PASSIVE`, read
only when the table is empty), `FAILOVER_MAX_LAG_S`. Migrations: `0001_init`, `0002_grants`.

## Test
`go test ./...`; the store and API tests need `PASSAGE_TEST_DATABASE_URL` (see `.github/postgres-test-setup.sql`) and skip without it.
The capacity tests pin the figures of `docs/ops/tools/capacity_estimate.py`.

Open items are in `NOT-IMPLEMENTED.md`.
