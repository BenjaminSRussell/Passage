# Inventory module (web)

Wave 16 slice 1 and 2 screens for `services/inventory` (`/api/v1/inventory/...`, rewritten by the
web BFF to the service's `/v1/inventory/...`). Built from the shell primitives in
`src/shell/ui` and `pages.module.css`, following `apps/web/docs/design-system.md`.

| Route | Screen | Duty |
|---|---|---|
| `/inventory`, `/inventory/onhand` | On-hand inquiry: filter by product, site, warehouse, batch; group by any dimension; groups holding negative stock say "Negative stock" in text | `inventory.read` |
| `/inventory/journals` | Journal list with status and type filters and "Load more" paging | `inventory.read` |
| `/inventory/journals/new` | New journal header (type, legal entity, posting date, currency, offset account for MOVEMENT) | `inventory.journal.write` |
| `/inventory/journals/:id` | Journal detail: header, lines, add line (DRAFT), post (If-Match), GL entry once posted | `inventory.read` (add line / post need the write / post duties on the service) |
| `/inventory/transactions` | Transaction inquiry: filter by product, status, origin, date range; drill to the journal; "Load more" paging | `inventory.read` |
| `/inventory/reservations` | Reservations by status with Release on active ones | `inventory.read` (release needs `inventory.reservation.write`) |
| `/inventory/reservations/new` | Reservation dialog: policy (FIFO, FEFO, LIFO, manual), demand, required date, shelf-life margin, dimensions, partial; shows the allocations and any shortfall | `inventory.reservation.write` |
| `/inventory/blockings` | Blocked and quarantined stock by state with Release | `inventory.read` (release needs `inventory.blocking.write`) |
| `/inventory/blockings/new` | Block stock: quantity, unit, BLOCKED or QUARANTINE, reason, note, dimensions | `inventory.blocking.write` |
| `/inventory/count-plans` | Count plans with Generate sheet (opens the new sheet) | `inventory.read` (generate needs `inventory.count.write`) |
| `/inventory/count-plans/new` | New count plan: legal entity, warehouse, currency, unit, tolerances, blind | `inventory.count.write` |
| `/inventory/count-sheets` | Count sheets by status | `inventory.read` |
| `/inventory/count-sheets/:id` | Count sheet: expected (hidden on blind sheets), inline count entry per line (If-Match), post; variance and count journal once posted | `inventory.read` (entry / post need `inventory.count.write` / `.post`, approval above tolerance `.approve`) |

Every screen has loading (skeleton rows or a busy region), empty and error states; errors
show the service's problem code.

- `api/client.ts` — typed client. Creates send an `Idempotency-Key` that stays the same for
  one form until a value changes; each added line carries a `clientKey`; posting sends the
  shown version in `If-Match`.
- `useLoad.ts` — fetch-on-key hook where the newest request wins.
- `format.ts` — labels, tones and small formatters.
- `parts.tsx` — shared detail entry, problem alert, row action button and field-error mapping.

Tests: `api/client.test.ts`, `module.test.ts`, `pages.test.tsx`, `slice2.test.tsx` (`npx vitest --run src/modules/inventory`).

Needs: none. No npm dependencies were added. Product and legal-entity pickers (the forms take
ids), the workspace tiles, the quarantine disposition choice, the offline count PWA and the
transfer screens are later slices (see `services/inventory/NOT-IMPLEMENTED.md`).
