# migration-factory web module

Migration factory workspace for wave 49 (PSG-49), discovered by the shell via `module.ts`. Screens
call the service at `tools/migration-factory` through `api/client.ts` (paths from its
`api/openapi.yaml`):

- `/migration-factory`: pack runs with pack and status filters, and the nine packs.
- `/migration-factory/runs/new`: start an open_items run from a pasted extract.
- `/migration-factory/runs/:id`: run counts, the control-total board and quarantined records.
- `/migration-factory/cutover`: cutover plans with progress.
- `/migration-factory/cutover/new`: generate a plan.
- `/migration-factory/cutover/:id`: the runbook, go/no-go checklist, step completion, abort warning
  and two-approver rollback.

Commands send an `Idempotency-Key`; plan commands send the plan `version` the user saw, and a
MIG-0006 conflict reloads the plan.

Needs: none beyond the shell lockfile.
