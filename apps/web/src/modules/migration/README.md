# Migration module

Web screens for `services/migration` (wave 10): migration runs (list, start from a published mapping spec, detail with exception queue, warnings, reconciliation and re-run of rejects) and mapping specs (list, publish from pasted JSON, detail).

The API client sends `Idempotency-Key` on commands. `migration.run` and `migration.spec.write` are enforced by the service; the routes and buttons are also gated on them. Nav items carry no duty because the duties are not seeded in `policies/authz/data.json` yet (see `services/migration/NOT-IMPLEMENTED.md`).
