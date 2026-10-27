# costacct

Cost accounting for Passage (PRD 7.38, ticket PSG-38, wave 38, slices 1 and 2). The service keeps cost objects and cost
elements, mirrors primary costs from journal lines into cost entries, runs reciprocal assessment allocations
that conserve cost to the cent, distributes aggregated amounts top-down and settles internal orders.

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

## What slice 2 adds

- **Top-down distribution (FR-38.19, BR-38.13, AC-38.11)**: `POST /v1/controlling/top-down-runs` takes an amount
  booked on an aggregated cost object (for example freight on the entity's cost center) and a reference basis per
  segment (for example revenue of a reference period). Each share is rounded half away from zero to the currency's
  minor unit and the rounding residual goes to the segment with the largest basis (the first on a tie), so the
  shares sum to the amount exactly. The source is credited and the segments debited on the same cost element, so
  the run nets to zero and the GL reconciliation of primary costs is unchanged. The amount may not exceed what is
  booked on the source for the element and period (`CST-0012`); the source row is locked while it is read. Zero-basis
  segments receive nothing and are listed in `excluded`. One completed run per source, element and period.
- **Internal order settlement (FR-38.9, BR-38.9)**: `POST /v1/controlling/internal-orders/{id}:settle` moves part or
  all of an internal order's collected cost for a period to a receiver cost object on a secondary (settlement)
  element. Collected cost is the net of the order's entries for the period and currency, so earlier settlements
  already reduce it. The order row is locked while collected cost is read, so concurrent settlements cannot together
  exceed it; a settlement above it is `CO-0618`. Each settlement records the collected cost before it.

Costs are `numeric(19,4)` with a currency. Commands require `Idempotency-Key` and write outbox events
(`dev.passage.controlling.costentry.created.v1`, `dev.passage.controlling.allocation.completed.v1`; a top-down run
emits `allocation.completed` with method `TopDown`).

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
PASSAGE_TEST_DATABASE_URL=postgres://... go test ./...   # runs the store tests against Postgres
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
| POST | `/v1/controlling/top-down-runs` | `controlling.run` |
| GET | `/v1/controlling/top-down-runs?legalEntityId=`, `/v1/controlling/top-down-runs/{id}` | `controlling.view` |
| POST | `/v1/controlling/internal-orders/{id}:settle` | `controlling.run` |
| GET | `/v1/controlling/internal-orders/{id}/settlements?period=` | `controlling.view` |

The PRD's `CostAccountingView`, `CostAccountingMaintain` and `CostAccountingRun` duties are named in the repository's
dotted style. Lists return `{"items": [...]}`, at most 500 rows.

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

## Error codes

| Code | Meaning |
|---|---|
| `CST-0001` | Cost object not found |
| `CST-0002` | The period already has a completed allocation run, or the source and element a completed top-down run |
| `CST-0003` | Duplicate cost object or element code |
| `CST-0004` | No primary element maps a journal line's main account |
| `CST-0005` | The cycle is closed: a sender reaches no final receiver (CO-0611) |
| `CST-0006` | Allocation totals differ; the run rolled back (CO-0612) |
| `CST-0007` | Cost element not found (settlement: not a secondary element of the order's legal entity) |
| `CST-0008` | Allocation input is invalid |
| `CST-0009` | Primary account range overlaps an existing primary element |
| `CST-0010` | Allocation run not found |
| `CST-0011` | Top-down run not found |
| `CST-0012` | Top-down amount exceeds what is booked on the source for the element and period |
| `CST-0013` | Only an internal order can be settled |
| `CO-0618` | Settlement exceeds collected cost (BR-38.9) |
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
