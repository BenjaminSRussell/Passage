# processmfg

Process manufacturing for Passage (wave 24, PSG-24, slice 1). Formulas, batch orders created
from a formula snapshot and scaled to the batch size, rescale before the order starts, and
the potency, catch-weight and co-product allocation rules.

Slice 1 persists formulas and batch orders (Postgres, RLS on every table, outbox events in
the same transaction). Batch attributes, disposition, co-product allocation on an order and
the other wave-24 process entities are later slices; see `NOT-IMPLEMENTED.md`.

## Layout

```
cmd/processmfg        entrypoint: dbx.Connect, optional dbx.Migrate, httpx router, serve
internal/domain       pure rules (no I/O): scale.go (FR-24.1 validation, BR-24.1 scaling),
                      potency.go (BR-24.2), coproduct.go (BR-24.3), catchweight.go (BR-24.5),
                      shelflife.go (FR-24.6, BR-24.4, BR-24.11); property tests use rapid
internal/store        Postgres repository: formulas, batch orders, rescale, idempotency, outbox
internal/api          HTTP handlers: decode → validate → tenant → store/domain → JSON/problem
internal/events       event types and payloads
migrations/           0001_init.sql (formula, formula_line, batch_order_sequence, batch_order,
                      batch_order_line, idempotency_keys, outbox), RLS on every table
testdata/             PRD 4.24.9 fixtures: pmf-potency-20, pmf-coby-12, pmf-catchweight-10
api/openapi.yaml      OpenAPI 3.1, checked against the router by internal/api/openapi_test.go
events/asyncapi.yaml  AsyncAPI 3, checked against internal/events by events_test.go
charts/processmfg     Helm chart (helm lint)
```

## Run

```
DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/processmfg
```

`DATABASE_URL` is required. `HTTP_ADDR` defaults to `:8080`. The gateway sets
`X-Passage-Tenant`, `X-Passage-User`, `X-Passage-Duties` and `X-Passage-Legal-Entity`.

## Test

```
go vet ./... && go test ./...
PASSAGE_TEST_DATABASE_URL=postgres://owner@... PASSAGE_TEST_APP_DATABASE_URL=postgres://passage_app@... go test ./...
```

Store tests use `testkit.DBWithApp`: migrations run as the owner, every query as the
`passage_app` role (no BYPASSRLS), so tenant and legal-entity isolation is exercised. They are
skipped when `PASSAGE_TEST_DATABASE_URL` is unset.

- AC-24.1 scaling of a 1,000 kg formula to 2,500 kg (domain, API and store), a property test
  that percent-controlled lines sum exactly to the batch size, and the order snapshot.
- AC-24.2 and `pmf-potency-20`: 20 potency rows, one of which must be rejected (PMF-0002).
- AC-24.3 and BR-24.3: `pmf-coby-12` (4 methods × by-product cost types) and a rapid property
  that the allocation sums to the batch cost exactly.
- BR-24.5: `pmf-catchweight-10` bounds.
- AC-24.5 catch weight. The fixture uses a per-unit minimum of 9.8 kg; see NOT-IMPLEMENTED.md.
- AC-24.9 a percent-controlled formula summing to 99.5% is refused (PMF-0001).
- BR-24.11 weighted average (43.0000%).

## Endpoints

| Method | Path | Duty | Purpose |
|---|---|---|---|
| POST | `/v1/production/formulas` | `production.formula.write` | Create a formula with its lines (FR-24.1); Idempotency-Key |
| GET | `/v1/production/formulas` | `production.batch-order.read` | Formulas by code |
| GET | `/v1/production/formulas/{id}` | `production.batch-order.read` | One formula with lines |
| POST | `/v1/production/formulas/{id}:scale` | `production.batch-order.read` | Scale preview, writes nothing (BR-24.1) |
| POST | `/v1/production/batch-orders` | `production.batch-order.write` | Create a batch order from a formula (FR-24.2); Idempotency-Key |
| GET | `/v1/production/batch-orders` | `production.batch-order.read` | Orders, newest first; `status`, `formulaId`, `limit` |
| GET | `/v1/production/batch-orders/{id}` | `production.batch-order.read` | One order with its scaled lines |
| POST | `/v1/production/batch-orders/{id}:rescale` | `production.batch-order.write` | New batch size before Started; `version`; Idempotency-Key |
| POST | `/v1/production/potency:consume` | `production.batch-order.read` | Potency consumption and filler adjustment (BR-24.2) |
| POST | `/v1/production/catch-weight:validate` | `production.batch-order.read` | Catch-weight range check (BR-24.5) |
| POST | `/v1/production/co-products:allocate` | `production.journal.post` | Co-product allocation preview (BR-24.3) |

Duties are enforced with the kernel `authz.RequireDuty`. The OPA entries are a handoff to
`policies/`.

## Events

| Type | When |
|---|---|
| `dev.passage.production.formula.created.v1` | a formula is created |
| `dev.passage.production.batchorder.created.v1` | a batch order is created (PRD `batch-order.created`) |
| `dev.passage.production.batchorder.rescaled.v1` | a batch order's size changes |

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `COM-0001` | 400 | Malformed body or invalid input; field errors in `errors` |
| `PMF-0001` | 422 | Percent-controlled lines do not sum to 100 within ±0.01 (FR-24.1) |
| `PMF-0002` | 422 | Compensating line would become negative; alias `POTENCY_COMPENSATION_NEGATIVE` (BR-24.2) |
| `PMF-0004` | 422 | Catch weight outside min × units to max × units (BR-24.5) |
| `PMF-0021` | 422 | Allocation weights sum to zero, or by-product burden exceeds batch cost (not in the PRD catalog) |
| `PMF-0101` | 404 | Formula not found |
| `PMF-0102` | 404 | Batch order not found |
| `PMF-0103` | 409 | Formula code already used |
| `PMF-0104` | 409 | Stale `version` on rescale |
| `PMF-0105` | 409 | Order has started; only planned or released orders can be rescaled |
| `idempotency_key_reuse` | 422 | Idempotency-Key reused with a different request |
| `idempotency_key_in_flight` | 409 | A concurrent request with the same key committed first; retry for its answer |
| `AUTH-0001` | 401 | No tenant or principal |
| `AUTH-0003` | 403 | Missing duty, or legal entity outside the caller's scope |
| `COM-0500` | 500 | Internal error (detail hidden) |

## Wave reference

PRD section 4.24 (`docs/prd/phase-4-manufacturing.md`), Wave 24. Owned paths:
`services/processmfg/` and `apps/web/src/modules/processmfg/` (screens listed in that module's
README).

## Dependencies

Only the platform module and what it already pulls in: chi, pgx/v5, shopspring/decimal,
pgregory.net/rapid (tests).
