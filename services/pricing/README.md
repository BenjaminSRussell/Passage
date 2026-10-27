# pricing (wave 26, PSG-26)

Deterministic pricing engine for Passage: resolves net prices and discounts for a
customer / product / date / quantity / currency tuple from condition records, and returns
the ordered trace of conditions that produced the price. Ticket PSG-26, PRD §5.26.

## Slice 1 status

Built in this slice:

- **Condition records** (FR-26.2): create (DRAFT), read, list, activate and withdraw, with
  the overlap check (BR-26.6, 409 PRC-0002 naming the conflicting id) and an outbox event in
  the same transaction. Create, activate and withdraw honour `Idempotency-Key` (a retry
  replays the first response); activate and withdraw check the body `version` (412).
  Store: `internal/store`, migration `0001_init.sql` with RLS (tenant and legal-entity scope).
- **Resolve with explain** (FR-26.3, FR-26.4, FR-26.5 for LINE_DISC and TOTAL_DISC, FR-26.7 unit
  rounding): `internal/domain/resolve.go`. Access sequence for PRICE (BR-26.1 steps 1–6), with
  `search_next` and `exclusive`. Explain trace is content-addressed (sha256).
- **Simulation** (FR-26.13, first half): `POST /v1/pricing/simulate` prices inline records with
  no storage.
- **Pure calculation helpers** that later slices wire up: `SmartRound` (FR-26.7 journal bands),
  `RebateAccrual` (FR-26.11 and FR-26.15 tier math), `CommissionAmount` and `SplitCommission`
  (FR-26.18, BR-26.12 split and residual cent), `FreeGoods` (FR-26.22, BR-26.13).
- **Fixtures**: `testdata/pricing-scenarios.json` (19 hand-worked cases covering AC-26.1, 26.2,
  26.3, 26.5, BR-26.1, BR-26.3, BR-26.10 and PRC-0001), run by `internal/domain/scenarios_test.go`.
  `property_test.go` resolves 10,000 seeded random requests and checks byte-identical output (AC2).
- **Contract tests**: `internal/api/contract_test.go` (router = OpenAPI paths) and
  `internal/events/events_test.go` (event types and payload fields = AsyncAPI).
- **Web module**: `apps/web/src/modules/pricing` (workspace tiles, trade agreements grid,
  create/copy form, detail with activate and withdraw, simulation / explain), all on the API.
- **Chart**: `charts/pricing` (Deployment, Service, HPA 70% CPU, ServiceMonitor toggle).

Not yet built: see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/pricing/            entrypoint (dbx.Connect -> dbx.Migrate -> httpx.NewRouter -> Serve)
internal/domain/        pure engine: types, resolve, calc, smart rounding, rebate, commission, free goods
internal/store/         Postgres repository (pgx, tenant-scoped transactions, outbox)
internal/api/           HTTP handlers and validation
internal/events/        event type constants and payloads
migrations/             0001_init.sql (condition_record, outbox, idempotency_keys, RLS)
api/openapi.yaml        OpenAPI 3.1 for every endpoint
events/asyncapi.yaml    AsyncAPI 3 for every published event
charts/pricing/         Helm chart
testdata/               fixtures
```

## Endpoints (served under `/v1/pricing`; the browser uses `/api/v1/pricing` via the BFF)

| Method | Path | Duty | Notes |
|---|---|---|---|
| POST | `/v1/pricing/resolve?explain=true` | pricing.view | up to `PRICING_MAX_LINES` (500) lines; the PRD's PricingResolve duty maps here until w27 |
| POST | `/v1/pricing/simulate?explain=true` | pricing.view | inline records, nothing stored |
| GET | `/v1/pricing/condition-records?legalEntityId=&status=&type=&customer=&product=&asOf=&limit=` | pricing.view | at most 100 rows, ordered by id |
| POST | `/v1/pricing/condition-records` | pricing.maintain | `Idempotency-Key` honoured; creates DRAFT |
| GET | `/v1/pricing/condition-records/{id}` | pricing.view | |
| POST | `/v1/pricing/condition-records/{id}:activate` | pricing.maintain | body `{version}`; overlap check; `Idempotency-Key` honoured |
| POST | `/v1/pricing/condition-records/{id}:withdraw` | pricing.maintain | body `{version, reason}`; `Idempotency-Key` honoured |
| GET | `/healthz`, `/readyz`, `/metrics` | none | `/metrics` on the admin port |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| PRC-0001 | 200 (line) / 422 | No price for a product on the pricing date (reported on the line) |
| PRC-0002 | 409 | Overlapping ACTIVE record for the same key, currency and unit; names the id |
| PRC-0003 | 422 | Negative amount (BR-26.10) |
| PRC-0009 | 422 | Request exceeds `PRICING_MAX_LINES` |
| PRC-0030 | 404 | Condition record not found (store) |
| PRC-0031 | 409 | Invalid state transition (store) |
| conflict_version | 412 | Body version does not match the stored version |
| idempotency_key_reuse | 422 | Same `Idempotency-Key` with a different body |
| COM-0001 | 422 | Validation failure; `errors` maps each field to its message (percentages outside 0–100, a product-keyed TOTAL_DISC, bad dates, ...) |
| AUTH-0001 | 401 | Missing or malformed tenant |
| AUTH-0003 | 403 | Missing duty (kernel `authz`) |

## Events

Written to `outbox` in the same transaction as the change (see `events/asyncapi.yaml`):

- `dev.passage.pricing.conditionrecord.activated.v1`
- `dev.passage.pricing.conditionrecord.withdrawn.v1`

The entity segment is `conditionrecord` (as in `internal/events`); the PRD writes
`condition-record`. Naming is a handoff to the kernel owner (see NOT-IMPLEMENTED.md).

## Configuration

12-factor env vars, validated at startup: `PASSAGE_ENV`, `SERVICE_NAME`, `HTTP_ADDR`,
`ADMIN_ADDR`, `DATABASE_URL`, `PRICING_MAX_LINES` (500), `PRICING_EXPLAIN_MAX_BYTES` (65536).
`PRICING_CACHE_TTL` is set by the chart but is not read yet (no cache in this slice).

## Run and test

```
cd services/pricing
go test ./...                      # runs without Postgres; store/API DB tests skip
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs store and API DB tests
go vet ./...
```

Domain tests are pure table-driven unit tests plus property tests with `pgregory.net/rapid`.
Dependencies beyond the platform module: `github.com/shopspring/decimal` (money arithmetic,
already in the platform's dependency set) and `pgregory.net/rapid` (test only).

## Wave reference

PRD §5.26 (functional requirements FR-26.1–26.23, rules BR-26.1–26.15, AC-26.1–26.14).
Depends on waves 6 (units), 7 (customers and groups), 8 (products and variants), 11
(journal), 12 (currencies), 25 (phase order). Consumed by 27 (sales orders, free goods lines),
19 (AP claims), 30 (rebate settlement), 43 (payroll commission export).
