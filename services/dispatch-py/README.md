# passage-dispatch (wave 45)

Numeric dispatch for field service: checks a schedule against the BR-45.3 hard
constraints, produces feasible schedules with a greedy-insertion solver and with
CP-SAT (OR-Tools), and serves both as an async optimizer API. Only numeric and
optimization work lives in Python (conventions); the work orders, technicians,
persistence and events stay in `services/fieldservice`, which calls this service.

## What is here

| Module | Role |
|---|---|
| `passage_dispatch.model` | Types (`Technician`, `WorkOrder`, `Assignment`, `Problem`) and `plan_violations`, the single feasibility oracle |
| `passage_dispatch.greedy` | `solve_greedy`: priority-ordered append insertion; every candidate goes through the same oracle, so output is feasible by construction; pinned assignments are kept verbatim (AC-45.5) |
| `passage_dispatch.objective` | `plan_cost`: the BR-45.3 objective (SLA lateness minutes x100, unassigned priority weight x1,000, travel minutes x1, overtime minutes x5, technicians used x50) |
| `passage_dispatch.cpsat` | `solve_cpsat` (one circuit per technician: skills, territory, windows, shift, travel plus buffer, daily load and overtime, pinned assignments, greedy plan as hint) and `optimize`, which falls back to greedy |
| `passage_dispatch.api` | FastAPI app: `POST /v1/dispatch/runs`, `GET /v1/dispatch/runs/{id}`, `/healthz`, `/readyz`, `/metrics` |
| `passage_dispatch.auth` | Verifier of the Ed25519 service tokens (`X-Passage-Service-Token`) fieldservice signs |
| `passage_dispatch.runs` | In-memory run store with a bounded background pool |

Constraints enforced (BR-45.3): skills subset, territory membership, time window
start and end, shift bounds, travel plus buffer between consecutive stops (and from
home), duration consistency, daily load (work + travel, including the return to
home) against `max_daily_minutes + overtime_minutes`, unique work order and
sequence numbers. `WorkOrder.due` is the soft SLA minute: finishing after it costs
lateness in the objective but is not a violation.

## Optimizer

`optimize(problem, pinned, time_limit_s=20, workers=8)` solves the greedy plan
first, runs CP-SAT with it as a hint, compacts routes to their earliest starts
(pinned stops stay put) and verifies the plan with `plan_violations`. It returns the
greedy plan, with `fallback_reason` set and a `SVC-DSP-002` warning logged, when
CP-SAT times out without a solution (`timeout`), proves the model infeasible
(`infeasible`), exceeds the arc cap (`too_large`), is missing (`solver_unavailable`),
returns a plan the oracle refuses (`invalid_plan`), or ends worse than greedy
(`not_better_than_greedy`), or reports MODEL_INVALID (`model_invalid`). Infeasible pinned assignments raise `ValueError`.

## API

Calls carry a service token from the caller (`fieldservice`) whose duties are within
the grant `fieldservice -> dispatch`: `dispatch.run.create` (POST) and
`dispatch.run.read` (GET). Configure `PASSAGE_SERVICE_NAME=dispatch` and
`PASSAGE_SERVICE_PUBLIC_KEYS=fieldservice[:kid]=<base64 Ed25519 public key>`; with no
keys every call is refused and `/readyz` is 503.

- `POST /v1/dispatch/runs` validates the territory-day (matrix, pinned plan), answers
  `202` with the run (`QUEUED`) and a `Location`, and solves in the background. An
  `Idempotency-Key` returns the same run on a retry (`200`, `Idempotent-Replayed`).
  Invalid input is `422 SVC-DSP-001`, a full queue `429 SVC-DSP-012`.
- `GET /v1/dispatch/runs/{id}` returns `status` (`QUEUED`, `RUNNING`, `SUCCEEDED`,
  `FAILED`), and on success the assignments (`workOrderId`, `technicianId`, `start`,
  `end`, `sequence`, minutes from local midnight), the unassigned ids, `solver`,
  `cpsatStatus`, `fallbackReason` and the cost breakdown. A run is visible only to the
  tenant and legal entity of its token (`404 SVC-DSP-013` otherwise).
- Errors are RFC 7807 problems with `code` and `traceId`.

## Run and test

```
cd services/dispatch-py
python -m pip install -e ".[dev]"
pytest
python -m passage_dispatch        # HTTP_HOST / HTTP_PORT, default 0.0.0.0:8080
```

Hypothesis tests (derandomized) check greedy plans (300 examples) and CP-SAT plans
(40 examples each, 1 s limit) against `oracle` in `tests/test_feasibility_proof.py`,
an independent BR-45.3 implementation, plus pinned routes and the plan cost.
`tests/test_cpsat.py` also runs a seeded benchmark of 12 territory-days where CP-SAT
is never worse than greedy and strictly better on at least half.

## Not built here

See `NOT-IMPLEMENTED.md`.

## Error and status codes

`ValueError` for malformed inputs (non-square travel matrix, out-of-range location or
home, non-positive duration, infeasible pinned routes). HTTP: `SVC-DSP-001` (invalid
input), `SVC-DSP-002` (greedy fallback, logged and counted in
`dispatch_solver_fallbacks_total`), `SVC-DSP-012` (queue full), `SVC-DSP-013` (run
not found), `AUTH-0001` / `AUTH-0003`, `COM-0001`.
