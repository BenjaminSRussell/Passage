# processmfg web module (PSG-24 slices 1 to 3, 9, 11 and 12)

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
| `/processmfg/batch-orders/:id` | Batch order detail: facts, scaled lines, rescale while planned or released (version checked; a stale version offers a reload), then release, start, report as finished (reported quantities per output line) and end with the cost allocation (method, currency, batch cost, weights, by-product rates); an ended order shows its stored cost split | `GET /batch-orders/{id}`, `POST /batch-orders/{id}:rescale`, `:release`, `:start`, `:report-finished`, `:end` | `production.batch-order.read` / `.write`; `:end` needs `production.journal.post` |
| `/processmfg/batches` | Batch list with disposition filter (loading, empty, error with retry); rows open the batch | `GET /api/v1/production/batches` | `production.batch-order.read` |
| `/processmfg/batches/new` | Register a batch (item id, batch number, starting disposition) | `POST /batches` | `production.batch-attribute.write` |
| `/processmfg/batches/:id` | Batch detail: current attributes with the limit result, recording form with one field per attribute definition (version checked, values outside limits reported), disposition form with a reason (hidden, with an explanation, without the duty), disposition history with the segregation flag (same user recorded attributes), attribute history, and a reload action on a stale version | `GET /batches/{id}`, `GET /batch-attribute-definitions`, `PUT /batches/{id}/attributes`, `POST /batches/{id}:disposition` | `production.batch-order.read`; recording `production.batch-attribute.write`; disposition `production.batch-disposition.write` |
| `/processmfg/attribute-definitions` | Attribute definitions by item (filter by item id) and a form to define one: kind, role (generic or potency, numeric only), numeric limits and target, enum options | `GET` and `POST /batch-attribute-definitions` | `production.batch-attribute.write` |
| `/processmfg/potency` | Potency consumption calculator (BR-24.2) | `POST /api/v1/production/potency:consume` | `production.batch-order.read` |
| `/processmfg/allocation` | Co-product allocation preview (BR-24.3) | `POST /api/v1/production/co-products:allocate` | `production.journal.post` |
| `/processmfg/derivation` | Batch derivation preview: rule (attribute, aggregation, fixed value) and sender batches; shows the derived value and warns about senders without a value (FR-24.15, PMF-0007) | `POST /api/v1/production/batch-derivation:preview` | `production.batch-attribute.write` |
| `/processmfg/merge` | Batch merge preview: source batches with quantity, cost, disposition, expiry, potency and grade; target quantity, cost, attributes and genealogy parents; PMF-0006 refusals (FR-24.16) | `POST /api/v1/production/batch-merge:preview` | `production.batch.merge` |
| `/processmfg/batch-requirements` | Customer batch requirement check: requirement rows by level, candidate batches; qualifying and excluded batches with reasons, same-batch pick or 409 PMF-0008 (FR-24.19, BR-24.13) | `POST /api/v1/production/batch-requirements:check` | `production.batch-order.read` |
| `/processmfg/consolidation` | Bulk consolidation preview: bulk/pack relation and packed orders; bulk orders with the packing orders pegged to each (FR-24.17, BR-24.12) | `POST /api/v1/production/bulk-consolidation:preview` | `production.batch-order.write` |
| `/processmfg/batch-record-check` | Batch record approval check: exceptions with disposition and signature; PMF-0014 while any is open (BR-24.15) | `POST /api/v1/production/batch-records:approve-check` | `production.batch-record.approve` |
| `/processmfg/batch-attributes` | Batch attribute check: definitions (kind, min/max, enum options) with the recorded value; results with values outside limits flagged and counted as exceptions (FR-24.5) | `POST /api/v1/production/batch-attributes:check` | `production.batch-attribute.write` |
| `/processmfg/picking` | Batch picking proposal: line (posting date, quantity, potency band, override reason) and candidate batches; picked batches FEFO with overridden ones flagged, excluded batches with PMF-0003, PMF-0005 or PMF-0016, shortfall, and a warning when the override duty is missing (FR-24.3, FR-24.5, FR-24.6, BR-24.10); a what-if for unregistered batches, since a batch of the register is refused with PMF-0112 | `POST /api/v1/production/batch-picking:proposal` | `production.batch-order.read` (override: `production.batch.consume-expired`) |
| `/processmfg/picking-stored` | Pick from stored batches: item id lists its registered batches (loading, empty, error with retry), an on-hand quantity per batch, line quantity, optional potency band (the item's potency attribute) and override reason; picked batches by batch number, excluded batches with PMF-0003, PMF-0005 or PMF-0016, shortfall (FR-24.3, FR-24.6, AC-24.10) | `GET /batches?itemId=`, `POST /api/v1/production/batch-picking:propose-stored` | `production.batch-order.read` (override: `production.batch.consume-expired`) |
| `/processmfg/customer-requirements` | Stored customer batch requirements by item and customer (loading, empty, error with retry): scope, sellable days, attribute range, same batch, validity and status; "Define requirement" for a holder of the duty (FR-24.19, BR-24.13) | `GET /customer-batch-requirements` | `production.batch-order.read` |
| `/processmfg/customer-requirements/new` | Define a requirement for a customer, a customer group or the item default: sellable days, attribute range, same-batch flag, validity; client-side checks before the call | `POST /customer-batch-requirements` | `production.batch-requirement.write` (enforced on the page and in the service) |
| `/processmfg/customer-requirements/:id` | One requirement with its version; retire it under the version read, with a reload on PMF-0104 | `GET /customer-batch-requirements/{id}`, `POST ...:retire` | `production.batch-order.read` (retire: `production.batch-requirement.write`) |
| `/processmfg/requirement-check-stored` | Check stored batches against a customer's stored requirements: item id lists its batches, an on-hand quantity per batch, ship date and line quantity; the resolved requirement, qualifying and excluded batches with reasons (PMF-0009), the same-batch pick or 409 PMF-0008 (FR-24.19, AC-24.14) | `GET /batches?itemId=`, `POST /batch-requirements:check-stored` | `production.batch-order.read` |
| `/processmfg/rework` | Rework batch order plan: batch, quantity to rework, expected output and the FR-24.18 parameters; output batch, disposition, quality order, cost treatment and genealogy (AC-24.18) | `POST /api/v1/production/rework-orders:preview` | `production.batch-order.write` |

Every command sends an `Idempotency-Key`, kept for the life of the form so a retried submit
cannot create twice; it is renewed when the input changes. Quantities stay decimal strings, and
the percent total is summed exactly with BigInt (no floating point).

## Run and test

```
cd apps/web
npx vitest --run src/modules/processmfg
npx tsc --noEmit
```

`customerrequirements.test.tsx` covers the slice-14 screens (list filters, empty and error states, define with one key and client-side validation, retire at the version read with its stale-version path, the duty gates, and the stored check of AC-24.14 with its same-batch shortfall). `batchrecords.test.tsx` covers the slice-11 screens (list filter, empty and error states, register,
recording with the version and the outside-limits report, client-side validation, the disposition
duty and reason, histories, definitions). `pages.test.tsx` stubs `fetch` and renders the module's routes in a MemoryRouter, so it needs no
service. It covers the list, empty, error and detail states, the formula designer's percent
check, create with Idempotency-Key, the scale preview (AC-24.1), rescale with version and its
PMF-0104 path, the calculators (BR-24.2, BR-24.3), and the slice-2 previews: derivation
(AC-24.11, PMF-0007, client-side validation), merge (AC-24.12, override, PMF-0006),
requirements (AC-24.14, 409 PMF-0008), consolidation (AC-24.13) and the approval check
(PMF-0014 then approvable), the slice-3 pages: attribute check (exceptions, client-side
validation, refusal), picking proposal (AC-24.4, AC-24.10 exclusions, the override reason and
its missing-duty warning, the empty state) and rework plan (AC-24.18 defaults, the quality-order
rule, refusal), plus the in-repo WCAG checks.

## Needs (for the shell owner, wave 5)

- Item, site and legal-entity pickers: forms take item ids as UUIDs until a shared item lookup
  exists in the shell.
- The derivation monitor, EBR review and signature prompts wait for their endpoints (see
  `services/processmfg/NOT-IMPLEMENTED.md`). The slice-2 and slice-3 pages take batches as typed
  input until a wave-16 batch lookup exists.
