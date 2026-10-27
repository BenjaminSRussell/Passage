# Inventory module (web)

Wave 16 slice 1 screens for `services/inventory` (`/api/v1/inventory/...`, rewritten by the
web BFF to the service's `/v1/inventory/...`). Built from the shell primitives in
`src/shell/ui` and `pages.module.css`, following `apps/web/docs/design-system.md`.

| Route | Screen | Duty |
|---|---|---|
| `/inventory`, `/inventory/onhand` | On-hand inquiry: filter by product, site, warehouse, batch; group by any dimension; groups holding negative stock say "Negative stock" in text | `inventory.read` |
| `/inventory/journals` | Journal list with status and type filters and "Load more" paging | `inventory.read` |
| `/inventory/journals/new` | New journal header (type, legal entity, posting date, currency, offset account for MOVEMENT) | `inventory.journal.write` |
| `/inventory/journals/:id` | Journal detail: header, lines, add line (DRAFT), post (If-Match), GL entry once posted | `inventory.read` (add line / post need the write / post duties on the service) |

Every screen has loading (skeleton rows or a busy region), empty and error states; errors
show the service's problem code.

- `api/client.ts` — typed client. Creates send an `Idempotency-Key` that stays the same for
  one form until a value changes; each added line carries a `clientKey`; posting sends the
  shown version in `If-Match`.
- `useLoad.ts` — fetch-on-key hook where the newest request wins.
- `format.ts` — labels, tones and small formatters.

Tests: `api/client.test.ts`, `module.test.ts`, `pages.test.tsx` (`npx vitest --run src/modules/inventory`).

Needs: none. No npm dependencies were added. Product and legal-entity pickers (the forms take
ids), the workspace tiles, transaction drill-down and the reservation, counting and transfer
screens are later slices (see `services/inventory/NOT-IMPLEMENTED.md`).
