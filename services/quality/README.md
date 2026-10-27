# quality

Passage quality management service. Wave 33 (ticket PSG-33), slice 1.

## What it does (slice 1)

- **Sampling (FR-33.2, BR-33.1):** fixed, percent and ANSI/ASQ Z1.4 AQL sizing at
  general inspection level II, normal inspection. Sample quantity is capped at lot
  quantity. Switching (BR-33.2) is a pure state machine per (item, vendor, test group).
- **Quality associations (FR-33.3, BR-33.3):** specificity score
  `4·item + 2·party + 1·site` (item group counts as item weight 3); highest score wins,
  ties resolved by lowest `sequence`.
- **Tests and test groups (FR-33.1):** variable tests (min/max limits, 0–6 decimals,
  inclusive comparison after half-up rounding) and attribute tests (passing outcomes);
  test group versions are immutable.
- **Instruments (BR-33.8):** `due_at = last_calibrated_at + interval`; results from an
  overdue instrument are refused with `QM-0102`. Lists show `calibrationStatus`
  (`DUE_SOON` within 14 days, the QM-0103 horizon); `:calibrate` records a calibration.
- **Quality orders and results (FR-33.4, BR-33.4):** order state machine, batch result
  recording (up to 1,000 results per request), retest numbering (retest n needs n-1);
  a pending order takes only retests. One order per source line and trigger (BR-33.3).
  The order view carries its test group version, the evaluation (defective samples
  against Ac, required tests still missing) and, once closed, its decision.
- **Variable evaluation (BR-33.6):** half-up rounding to the declared decimals, half
  away from zero (`-1.25` → `-1.3`), then inclusive limits.
- **Usage decision (FR-33.6, BR-33.7):** quantities of the decision lines must sum to
  lot quantity; accept, reject, accept-with-deviation (reason required), rework,
  return-to-vendor, scrap.
- **Events** written to the outbox in the same transaction as the state change.

Code layout: `internal/domain` (pure rules and unit/property tests),
`internal/store` (pgx, RLS-scoped transactions, outbox), `internal/api` (HTTP),
`internal/events` (event names and payloads), `migrations/` (`0001_init.sql`).

## Run

```
PASSAGE_ENV=dev SERVICE_NAME=quality DATABASE_URL=postgres://... \
  go run ./cmd/quality
```

Config is read from the environment; `DATABASE_URL` is required. Health: `/healthz`,
`/readyz`; metrics on the admin port (`/metrics`).

## Test

```
cd services/quality
go vet ./... && go test ./...
```

Domain tests run without a database. Store and API tests use `platform/go/testkit` and
are skipped unless `PASSAGE_TEST_DATABASE_URL` is set.

## API

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/quality/sampling-plans:size` | QM_VIEW |
| GET | `/v1/quality/test-groups` | QM_VIEW |
| GET | `/v1/quality/test-groups/{id}` | QM_VIEW |
| POST | `/v1/quality/test-groups` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/instruments` | QM_VIEW |
| POST | `/v1/quality/instruments` | QM_SETUP_MAINTAIN |
| POST | `/v1/quality/instruments/{id}:calibrate` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/associations` | QM_VIEW |
| POST | `/v1/quality/associations` | QM_SETUP_MAINTAIN |
| GET | `/v1/quality/quality-orders?status=&siteId=&limit=` | QM_VIEW |
| POST | `/v1/quality/quality-orders` | QM_ORDER_MAINTAIN |
| GET | `/v1/quality/quality-orders/{id}` | QM_VIEW |
| POST | `/v1/quality/quality-orders/{id}/results` | QM_RESULTS_RECORD |
| POST | `/v1/quality/quality-orders/{id}:usage-decision` | QM_USAGE_DECISION |

The PRD writes paths as `/quality/v1/...`; the service follows the repository
convention `/v1/<context>/...`. Lists answer `{"items": [...]}` with `limit` 1 to 500
(default 100). Creating and posting calls require `Idempotency-Key`; `:calibrate` is
idempotent by value. The usage decision requires
`If-Match` carrying the order's ETag (428 when absent, 412 when stale). Duties are checked
through the platform `authz` package; they are not read from the token.

## Error codes

Problems are RFC 9457 (`application/problem+json`) with `code` set to the stable value.

| Code | HTTP | Meaning |
|---|---|---|
| COM-0001 | 400 | Validation failed; `errors` names the fields |
| COM-0428 | 428 | `If-Match` required on the usage decision |
| AUTH-0001 | 401 | Missing or malformed tenant |
| QM-0100 | 422 | No quality association matches the trigger |
| QM-0102 | 422 | `INSTRUMENT_OVERDUE`: instrument past its calibration due date |
| QM-0104 | 422 | Decision quantities do not sum to lot quantity |
| QM-0105 | 422 | Required test has no result |
| QM-0109 | 409 | Order is closed; amend through a nonconformance |
| QM-0111 | 409 | Result already recorded for this test, sample and retest |
| QM-0113 | 422 | Result references an unknown instrument |
| QM-0114 | 422 | Z1.4 table not loaded for the requested level |
| QM-0115 | 409 | Order status does not allow the operation (first results on a pending order, decision before pending) |
| QM-0116 | 409 | A quality order already exists for this source line and trigger (BR-33.3) |
| QM-0140 | 404 | Test group not found |
| QM-0141 | 404 | Quality order not found |
| QM-0142 | 404 | Instrument not found |
| QM-0151 | 409 | Instrument code already exists |
| QM-0152 | 409 | Concurrent test group version; retry |
| conflict_version | 412 | Stale `If-Match` |
| idempotency_key_reuse | 422 | Decision `Idempotency-Key` already used on another order |
| AUTH-0003 | 403 | Missing duty |

## Wave reference

Wave 33, PRD section 6.33. Slice 1 scope and deferred items are listed in
`NOT-IMPLEMENTED.md`. The web workspace is `apps/web/src/modules/quality`.

Tenant isolation: every table forces RLS with a tenant policy, and every read and
guarded update also repeats the tenant predicate, so a connection that bypasses RLS
still sees one tenant (`TestPostgres_RowLevelSecurity`).
