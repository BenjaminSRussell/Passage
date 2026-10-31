# NOT-IMPLEMENTED: routing-py (PSG-34, wave 34, slices 1 to 5)

| Item | Status | Reason |
|---|---|---|
| OSRM distance and time matrix; stored lane matrix fallback (FR-34.2 notes, §6.34.10 runbook) | later slice of PSG-34 | Straight-line distances are used; OSRM is optional and undecided (open question 1) |
| Pickup-before-delivery precedence for milk runs (BR-34.1 item 5) | later slice of PSG-34 | Not modelled in slice 1 |
| Vehicle equipment type and hazmat compatibility (BR-34.1 items 6 and 7) | later slice of PSG-34 | Requires the fleet model (FR-34.16) |
| Driver-hours constraints per rule set, FMCSA and EU 561 (BR-34.16) | later slice of PSG-34 | Requires the driver model (FR-34.16); route infeasibility per driver is a hard constraint still to build |
| Duration limit of 11 h driving and 14 h on-duty per BR-34.1 item 3 | partial | `max_route_min` is a single per-vehicle limit; the 14 h on-duty window is not separate |
| Solomon benchmark tests C101, R101, RC101, C201, R201 (AC-34.1) | met (locally; see the pipeline row) | `tests/test_solomon.py` over `testdata/solomon/`: every route feasible under Solomon's real-valued times, distance within 10 % of best known (OR-Tools 9.15, 60 s: C101 +2.5 %, R101 +0.5 %, RC101 +1.2 %, C201 +2.8 %, R201 −7.0 % with 8 vehicles against a 4-vehicle reference). The solver is not deterministic across machines, so gaps vary run to run |
| 500-stop run within 60 s (FR-34.2) | later slice of PSG-34 | Only small random instances are tested |
| Load stacking: DG segregation inside the trailer, pallet rotation, a timing test of < 5 s for 60 pallets (FR-34.18 notes) | later slice of PSG-34 | Slice 5 builds `plan_load` (first-fit decreasing by footprint, layers, non-stackable never stacked on, reverse stop order, axle estimate) |
| Asynchronous solve for budgets above 25 s and the optimizer queue (§6.34.19) | later slice of PSG-34 | The transport service calls `POST /v1/routes:solve` synchronously inside its 30 s request limit |
| Helm chart `passage-tms-optimizer` (1–4 replicas, 2 CPU / 4 GB) (§6.34.10) | later slice of PSG-34 | The chart lives with the transport chart work; the optimizer image is not built |
| Lockfile (`uv.lock`) and reproducible CI image | handoff (deployment-cicd) | The build pipeline owns lockfile policy |
| `/readyz` and `/metrics` endpoints | later slice of PSG-34 | Slice 5 serves `/healthz` only |
| Test execution in the wave build pipeline | handoff (testing-reconciliation CI) | The wave build tooling has no Python check kind; `pytest` passed in review (OR-Tools 9.15) and must be run by CI |
| Per-vehicle cost per km and per hour (FR-34.16) | later slice of PSG-34 | `cost_per_km` and `cost_per_hour` are per problem, not per vehicle |
