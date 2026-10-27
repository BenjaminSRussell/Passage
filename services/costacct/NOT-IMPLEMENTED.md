# NOT-IMPLEMENTED (costacct, PSG-38 slices 1 and 2)

Every PSG-38 deliverable and acceptance criterion that slices 1 and 2 do not build, with its status and reason.

| Item | Ref | Status | Reason |
|---|---|---|---|
| Ledger entity: one cost accounting ledger per legal entity and currency, versions (Actual 0, Plan 1), period lock | FR-38.6, 7.38.13 `ledger.*`, CO-0614 | later slice of PSG-38 | Slice 1 keys cost objects and entries by legal entity; the ledger row and period lock come next. |
| Dimension hierarchies (effective-dated, versioned) | FR-38.6 | later slice of PSG-38 | Needs the hierarchy entity and its effective dating. |
| Cost behavior classification (Fixed, Variable) by policy; regression with manual override | FR-38.16 | later slice of PSG-38 | Slice 1 writes `Unclassified`. |
| Journal mirroring from events within 5 s (`ledger.elementMapping` consumer) | FR-38.6, AC-38.8 | handoff | The event consumer runs in the eventing backbone. Slice 1 exposes the mirror endpoint it will call. |
| Persisted allocation policies (`AllocationPolicy`: steps, schedule, version); multi-step cycles; ordered steps | FR-38.7 | later slice of PSG-38 | Slice 1 runs one step per request with an inline basis matrix. |
| Actual-cost basis and statistical measure basis; fixed-percentage basis as a named type | FR-38.7 | partial | The basis matrix is generic. Named basis types and the measure table come with the policy entity. |
| Rerun reversal of a prior run's entries (BR-38.7) and the late-postings warning (CO-0615) | BR-38.7 | later slice of PSG-38 | Slice 1 rejects a second completed run for the period (CST-0002) rather than reversing. |
| Allocation schedule per policy (`allocation-schedule`, period close day 2) | 7.38.16 | later slice of PSG-38 | Depends on the policy entity. |
| Zero-basis report and CO-0613 warning | BR-38.8 | partial | Slice 1 returns `unallocated` in the run result. It does not emit CO-0613 or a notice. |
| Exact solve performance at 2,000 senders; 1M-entry allocation under 10 min | FR-38.7, 7.38.9 | later slice of PSG-38 | Slice 1 is correct at any size but not benchmarked. Dense decimal LU is O(n³). Sparse solve or benchmark is the next step. |
| Convergence property test on diagonally dominant cases (residual ≤ 0.01 within 100 iterations) and the `alloc-reciprocal-10` fixture with its 2,500-sender case | 7.38.9 | partial | Slice 1 has a conservation property test on both solver paths, the AC-38.5 analytic test on both paths, an iteration-limit sweep test and a closed-cycle test. |
| Cost entry partitioning by period_key | 7.38.4, 7.38.19 | later slice of PSG-38 | Single table in slice 1; the index is in place. |
| Activity-based costing: activity types, quantities from production (wave 21), rate variances | FR-38.8 | later slice of PSG-38 | Depends on wave 21 quantities. |
| Internal order settlement to a cost object with the settlement cap | FR-38.9, BR-38.9, CO-0618 | built (slice 2) | `POST /v1/controlling/internal-orders/{id}:settle`, partial settlements, row lock on the order. |
| Internal order budgets, persisted settlement rules (percent or equivalence distribution, period settlement of the full balance) | FR-38.9 | later slice of PSG-38 | Each settlement names one receiver and an amount; rules and order budgets need their entities. |
| Settlement to assets (wave 37) or to P&L accounts | FR-38.9 | handoff | Receivers are cost objects. Asset and G/L receivers need a posting to fixedassets and the journal, which those services own. |
| Statistical measure values endpoint (`/statistical-measures/{id}/values`) | 7.38.5 | later slice of PSG-38 | Needs the measure entity. |
| Cost control workspace: plan vs actual vs committed, variance %, period comparison | FR-38.10 | later slice of PSG-38 | Plan comes from the budget plan (budgeting, later slice). Slice 1 lists actual entries per object. |
| Reconciliation job: nightly `cost-gl-reconcile` reading journal balances from the ledger, the alert on a difference, and the `costledger-month` fixture | FR-38.11, AC-38.6, AC3 | partial | `POST /v1/controlling/reconciliations` compares supplied journal balances with Σ primary entries per element (domain and store tests cover AC-38.6). Reading balances from the ledger (wave 11) and scheduling are not built. |
| Management reporting cubes and star schema for wave 46 | FR-38.12 | handoff | Star schema is owned by wave 46 and analytics. Events and CDC feed it. |
| Cost behavior/ overhead rate / cost rollup policies | FR-38.16 | later slice of PSG-38 | Policy entities are not built. |
| Template (driver-formula) allocation, formula grammar, dry run | FR-38.17, AC-38.14, BR-38.12 | later slice of PSG-38 | Needs the restricted expression grammar and its validator. |
| Cost object hierarchies and hierarchy distribution (`hierarchy.distributed.v1`) | FR-38.18 | later slice of PSG-38 | Needs the hierarchy entity. |
| Top-down distribution with residual to the largest basis (worked example AC-38.11) | FR-38.19, BR-38.13 | built (slice 2) | `POST /v1/controlling/top-down-runs`; the basis is supplied per segment. |
| Top-down policy entity, reading the reference basis from the ledger or the sales cube, rerun with reversal | FR-38.19, 7.38.5 `(policy, period, version)` | later slice of PSG-38 | The run takes the basis inline and records the reference period; a second run for the same source, element and period is refused (CST-0002). |
| Allocation run statement (PDF/CSV) and run log inquiry | 7.38.14, 7.38.15 | later slice of PSG-38 | Reporting over `allocation_run.flows`, which is stored. |
| Server-sent events for step progress and residual | 7.38.6 | later slice of PSG-38 | UI feature. |
| Costs posted in one currency per run; FX | FR-38.6 | partial | Slice 1 requires one currency per request and does not convert. |
| ETag on reads; If-Match on custom methods | Platform kernel | later slice of PSG-38 | No versioned reads in slice 1. |
| Separation of duties (policy author cannot run the period) | 7.38.17 | handoff | Rego policy belongs in `policies/` (edge-identity owns it). |
| Shared Postgres `controlling` schema and the Helm umbrella entry | 7.38.10, deploy | handoff | Chart is in `charts/costacct`. The umbrella and the database claim belong to `deploy/`. |
| Jobs `cost-mirror-lag-check` and alerts | 7.38.10 | handoff | Monitoring rules belong to `deploy/` and the observability owner. |
| AX/SAP migration (cost objects, elements, hierarchies, cycles T811C/S/K, KSH1) | 7.38.11 | later slice of PSG-38 | Migration packs are owned by the migration framework. |
| Demo entity cost objects loaded | 7.38.1 Deploy | handoff | Seed data belongs in `deploy/`. |
| Web module | apps/web | built | `apps/web/src/modules/costacct/`: cost objects (list, create, detail with period entries, and for internal orders the settlement form and history), cost elements (list, create), allocation runs (list, new run with a basis grid, run detail), top-down runs (list, new run with a segment grid, run detail), reconciliation. The cost control workspace and variance views are later slices. |
