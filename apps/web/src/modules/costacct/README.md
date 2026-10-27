# Cost accounting module (web)

Cost accounting screens for PSG-38 slice 1, wired to `services/costacct` through the BFF at `/api/v1/controlling/...`.

| Route | Page | Service calls |
|---|---|---|
| `/costacct` | Cost objects: list with legal entity and kind filters | `GET /cost-objects` |
| `/costacct/cost-objects/new` | New cost object | `POST /cost-objects` |
| `/costacct/cost-objects/:id` | Cost object with its entries for a period and the period total per currency | `GET /cost-objects/{id}`, `/cost-objects/{id}/entries`, `/cost-elements` |
| `/costacct/cost-elements` | Cost elements with main account ranges | `GET /cost-elements` |
| `/costacct/cost-elements/new` | New primary (account range) or secondary element | `POST /cost-elements` |
| `/costacct/allocation-runs` | Allocation runs with cost in / out and convergence | `GET /allocation-runs` |
| `/costacct/allocation-runs/new` | Choose the centers and type the basis matrix, then run | `GET /cost-objects`, `/cost-elements`, `POST /allocation-runs` |
| `/costacct/allocation-runs/:id` | Run totals, net postings and flows | `GET /allocation-runs/{id}` |
| `/costacct/reconciliation` | Cost ledger against journal balances by element (FR-38.11) | `POST /reconciliations` |

Every list has loading, empty and error (with "Try again") states, and pages use the shell primitives from
`src/shell/ui` with `pages.module.css`. Money stays a decimal string; sums use BigInt ticks of 1/10000 (`logic.ts`).

## Run and test

```sh
cd apps/web
npx vitest --run src/modules/costacct
npx tsc --noEmit
```

## Needs (for the shell owner, wave 5)

- Nav duty `controlling.view` must exist in the duty catalogue (the service also uses `controlling.maintain` and
  `controlling.run`).
- Journal balances for the reconciliation are pasted until the ledger (wave 11) exposes a balance query.
- The cost control workspace (plan vs actual vs committed) and live step progress over server-sent events are later
  slices of PSG-38.
