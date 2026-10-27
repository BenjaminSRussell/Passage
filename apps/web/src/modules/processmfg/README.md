# processmfg web module (PSG-24 slices 1 and 2)

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
| `/processmfg/derivation` | Batch derivation preview: rule (attribute, aggregation, fixed value) and sender batches; shows the derived value and warns about senders without a value (FR-24.15, PMF-0007) | `POST /api/v1/production/batch-derivation:preview` | `production.batch-attribute.write` |
| `/processmfg/merge` | Batch merge preview: source batches with quantity, cost, disposition, expiry, potency and grade; target quantity, cost, attributes and genealogy parents; PMF-0006 refusals (FR-24.16) | `POST /api/v1/production/batch-merge:preview` | `production.batch.merge` |
| `/processmfg/batch-requirements` | Customer batch requirement check: requirement rows by level, candidate batches; qualifying and excluded batches with reasons, same-batch pick or 409 PMF-0008 (FR-24.19, BR-24.13) | `POST /api/v1/production/batch-requirements:check` | `production.batch-order.read` |
| `/processmfg/consolidation` | Bulk consolidation preview: bulk/pack relation and packed orders; bulk orders with the packing orders pegged to each (FR-24.17, BR-24.12) | `POST /api/v1/production/bulk-consolidation:preview` | `production.batch-order.write` |
| `/processmfg/batch-record-check` | Batch record approval check: exceptions with disposition and signature; PMF-0014 while any is open (BR-24.15) | `POST /api/v1/production/batch-records:approve-check` | `production.batch-record.approve` |

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
PMF-0104 path, the calculators (BR-24.2, BR-24.3), and the slice-2 previews: derivation
(AC-24.11, PMF-0007, client-side validation), merge (AC-24.12, override, PMF-0006),
requirements (AC-24.14, 409 PMF-0008), consolidation (AC-24.13) and the approval check
(PMF-0014 then approvable), plus the in-repo WCAG checks.

## Needs (for the shell owner, wave 5)

- Item, site and legal-entity pickers: forms take item ids as UUIDs until a shared item lookup
  exists in the shell.
- Batch attributes, disposition, the derivation monitor, EBR review and signature prompts wait
  for their endpoints (see `services/processmfg/NOT-IMPLEMENTED.md`). The slice-2 pages take
  batches as typed input until a wave-16 batch lookup exists.
