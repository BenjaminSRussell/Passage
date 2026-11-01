# Migration module

Web screens for `services/migration` (wave 10): migration runs (list, start from a published mapping spec, detail with exception queue, warnings, reconciliation and re-run of rejects), mapping specs (list, publish from pasted JSON, detail) configuration packages (list, paste package and signature, preview the diff, apply, detail with applied history) and migration projects (list with type and status filters, create, detail with archive or reactivate, an edit form for the name, cutover and schedule of an active project and, for copy projects, the ordered key rewrite rules with a preview on sample keys).

The API client sends `Idempotency-Key` on commands. `migration.run`, `migration.spec.write`, `migration.package.apply` and `migration.project.write` are enforced by the service; the routes and buttons are also gated on them. Nav items carry no duty because the duties are not seeded in `policies/authz/data.json` yet (see `services/migration/NOT-IMPLEMENTED.md`).
