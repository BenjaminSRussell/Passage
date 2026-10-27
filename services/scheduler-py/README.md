# scheduler-py (scheduler-opt)

Stateless CP-SAT optimizer for Passage finite scheduling (wave 23, PSG-23,
FR-23.6 and BR-23.9). It solves a classic job shop: each job is an ordered
chain of operations, each operation runs on one machine for a whole number of
minutes, and a machine runs one operation at a time. The objective is

    10 x sum_j weight_j x max(0, end_j - due_j) + lambdaTenths x makespan

that is, weighted lateness plus lambda x makespan with lambda = lambdaTenths / 10
(default 0.1). Jobs without `due` add no lateness. Windows are limited to
2,000 operations; larger sets are split by the caller.

- `POST /v1/optimize/job-shop` with `{machines, jobs: [{operations: [{machine, minutes}], due?, weight?}], timeLimitSeconds?, lambdaTenths?}`
  returns `{status, starts, makespan, totalLateness, objective}`. `status` is the
  CP-SAT status name (OPTIMAL, FEASIBLE, UNKNOWN, INFEASIBLE); `starts` is empty
  unless OPTIMAL or FEASIBLE.
- `GET /healthz`, `GET /readyz`.

Run: `uvicorn scheduler_opt.app:create_app --factory --port 8080`.
Test: `uv run --with ortools --with fastapi --with httpx --with pytest --with hypothesis --no-project -- python -m pytest -q`
(the solver and HTTP tests skip when OR-Tools or FastAPI is not installed).

Fixtures: `testdata/orlib/ft06.txt` (Fisher and Thompson 6x6, optimum 55). The
test suite proves the optimum with CP-SAT (AC-23.1 for ft06). The remaining 29
OR-Library instances are listed in `services/scheduling/NOT-IMPLEMENTED.md`.

Dependencies: `ortools` (CP-SAT), `fastapi`, `pydantic`, `uvicorn`; tests use
`pytest`, `hypothesis` and `httpx`.

Chart: `charts/scheduler-opt` (1 replica, 8 CPU / 16 GB limit per PRD 4.23.10;
KEDA scale-to-zero is not configured yet).
