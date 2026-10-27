# Cost accounting module (web)

Cost accounting screens for PSG-38 slices 1 to 3, wired to `services/costacct` through the BFF at `/api/v1/controlling/...`.

| Route | Page | Service calls |
|---|---|---|
| `/costacct` | Cost objects: list with legal entity and kind filters | `GET /cost-objects` |
| `/costacct/cost-objects/new` | New cost object | `POST /cost-objects` |
| `/costacct/cost-objects/:id` | Cost object with its entries for a period and the period total per currency; an internal order also shows its collected cost, the settlement form and its settlements | `GET /cost-objects/{id}`, `/cost-objects/{id}/entries`, `/cost-elements`, `/internal-orders/{id}/settlements`, `POST /internal-orders/{id}:settle` |
| `/costacct/cost-elements` | Cost elements with main account ranges | `GET /cost-elements` |
| `/costacct/cost-elements/new` | New primary (account range) or secondary element | `POST /cost-elements` |
| `/costacct/allocation-runs` | Allocation runs with cost in / out and convergence | `GET /allocation-runs` |
| `/costacct/allocation-runs/new` | Choose the centers and type the basis matrix, then run | `GET /cost-objects`, `/cost-elements`, `POST /allocation-runs` |
| `/costacct/allocation-runs/:id` | Run totals, net postings and flows | `GET /allocation-runs/{id}` |
| `/costacct/top-down-runs` | Top-down runs with amount, basis period and segment count | `GET /top-down-runs` |
| `/costacct/top-down-runs/new` | Choose source, element, amount and segments, type each basis and see the cent-exact preview, then run | `GET /cost-objects`, `/cost-elements`, `POST /top-down-runs` |
| `/costacct/top-down-runs/:id` | Run totals, shares per segment and zero-basis segments | `GET /top-down-runs/{id}` |
| `/costacct/measures` | Statistical measures with their formula name (`m.<code>`) | `GET /statistical-measures` |
| `/costacct/measures/new` | New statistical measure | `POST /statistical-measures` |
| `/costacct/measures/:id` | Values of a period per cost object and a form to load them | `GET /statistical-measures`, `/statistical-measures/{id}/values`, `/cost-objects`, `POST /statistical-measures/{id}/values` |
| `/costacct/templates` | Allocation templates with version | `GET /allocation-templates` |
| `/costacct/templates/new`, `/costacct/templates/:id/edit` | Template editor: sender, lines of receiver, secondary element and formula; unknown `m.` / `b.` references are caught on the page; edits send the version read | `GET /cost-objects`, `/cost-elements`, `/statistical-measures`, `POST /allocation-templates` |
| `/costacct/templates/:id` | Template lines, dry run for a period, run, run history | `GET /allocation-templates/{id}`, `/template-allocations/runs`, `POST /allocation-templates/{id}:dry-run`, `/template-allocations/runs` |
| `/costacct/template-runs/:id` | Run totals (receivers and sender credit), lines, zero-divisor warnings | `GET /template-allocations/runs/{id}` |
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
