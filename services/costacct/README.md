# costacct

Cost accounting for Passage (PRD 7.38, ticket PSG-38, wave 38, slice 1). The service keeps cost objects and cost
elements, mirrors primary costs from journal lines into cost entries, and runs reciprocal assessment allocations
that conserve cost to the cent.

## What slice 1 does

- **Cost objects (FR-38.6)**: cost centers, internal orders, products and projects per legal entity. Codes are
  unique per legal entity.
- **Cost elements (FR-38.6)**: primary elements map an inclusive main account range. Ranges may not overlap another
  primary element in the same legal entity. Secondary elements carry allocated cost.
- **Journal mirroring (FR-38.6)**: each journal line maps to one primary element by main account and writes one
  `Journal` cost entry. A journal line is mirrored at most once (partial unique index), so replays are safe. Every
  cost object on the lines must belong to the request's legal entity (`CST-0001`).
- **Reciprocal allocation (FR-38.7, BR-38.6, BR-38.8)**: one step per run. The cost base of each center is its
  journal cost for the period in the run currency. Basis matrices give the receiver shares. Small cycles (≤ 2,000
  centers) are solved exactly by LU with partial pivoting in decimal arithmetic. Larger cycles use Gauss–Seidel up
  to 100 iterations with a 0.01 residual bound; a solve that stops at the limit does not fail the run. Each sender's
  total is rounded to cents and split by share with the rounding on its largest receiver; whatever a sender still
  holds afterwards (rounding cents, or the iteration residual) is swept to its largest receiver and on to a final
  receiver, so every sender ends at exactly zero and `totalIn` (senders' cost) equals `totalOut` (cost taken by
  final receivers) to the cent, or the run rolls back (`CST-0006`). A closed cycle, where some sender reaches no
  final receiver, is `CST-0005`. Senders with no basis keep their cost and are reported as unallocated. Only one
  completed run exists per legal entity and period.
- **Reconciliation (FR-38.11, AC-38.6)**: `POST /v1/controlling/reconciliations` maps the supplied journal balances
  to primary elements and compares them with Σ primary cost entries per element for the period and currency.
- **Cost object line items**: entries of one object for one period, with origin and behavior.

Costs are `numeric(19,4)` with a currency. Commands require `Idempotency-Key` and write outbox events
(`dev.passage.controlling.costentry.created.v1`, `dev.passage.controlling.allocation.completed.v1`).

## Algorithm note

For a cycle with centers `i`, costs `c` and fractional shares `S` (row `j` sums to 1 over receivers), the total cost
each center carries is `x = c + Sᵀx`. The solver returns `x`. Flows are `x_j × S[j][i]` rounded to cents. The
convergence test is the fixed-point residual `max |x_i − (c_i + Σ_j S[j][i] x_j)|`. Because the error is about the
residual divided by `1 − ρ`, a residual of 0.01 can leave a few cents of drift on large totals; the residual sweep
moves that drift to the largest receivers, so conservation holds on both paths and `swept` reports how much moved.
A closed cycle with no leakage is singular (`CST-0005`) and is detected from the share graph before solving.

## Run and test

```sh
cd services/costacct
go test ./...                      # domain and API tests; store tests skip without a database
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store reconciliation test
DATABASE_URL=postgres://... MIGRATE_ON_START=true HTTP_ADDR=:8080 go run ./cmd/costacct
```

Health: `GET /healthz`, `GET /readyz` (provided by `httpx.NewRouter`).

## Routes

| Method | Path | Duty |
|---|---|---|
| POST | `/v1/controlling/cost-objects` | `controlling.maintain` |
| GET | `/v1/controlling/cost-objects?legalEntityId=&kind=`, `/v1/controlling/cost-objects/{id}` | `controlling.view` |
| POST | `/v1/controlling/cost-elements` | `controlling.maintain` |
| GET | `/v1/controlling/cost-elements?legalEntityId=` | `controlling.view` |
| POST | `/v1/controlling/cost-entries:mirror` | `controlling.maintain` |
| POST | `/v1/controlling/allocation-runs` | `controlling.run` |
| GET | `/v1/controlling/allocation-runs?legalEntityId=`, `/v1/controlling/allocation-runs/{id}` | `controlling.view` |
| GET | `/v1/controlling/cost-objects/{id}/entries?period=` | `controlling.view` |
| POST | `/v1/controlling/reconciliations` | `controlling.view` (read-only, no Idempotency-Key) |

The PRD's `CostAccountingView`, `CostAccountingMaintain` and `CostAccountingRun` duties are named in the repository's
dotted style. Lists return `{"items": [...]}`, at most 500 rows.

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

## Error codes

| Code | Meaning |
|---|---|
| `CST-0001` | Cost object not found |
| `CST-0002` | The period already has a completed allocation run |
| `CST-0003` | Duplicate cost object or element code |
| `CST-0004` | No primary element maps a journal line's main account |
| `CST-0005` | The cycle is closed: a sender reaches no final receiver (CO-0611) |
| `CST-0006` | Allocation totals differ; the run rolled back (CO-0612) |
| `CST-0007` | Secondary element not found |
| `CST-0008` | Allocation input is invalid |
| `CST-0009` | Primary account range overlaps an existing primary element |
| `CST-0010` | Allocation run not found |
| `COM-0001` | Malformed body or request fields fail validation (400) |
| `idempotency_key_reuse` | Same Idempotency-Key with a different body (422) |
| `idempotency_in_progress` | A request with this key is still running (409) |
| `AUTH-0001` | No principal on the request |
| `AUTH-0003` | Missing duty |

## Dependencies

Go only: the platform kernel and the libraries already in its module graph (chi, pgx/v5, shopspring/decimal,
rapid for property tests). No new dependency needs a justification.

## Wave reference

Wave 38 of PSG-38. Remaining scope is listed in `NOT-IMPLEMENTED.md`.
