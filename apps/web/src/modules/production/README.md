# production (web module)

Production control workspace for wave 21 (PSG-21, PRD 4.21.6). Module id `production`, discovered
by the shell through `module.ts`.

| Route | Page | Duty (nav) | API |
|---|---|---|---|
| `/production` | `ProductionOrdersPage` (`OrdersPage.tsx`): newest first, status filter, cursor paging, planned cost | `production.order.read` | `GET /api/v1/production/orders` |
| `/production/new` | `CreateProductionOrderPage` (`CreateOrderPage.tsx`): header, BOM lines and route, client-side checks that mirror the service, server field errors mapped back to the fields | `production.order.write` | `POST /api/v1/production/orders` |
| `/production/:id` | `ProductionOrderDetail` (`OrderDetailPage.tsx`): header, status actions (the commands BR-21.1 allows, report as finished with good and error quantities, reset to any earlier status), components, route, planned cost by group, status log | `production.order.read` | `GET …/{id}`, `…/{id}/costs`, `…/{id}/status-log`, `POST …/{id}:{command}` |

- `api/client.ts`: typed fetch client over `services/production/api/openapi.yaml`. Commands send
  `If-Match` with the order version and an `Idempotency-Key`; the detail page keeps one key per
  command, version and body so a retry after a lost response replays instead of applying twice,
  and the create form keeps one key per unchanged form.
- Amounts stay decimal strings end to end (`labels.ts` `amount` only pads to cents).
- Pages use the shell primitives (`PageHeader`, `Card`, `DataTable`, `FormField`, `Input`, `Select`,
  `Button`, `Badge`, `Alert`, `EmptyState`, `Skeleton`) and `pages.module.css` tokens. Every page has
  loading, empty and error states.

Tests: `npx vitest --run src/modules/production` and `npx tsc --noEmit` in `apps/web`. The page
tests render the module routes inside a stand-in shell frame and run the in-repo AA checks.

## Needs

- Lookups for product (wave 8), site and warehouse (wave 6), resource (wave 9) and the active BOM
  and route versions (wave 9). Until then the create form takes ids and the BOM and route lines
  by hand.
- Bulk status update with multi-select (FR-21.12), saved views, the `Ctrl+Shift+S` status menu,
  the journal editor, actual cost and variances on the cost card, and the SSE live update of the
  costs (PRD 4.21.6) arrive with the later PSG-21 slices that build them in the service.
- No npm dependencies added.
