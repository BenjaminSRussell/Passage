# NOT-IMPLEMENTED — dispatch-py (PSG-45, slice 1)

Full list and reasons: `services/fieldservice/NOT-IMPLEMENTED.md`. Items owned here:

| Item | Status | Reason |
|---|---|---|
| CP-SAT model with the BR-45.3 objective (SLA lateness x100, unassigned priority x1000, travel x1, overtime x5, technician count x50) | later slice of PSG-45 | Needs OR-Tools in the `solver` extra and a timed solve with a 30 s limit and 8 workers. The greedy solver and checker come first so the CP-SAT result can be checked against the same oracle. |
| CP-SAT fallback accounting (`SVC-DSP-002` at fallback rate > 10 %) | later slice of PSG-45 | Depends on the CP-SAT path. |
| Scale run: 1,000 territory-days at 200 work orders x 25 technicians, p95 < 30 s (AC-45.4) | later slice of PSG-45 | Current property test uses up to 10 work orders and 4 technicians. |
| Incremental re-optimization that moves only non-dispatched work orders (BR-45.3) | partial | Pinned routes are kept (AC-45.5, greedy). Consent-based moves are not modelled. |
| OSRM/Valhalla travel matrix client and cached matrix with straight-line fallback x 1.3 | later slice of PSG-45 | Matrices are passed in by the caller. |
| Worker entrypoint, HTTP or queue interface, Helm chart `passage-dispatch`, OSRM sidecar | later slice of PSG-45 | Dispatch runs are not yet called from fieldservice. |
| Certification expiry (BR-42.8) and skills from wave 42 | handoff | Owned by wave 42. |
| Parts availability on truck or reservation (BR-45.3) | later slice of PSG-45 | Needs wave-16 TRUCK locations. |
