# Inventory module (web)

Wave 16 slice 1 to 6 screens for `services/inventory` (`/api/v1/inventory/...`, rewritten by the
web BFF to the service's `/v1/inventory/...`). Built from the shell primitives in
`src/shell/ui` and `pages.module.css`, following `apps/web/docs/design-system.md`.

| Route | Screen | Duty |
|---|---|---|
| `/inventory`, `/inventory/onhand` | On-hand inquiry: filter by product, site, warehouse, batch, owner (vendor), segment; group by any dimension; groups holding negative stock say "Negative stock" in text | `inventory.read` |
| `/inventory/journals` | Journal list with status and type filters and "Load more" paging | `inventory.read` |
| `/inventory/journals/new` | New journal header (type, legal entity, posting date, currency, offset account for MOVEMENT); `?type=OWNERSHIP_CHANGE` preselects the type | `inventory.journal.write` (OWNERSHIP_CHANGE also `inventory.consignment.write`) |
| `/inventory/journals/:id` | Journal detail: header, lines, add line (DRAFT, not on a reversal; vendor and consignment price on an ownership change, segment on any line), the approval workflow (Validate, Submit, Approve / Reject, Reopen, Cancel, each If-Match), post (If-Match), GL entry once posted; Reverse on a POSTED journal (not an ownership change) and links between a journal and its reversal | `inventory.read` (add line / validate / submit / reopen / cancel / reverse need `inventory.journal.write`, approve / reject / post `inventory.journal.post`) |
| `/inventory/transactions` | Transaction inquiry: filter by product, status, origin (including purchase, receipt, sales and production lines registered by other services), date range; drill to the transaction or the journal; "Load more" paging | `inventory.read` |
| `/inventory/transactions/:id` | Transaction detail: status, origin, dates, physical and posted cost, status history; the named backward moves that apply at its status (Unpick from picked, Unregister from registered, Cancel reservation from reserved) with the date of the move; a retried move keeps its `Idempotency-Key` | `inventory.read` (moves need `inventory.transaction.step`) |
| `/inventory/status-changes` | Status-change inquiry: every registration and status move newest first, filter by transaction and command, link to the transaction, "Load more" paging | `inventory.read` |
| `/inventory/reservations` | Reservations by status with Release on active ones | `inventory.read` (release needs `inventory.reservation.write`) |
| `/inventory/reservations/new` | Reservation dialog: policy (FIFO, FEFO, LIFO, manual), demand, demand segment (sales order or project, with fallback to unsegmented stock), required date, shelf-life margin, dimensions, partial; shows the allocations and any shortfall | `inventory.reservation.write` |
| `/inventory/blockings` | Blocked and quarantined stock by state with Release | `inventory.read` (release needs `inventory.blocking.write`) |
| `/inventory/blockings/new` | Block stock: quantity, unit, BLOCKED or QUARANTINE, reason, note, dimensions | `inventory.blocking.write` |
| `/inventory/count-plans` | Count plans with Generate sheet (opens the new sheet) and Tags | `inventory.read` (generate needs `inventory.count.write`) |
| `/inventory/count-plans/:id/tags` | Count tags of a plan: list with status filter, generate a range, count (product, quantity, dimensions) or void a tag (If-Match), transfer the used tags to a new count sheet, marking open tags missing as supervisor | `inventory.read` (generate / count / void / transfer need `inventory.count.write`, mark missing `inventory.count.approve`) |
| `/inventory/count-plans/new` | New count plan: legal entity, warehouse, currency, unit, tolerances, blind | `inventory.count.write` |
| `/inventory/count-sheets` | Count sheets by status | `inventory.read` |
| `/inventory/count-sheets/:id` | Count sheet: expected (hidden on blind sheets), inline count entry per line (If-Match), post; variance and count journal once posted | `inventory.read` (entry / post need `inventory.count.write` / `.post`, approval above tolerance `.approve`) |
| `/inventory/transfer-orders` | Transfer orders by status with route and received progress | `inventory.read` |
| `/inventory/transfer-orders/new` | New transfer order: legal entity, shipping / receiving / transit warehouse (or one-step), receiving site, planned ship date, one or more lines with source dimensions and under / over-delivery % | `inventory.transfer.write` |
| `/inventory/transfer-orders/:id` | Transfer order: route, lines (ordered, shipped, received, in transit), Ship with date, Receive per open line with close-short, Cancel (If-Match) | `inventory.read` (ship / receive need `inventory.transfer.post`, cancel `inventory.transfer.write`) |
| `/inventory/in-transit` | Stock in transit by order line with days in transit; lines closed short flagged for investigation | `inventory.read` |
| `/inventory/abc-runs` | ABC classification runs with class counts | `inventory.read` |
| `/inventory/abc-runs/new` | Run classification: legal entity, A / B / C shares (sum 100), class for zero-value products | `inventory.abc.write` |
| `/inventory/abc-runs/:id` | ABC report: product, site, value, cumulative %, class | `inventory.read` |
| `/inventory/consignment-orders` | Consignment replenishment orders by status with vendor, warehouse and received progress | `inventory.read` |
| `/inventory/consignment-orders/new` | New consignment order: legal entity, vendor, site, warehouse, requested date, currency, lines with consignment price and location / batch / serial | `inventory.consignment.write` |
| `/inventory/consignment-orders/:id` | Consignment order: details, lines (ordered, received, open, price), Receive per open line (If-Match) as vendor-owned stock | `inventory.read` (receive needs `inventory.consignment.write`) |
| `/inventory/consignment/stock` | Consignment stock by vendor: vendor-owned quantity, consignment price, statistical value, first receipt and age; New ownership change | `inventory.read` |

Every screen has loading (skeleton rows or a busy region), empty and error states; errors
show the service's problem code.

- `api/client.ts` — typed client. Creates send an `Idempotency-Key` that stays the same for
  one form until a value changes; each added line carries a `clientKey`; posting sends the
  shown version in `If-Match`.
- `useLoad.ts` — fetch-on-key hook where the newest request wins.
- `format.ts` — labels, tones and small formatters.
- `parts.tsx` — shared detail entry, problem alert, row action button and field-error mapping.

Tests: `api/client.test.ts`, `module.test.ts`, `pages.test.tsx`, `slice2.test.tsx`, `slice3.test.tsx`, `slice4.test.tsx`, `slice5.test.tsx`, `slice6.test.tsx` (`npx vitest --run src/modules/inventory`).

Needs: none. No npm dependencies were added. Product and legal-entity pickers (the forms take
ids), the workspace tiles, the quarantine disposition choice and the offline count PWA are later
slices (see `services/inventory/NOT-IMPLEMENTED.md`).
