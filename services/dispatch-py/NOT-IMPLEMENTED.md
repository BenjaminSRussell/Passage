# NOT-IMPLEMENTED — dispatch-py (PSG-45)

Full list and reasons: `services/fieldservice/NOT-IMPLEMENTED.md`. Items owned here:

| Item | Status | Reason |
|---|---|---|
| CP-SAT model with the BR-45.3 objective | met | `passage_dispatch.cpsat` / `objective`: lateness x100, unassigned priority x1000, travel x1, overtime x5, technician count x50 as one weighted sum (not lexicographic); skills, territory, windows, shift, travel, overtime limit and pinned assignments are constraints. Tests: `tests/test_cpsat.py` (oracle properties, pinned, hand scenarios, seeded benchmark vs greedy). |
| Greedy fallback on timeout or infeasibility | met | `optimize` returns the greedy plan with `fallback_reason` (`timeout`, `infeasible`, `too_large`, `solver_unavailable`, `invalid_plan`, `not_better_than_greedy`). Tests: `test_falls_back_*`, `test_a_plan_*`. |
| Optimizer API with async run and status | met | `POST /v1/dispatch/runs`, `GET /v1/dispatch/runs/{id}`, service-token auth with the `fieldservice -> dispatch` grant. Tests: `tests/test_api.py`. |
| CP-SAT fallback accounting (`SVC-DSP-002` alert at fallback rate > 10 %) | partial | Each fallback is logged with `SVC-DSP-002` and counted in `dispatch_solver_fallbacks_total` against `dispatch_runs_total`; the alert rule and dashboard are infrastructure. |
| Scale run: 1,000 territory-days at 200 work orders x 25 technicians, p95 < 30 s (AC-45.4) | later slice | The arc-based model is capped at 400,000 arcs (`too_large` falls back to greedy); the timed scale run and any decomposition for larger territories are not done. The oracle properties run on up to 30 work orders and 6 technicians. |
| Run persistence across restarts | later slice | Runs are in memory (at most 1,000, finished ones evicted first) and lost on restart. Fieldservice stores the request snapshot and the result it received, so a lost run is re-requested. |
| Incremental re-optimization that moves only non-dispatched work orders (BR-45.3) | partial | Pinned assignments are fixed (AC-45.5). Fieldservice sends every already assigned order as pinned; moving SCHEDULED, unpinned orders and consent-based moves are not modelled. |
| OSRM/Valhalla travel matrix client and cached matrix | later slice | Fieldservice builds the matrix from coordinates (straight line x 1.3 at an assumed speed); this service takes the matrix as given. |
| Helm chart `passage-dispatch`, Dockerfile, OSRM sidecar, NetworkPolicy for `/metrics` | later slice | Entrypoint is `python -m passage_dispatch`; deployment is not packaged. No chart or deploy values for dispatch exist in the repo yet; whoever writes them must pin `replicas: 1`, because runs, idempotency keys and the token replay cache are in process memory and a second replica would answer 404 for runs it never saw. Fieldservice resubmits a lost run (404) and a run still QUEUED or RUNNING after 10 minutes. |
| Certification expiry (BR-42.8) and skills from wave 42 | handoff | Owned by wave 42. |
| Parts availability on truck or reservation (BR-45.3) | later slice | Needs wave-16 TRUCK locations. |
