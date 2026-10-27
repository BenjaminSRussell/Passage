# passage-dispatch (wave 45, slice 1)

Numeric dispatch for field service: checks a schedule against the BR-45.3 hard
constraints and produces feasible schedules with a greedy-insertion solver.
Only numeric and optimization work lives in Python (conventions); HTTP, persistence
and events stay in `services/fieldservice`.

## What is here

| Module | Role |
|---|---|
| `passage_dispatch.model` | Types (`Technician`, `WorkOrder`, `Assignment`, `Problem`) and `plan_violations`, the single feasibility oracle |
| `passage_dispatch.greedy` | `solve_greedy`: priority-ordered append insertion; every candidate goes through the same oracle, so output is feasible by construction; pinned assignments are kept verbatim (AC-45.5) |

Constraints enforced (BR-45.3): skills subset, territory membership, time window
start and end, shift bounds, travel plus 5-minute buffer between consecutive stops
(and from home), duration consistency, daily load (work + travel, including the
return to home) against `max_daily_minutes + overtime_minutes`, unique work order
and sequence numbers.

## Run and test

```
cd services/dispatch-py
python -m pip install -e ".[dev]"
pytest
```

Property tests (Hypothesis, 300 examples each) generate territory-days with up to
4 technicians, 10 work orders and 8 locations, then check that the greedy plan is
feasible, partitions the work orders, and keeps pinned routes unchanged on
re-solve (AC-45.4 and AC-45.5 in shape; the 1,000-day, 200 × 25 scale run and the
p95 timing are a later slice).

## Not built here

- CP-SAT model (OR-Tools, 30 s limit, 8 workers, lexicographic objective). The
  `solver` extra is declared but no model is wired yet. See `NOT-IMPLEMENTED.md`.
- OSRM/Valhalla travel matrix client (matrices are passed in by the caller).
- Worker entrypoint, Helm chart `passage-dispatch`, and the scheduler job
  `service-dispatch-auto`.

## Error and status codes

The worker raises `ValueError` for malformed inputs (non-square travel matrix,
out-of-range location or home, non-positive duration, infeasible pinned routes).
Service-level codes (`SVC-DSP-001..003`) are raised by fieldservice, not here.
