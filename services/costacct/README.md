# costacct

Cost accounting for Passage (PRD 7.38, ticket PSG-38, wave 38, slices 1 to 5). The service keeps cost objects and cost
elements, mirrors primary costs from journal lines into cost entries, runs reciprocal assessment allocations
that conserve cost to the cent, distributes aggregated amounts top-down, settles internal orders, runs template
(driver-formula) allocations over statistical measures, reruns allocations by reversal, classifies cost as fixed
or variable, and collects and distributes cost through cost object hierarchies.

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
  completed run exists per legal entity and period (a second request is a rerun, slice 4).
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

## What slice 3 adds

- **Statistical measures (FR-38.17 inputs, PRD 7.38.5)**: measures per legal entity (code, name, unit) and their
  values per cost object and period, loaded as upserts (`POST /statistical-measures/{id}/values`). Values are
  non-negative with up to 4 decimals.
- **Template (driver-formula) allocation (FR-38.17, BR-38.12, AC-38.14)**: a template names a sender cost object, a
  currency and lines of (receiver, secondary element, formula). The formula grammar is restricted: numbers,
  `+ - * /` (and `× ÷ −`), `min(a, b)`, `max(a, b)`, `if(cond, a, b)` with `< <= > >= = !=`, `m.<measure code>` (the
  receiver's measure value in the period) and `b.<cost element code>` (the receiver's Journal and Manual cost on that
  element in the period and the template currency; allocated cost is never a driver, so runs do not feed on runs).
  Formulas are parsed at save and may reference only measures and elements of the legal entity (`CO-0621` naming
  the template and line). Every save of a code is a new version kept in `allocation_template_version`; the caller
  sends the version it read (`expectedVersion`, 0 for a new code), so a concurrent edit is refused (`CST-0018`).
- **Dry run and run**: `:dry-run` evaluates a period and posts nothing. A run evaluates each line once, rounds it to
  the currency's minor unit, debits the receiver and credits the sender on the line's secondary element (origin
  `Allocation`, linked by `template_run_id`), so the run nets to zero. A negative line fails the run unless the
  template sets `allowNegative` (`CST-0015`); dividing by zero gives 0 for that line with a `CO-0622` warning. The
  template row is locked and one completed run exists per template and period (`CST-0002`). The run emits
  `allocation.completed` with method `Template` and `template_id`, plus `costentry.created` per entry.

## What slice 4 adds

- **Allocation rerun with reversal (BR-38.7)**: posting an allocation run for a period that already has a completed
  run is a rerun. In one transaction the earlier run's Allocation entries are negated (the negations carry the old
  run id, so each run nets to zero per cost object), the old run becomes `Reversed` with `reversedByRunId`, and the
  new run is computed from the current journal cost and stored with `reversesRunId`. `allocation.completed` carries
  `reversed_run_id`. The completed-run row is locked first, so two concurrent reruns serialize; the loser is
  `CST-0002`.
- **Warnings (BR-38.7, BR-38.8)**: runs report `warnings`. `CO-0613` names each sender with cost and zero basis (its
  cost stays where it is). A completed run read back (`GET /allocation-runs`, `/{id}`) carries `CO-0615` when journal
  cost for its cost objects, period and currency was mirrored after the run, so the period needs a rerun.
- **Cost behavior (FR-38.16)**: a cost element may carry a `defaultBehavior` (Fixed or Variable), and
  `POST /v1/controlling/cost-behavior-rules` stores effective-dated rules per element, for every cost object kind or
  one kind (one rule per element, kind and date, `CST-0020`). Journal lines are classified when they are mirrored: a
  rule for the object's kind beats a rule for any kind, the latest rule effective on or before the first day of the
  period wins within a tier, then the element default, else `Unclassified`. Entries already in the ledger keep their
  class; allocation, top-down, settlement, template and hierarchy distribution entries stay `Unclassified`.

## What slice 5 adds

- **Cost object hierarchies (FR-38.18)**: `POST /v1/controlling/cost-object-hierarchies` defines a hierarchy per legal
  entity (code unique, `CST-0003`) of type `Reporting` (grouping only) or `Collective` (nodes may collect cost, as in
  SAP KKPHIE). Nodes (`POST /cost-object-hierarchies/{id}/nodes`) place a cost object of the hierarchy's legal entity
  under a parent of the same hierarchy (for example product group → product → production order); a cost object
  appears once per hierarchy (`CST-0025`) but may sit in several hierarchies. A collecting node names its
  distribution basis: `ActualOutput`, `StandardCost` (output valued at standard cost) or `FixedPercent`. A collecting
  node in a Reporting hierarchy is `CST-0023`.
- **Collection (SAP KKP4)**: `POST /v1/controlling/hierarchy-nodes/{id}:collect` posts cost that cannot be assigned to
  a lower object as a `Manual` cost entry on the node's cost object, classified Fixed or Variable by the FR-38.16
  rules. A negative amount corrects an earlier collection and may not take the node below zero (`CST-0028`).
- **Distribution at period end (SAP KKPX)**: `POST /v1/controlling/hierarchy-distributions` takes everything the node
  holds for an element, period and currency and splits it among the named direct children (`CST-0026` otherwise) by
  the node's basis, with the basis value per child supplied on the request. ActualOutput and StandardCost split in
  proportion with the rounding residue to the largest basis (the top-down rule); FixedPercent gives each child its
  percentage (Σ ≤ 100) truncated to the minor unit, with the residue to the largest percentage when they total 100.
  The node is credited and each child debited on the same element (origin `HierarchyDistribution`, linked by
  `hierarchy_run_id`), so the run nets to zero and the GL reconciliation is unchanged. What is not distributed stays
  on the node with warning `CO-0645` and a later run may take it. A collecting child passes what it received further
  down with its own run. Every collected or distributed entry carries the node that owns it, so a node's balance is
  its own entries even if its cost object is in another hierarchy too; `GET /cost-object-hierarchies/{id}/balances`
  lists them per period. The node row is locked during a collection or a run, so two runs cannot both distribute
  the same balance. The run emits `dev.passage.controlling.hierarchy.distributed.v1` plus `costentry.created` per
  entry.

Costs are `numeric(19,4)` with a currency. Commands require `Idempotency-Key` and write outbox events
(`dev.passage.controlling.costentry.created.v1`, `dev.passage.controlling.allocation.completed.v1`,
`dev.passage.controlling.hierarchy.distributed.v1`; a top-down run
emits `allocation.completed` with method `TopDown`, a template run with method `Template`).

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
| POST | `/v1/controlling/cost-behavior-rules` | `controlling.maintain` |
| GET | `/v1/controlling/cost-behavior-rules?legalEntityId=&costElementId=` | `controlling.view` |
| POST | `/v1/controlling/cost-entries:mirror` | `controlling.maintain` |
| POST | `/v1/controlling/allocation-runs` | `controlling.run` |
| GET | `/v1/controlling/allocation-runs?legalEntityId=`, `/v1/controlling/allocation-runs/{id}` | `controlling.view` |
| GET | `/v1/controlling/cost-objects/{id}/entries?period=` | `controlling.view` |
| POST | `/v1/controlling/reconciliations` | `controlling.view` (read-only, no Idempotency-Key) |
| POST | `/v1/controlling/top-down-runs` | `controlling.run` |
| GET | `/v1/controlling/top-down-runs?legalEntityId=`, `/v1/controlling/top-down-runs/{id}` | `controlling.view` |
| POST | `/v1/controlling/internal-orders/{id}:settle` | `controlling.run` |
| GET | `/v1/controlling/internal-orders/{id}/settlements?period=` | `controlling.view` |
| POST | `/v1/controlling/statistical-measures`, `/v1/controlling/statistical-measures/{id}/values` | `controlling.maintain` |
| GET | `/v1/controlling/statistical-measures?legalEntityId=`, `/v1/controlling/statistical-measures/{id}/values?period=` | `controlling.view` |
| POST | `/v1/controlling/allocation-templates`, `/v1/controlling/allocation-templates/{id}:dry-run` | `controlling.maintain` (dry run takes no Idempotency-Key) |
| GET | `/v1/controlling/allocation-templates?legalEntityId=`, `/v1/controlling/allocation-templates/{id}` | `controlling.view` |
| POST | `/v1/controlling/template-allocations/runs` | `controlling.run` |
| GET | `/v1/controlling/template-allocations/runs?legalEntityId=&templateId=`, `/v1/controlling/template-allocations/runs/{id}` | `controlling.view` |
| POST | `/v1/controlling/cost-object-hierarchies`, `/v1/controlling/cost-object-hierarchies/{id}/nodes` | `controlling.maintain` |
| GET | `/v1/controlling/cost-object-hierarchies?legalEntityId=`, `/v1/controlling/cost-object-hierarchies/{id}`, `/v1/controlling/cost-object-hierarchies/{id}/balances?period=` | `controlling.view` |
| POST | `/v1/controlling/hierarchy-nodes/{id}:collect`, `/v1/controlling/hierarchy-distributions` | `controlling.run` |
| GET | `/v1/controlling/hierarchy-distributions?legalEntityId=&hierarchyId=`, `/v1/controlling/hierarchy-distributions/{id}` | `controlling.view` |

The PRD's `CostAccountingView`, `CostAccountingMaintain` and `CostAccountingRun` duties are named in the repository's
dotted style. Lists return `{"items": [...]}`, at most 500 rows.

Contract: `api/openapi.yaml`. Events: `events/asyncapi.yaml`.

## Error codes

| Code | Meaning |
|---|---|
| `CST-0001` | Cost object not found |
| `CST-0002` | A concurrent rerun of the period won the race, the source and element already have a completed top-down run, or the template a completed run |
| `CST-0003` | Duplicate cost object, element or hierarchy code |
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
| `CST-0015` | A template line evaluates below zero and the template does not allow negative amounts (BR-38.12) |
| `CST-0016` | Statistical measure not found |
| `CST-0017` | Allocation template not found |
| `CST-0018` | Template version conflict: the code is at another version than the one sent (409) |
| `CST-0019` | Template run not found |
| `CST-0020` | A cost behavior rule already exists for the element, object kind and date (409) |
| `CST-0021` | Cost object hierarchy not found |
| `CST-0022` | Hierarchy node not found, or the parent is not a node of the hierarchy |
| `CST-0023` | The node does not collect, or a collecting node was placed in a Reporting hierarchy |
| `CST-0024` | The node holds no cost for the element, period and currency |
| `CST-0025` | The cost object already has a node in the hierarchy (409) |
| `CST-0026` | Invalid distribution: a receiver is not a direct child, or fixed percentages exceed 100 |
| `CST-0027` | Hierarchy distribution run not found |
| `CST-0028` | A negative collection would take the node below zero |
| `CO-0645` | Warning on a distribution run: the node keeps an undistributed residual |
| `CO-0618` | Settlement exceeds collected cost (BR-38.9) |
| `CO-0621` | Formula error in a template line: syntax, or a reference to an unknown measure or element |
| `CO-0622` | Warning on a dry run or run: a divisor is zero for a receiver; that division evaluates to 0 |
| `CO-0613` | Warning on an allocation run: a sender with cost has zero basis; its cost is not allocated (BR-38.8) |
| `CO-0615` | Warning on a completed allocation run: journal postings arrived after it; rerun the allocation (BR-38.7) |
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
