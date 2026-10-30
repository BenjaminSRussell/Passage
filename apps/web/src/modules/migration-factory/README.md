# migration-factory web module

Migration factory workspace for wave 49 (PSG-49), discovered by the shell via `module.ts`. Screens
call the service at `tools/migration-factory` through `api/client.ts` (paths from its
`api/openapi.yaml`):

- `/migration-factory`: pack runs with pack and status filters.
- `/migration-factory/packs`: the nine transactional packs from the service catalog, the legacy
  systems and control totals of each, and a start link for the packs that run today.
- `/migration-factory/runs/new`: start an open_items run from a pasted extract.
- `/migration-factory/runs/new-gl`: load GL balances (gl_balances) from a pasted extract with the
  chart of accounts, optionally reconciled with an open_items run of the same company (BR-49.2).
  The form refuses a paste whose debits and credits differ in a currency, summed exactly.
- `/migration-factory/runs/new-project`: load project beginning balances (project_wip) from pasted
  opening lines and legacy category totals.
- `/migration-factory/runs/:id`: run counts, the control-total board, quarantined records with
  release (party or control-account correction) on LOADED open_items runs, and, for project_wip
  runs, each project's reconciliation; for gl_balances runs, the MIG-1506 control-account
  messages and a link to the open_items run they were reconciled with.
- `/migration-factory/runs/:id/projects/:project`: a project's totals per category against legacy
  and its opening lines, paged by legacy id.
- `/migration-factory/parallel-runs`: parallel runs with a state filter.
- `/migration-factory/parallel-runs/new`: diff a replayed period from pasted legacy and Passage tuples.
- `/migration-factory/parallel-runs/:id`: the variance report with a class filter, explain, and
  sign-off once the exit criterion holds.
- `/migration-factory/xref`: resolve a legacy id to the Passage record it became.
- `/migration-factory/cutover`: cutover plans with progress.
- `/migration-factory/cutover/new`: generate a plan.
- `/migration-factory/cutover/:id`: the runbook, go/no-go checklist, step start with a timer that
  refreshes every 30 s (elapsed against planned, amber at 110 %, red at 125 %, CUT-1602 warnings),
  step completion (blank minutes on a started step use the measured time), abort warning, the
  generated rollback plan, two-approver rollback with the postings since T-0 to re-key, and the journal and document CSV
  downloads once rolled back (FR-49.9).

Commands send an `Idempotency-Key`; pack-run releases, variance explanations, sign-off and plan
commands send the `version` the user saw, and a MIG-0006 conflict reloads the record.

Needs: none beyond the shell lockfile.
