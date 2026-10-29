# scheduler-py (scheduler-opt)

Stateless CP-SAT optimizer for Passage finite scheduling (wave 23, PSG-23,
FR-23.6 and BR-23.9). It solves a classic job shop: each job is an ordered
chain of operations, each operation runs on one machine for a whole number of
minutes, and a machine runs one operation at a time. The objective is

    10 x sum_j weight_j x max(0, end_j - due_j) + lambdaTenths x makespan

that is, weighted lateness plus lambda x makespan with lambda = lambdaTenths / 10
(default 0.1). Jobs without `due` add no lateness. Windows are limited to
2,000 operations; larger sets are split by the caller.

- `POST /v1/optimize/job-shop` with `{machines, jobs: [{operations: [{machine, minutes, candidates?, network?}], due?, weight?}], timeLimitSeconds?, lambdaTenths?}`
  returns `{status, starts, machines, makespan, totalLateness, objective}`. `status` is the
  CP-SAT status name (OPTIMAL, FEASIBLE, UNKNOWN, INFEASIBLE); `starts` and
  `machines` are empty unless OPTIMAL or FEASIBLE.
  - `candidates` lists the machines an operation may run on, its `machine`
    first; `machines` in the answer gives the one chosen.
  - `network` is the resource network step from the job's previous operation
    into this one (FR-23.13, BR-23.11): a list of edges
    `{fromMachine, toMachine, transferMinutes?, maxWaitMinutes?}`. The two
    operations must run on the machines of one edge (`AddAllowedAssignments` on
    the machine-choice variables), and the next one starts between the edge's
    transfer time and maximum wait after the previous one ends. A job whose
    steps leave no chain of machines is refused with 422.
- `POST /v1/optimize/campaign-sequence` with `{changeover, timeLimitSeconds?}`
  orders a campaign's orders to minimise changeover minutes with `AddCircuit`
  (FR-23.14, at most 50 orders, 30 s by default). `changeover` is an n+1 square
  matrix of whole minutes: index 0 is the resource's current setup state, index
  i is order i-1, and the arc back to index 0 is free. It returns
  `{status, sequence, changeoverMinutes}` with `sequence` the zero-based order
  indices in run order (empty unless OPTIMAL or FEASIBLE; the caller keeps its
  nearest-neighbour sequence then). `changeover_matrix` builds the matrix from
  setup groups and a (from, to) table.
- `GET /healthz`, `GET /readyz`.

Run: `uvicorn scheduler_opt.app:create_app --factory --port 8080`.
Test: `uv run --with ortools --with fastapi --with httpx --with pytest --with hypothesis --no-project -- python -m pytest -q`
(the solver and HTTP tests skip when OR-Tools or FastAPI is not installed).

Fixtures: `testdata/orlib/` holds 30 OR-Library job-shop instances (ft06, ft10,
ft20, la01-la25, abz5, abz6) from `jobshop1.txt`, with the proven optimum of each
in `manifest.json`. The suite solves every instance with CP-SAT (30 s limit
each, about a minute in total) and asserts a feasible schedule within 5% of the
optimum (AC-23.1). Run one with `pytest -k "within_5_percent and la21"`.

Dependencies: `ortools` (CP-SAT), `fastapi`, `pydantic`, `uvicorn`; tests use
`pytest`, `hypothesis` and `httpx`.

Chart: `charts/scheduler-opt` (1 replica, 8 CPU / 16 GB limit per PRD 4.23.10;
KEDA scale-to-zero is not configured yet).
