# configurator-py

Stateless constraint configurator for Passage wave 8. Solves the 50-constraint bicycle fixture with OR-Tools CP-SAT when installed (`pip install '.[ortools]'`), otherwise a backtracking solver with the same semantics.

Run: `uvicorn configurator.app:app --port 8080`. Test: `python3 -m pytest -q`.

No database. Go `services/product` calls this worker when `CONFIGURATOR_URL` is set; otherwise it uses the in-process solver.
