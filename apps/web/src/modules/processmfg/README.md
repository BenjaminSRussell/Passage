# processmfg web module (PSG-24 slice 1)

Front end for `services/processmfg`. Discovered by the shell through `module.ts`. Pages use the
`shell/ui` primitives and `pages.module.css` (tokens only), as in `docs/design-system.md`.

## Screens

| Route | Screen | Calls | Duty |
|---|---|---|---|
| `/processmfg` | Overview: links to each area | none | `production.batch-order.read` |
| `/processmfg/formulas` | Formula list (loading, empty, error with retry) | `GET /api/v1/production/formulas` | `production.batch-order.read` |
| `/processmfg/formulas/new` | Formula designer: lines with type, quantity, percent, scalable and percent-controlled; the percent total runs live and turns red when it is not 100 (FR-24.1) | `POST /api/v1/production/formulas` | `production.formula.write` |
| `/processmfg/formulas/:id` | Formula detail: lines, side-by-side scale preview (BR-24.1), "New batch order" | `GET /formulas/{id}`, `POST /formulas/{id}:scale` | `production.batch-order.read` |
| `/processmfg/batch-orders` | Batch order list with status filter | `GET /api/v1/production/batch-orders` | `production.batch-order.read` |
| `/processmfg/batch-orders/new` | Create a batch order from a formula and batch size (FR-24.2) | `GET /formulas`, `POST /batch-orders` | `production.batch-order.write` |
| `/processmfg/batch-orders/:id` | Batch order detail: facts, scaled lines, rescale while planned or released (version checked; a stale version offers a reload) | `GET /batch-orders/{id}`, `POST /batch-orders/{id}:rescale` | `production.batch-order.read` / `.write` |
| `/processmfg/potency` | Potency consumption calculator (BR-24.2) | `POST /api/v1/production/potency:consume` | `production.batch-order.read` |
| `/processmfg/allocation` | Co-product allocation preview (BR-24.3) | `POST /api/v1/production/co-products:allocate` | `production.journal.post` |

Every command sends an `Idempotency-Key`, kept for the life of the form so a retried submit
cannot create twice; it is renewed when the input changes. Quantities stay decimal strings, and
the percent total is summed exactly with BigInt (no floating point).

## Run and test

```
cd apps/web
npx vitest --run src/modules/processmfg
npx tsc --noEmit
```

`pages.test.tsx` stubs `fetch` and renders the module's routes in a MemoryRouter, so it needs no
service. It covers the list, empty, error and detail states, the formula designer's percent
check, create with Idempotency-Key, the scale preview (AC-24.1), rescale with version and its
PMF-0104 path, and the calculators (BR-24.2, BR-24.3), plus the in-repo WCAG checks.

## Needs (for the shell owner, wave 5)

- Item, site and legal-entity pickers: forms take item ids as UUIDs until a shared item lookup
  exists in the shell.
- Batch attributes, disposition, EBR and signature screens wait for their endpoints (see
  `services/processmfg/NOT-IMPLEMENTED.md`).
