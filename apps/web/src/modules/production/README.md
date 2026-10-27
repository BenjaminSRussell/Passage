# production (web module)

Production control workspace for wave 21 (PSG-21, PRD 4.21.6). Module id `production`, discovered
by the shell through `module.ts`.

| Route | Page | Duty (nav) | API |
|---|---|---|---|
| `/production` | `ProductionOrdersPage` (`OrdersPage.tsx`): newest first, status and origin filters (rework orders on their own), cursor paging, planned and actual cost, a Rework badge | `production.order.read` | `GET /api/v1/production/orders` |
| `/production/new` | `CreateProductionOrderPage` (`CreateOrderPage.tsx`): order type (production or rework order, FR-21.20) with the rework reference (order, batch or serial), header, BOM lines and route, client-side checks that mirror the service, server field errors mapped back to the fields. "Create rework order" on a detail page opens it prefilled with the product as the first component and the order as the reference | `production.order.write` (rework: also `production.order.rework`) | `POST /api/v1/production/orders` |
| `/production/:id` | `ProductionOrderDetail` (`OrderDetailPage.tsx`): header, status actions (the commands BR-21.1 allows, report as finished with good and error quantities, reset to any earlier status), components with flushing and consumed quantity, route with reported hours, the journals panel, costs (planned and actual by group, WIP balance, planned against actual, variances after End), status log | `production.order.read` | `GET …/{id}`, `…/{id}/costs`, `…/{id}/status-log`, `POST …/{id}:{command}` |
| (on the detail page) | `JournalsPanel` (`JournalsPanel.tsx`): journals of the order with Draft / Posted / Reversal / Reversed status and value; open one to see its lines and postings, post it or delete the draft (with confirmation); create a picking list, route card or report as finished with a proposal or with lines entered per component or operation | `production.journal.post` to change | `GET/POST …/{id}/journals`, `POST /api/v1/production/journals/{id}:post`, `DELETE …/journals/{id}` |
| (on the detail page) | `AsBuiltPanel` (`AsBuiltPanel.tsx`): the order's as-built records (FR-21.19); while Started or Reported as finished, assign a component serial or batch to a finished serial or batch, and detach a wrong one after confirmation | `production.journal.post` to change | `GET/POST …/{id}/as-built`, `POST /api/v1/production/as-built/{id}:detach` |
| (on the detail page) | Rework orders of the order (FR-21.20), with the reference of a rework order and a link to its original | `production.order.read` | `GET /api/v1/production/orders?reworkOf=` |
| `/production/as-built` | `AsBuiltInquiryPage` (`AsBuiltInquiryPage.tsx`): as-built of a finished serial or where-used of a component serial (4.21.14, AC-21.14), opens the order | `production.order.read` | `GET /api/v1/production/as-built` |
| `/production/posting-profiles` | `PostingProfilesPage` (`PostingProfilesPage.tsx`): profiles by item group × site with the optional rework variance account, create, edit under If-Match | `production.setup.read` | `GET/PUT /api/v1/production/posting-profiles` |

- `api/client.ts`: typed fetch client over `services/production/api/openapi.yaml`. Commands send
  `If-Match` with the order version and an `Idempotency-Key`; the detail page keeps one key per
  command, version and body so a retry after a lost response replays instead of applying twice,
  and the create form keeps one key per unchanged form.
- Amounts stay decimal strings end to end: `labels.ts` `amount` only pads to cents, and
  `addDecimals`/`negate` add exactly with `BigInt` (journal values, actual − planned); no floats.
- Journals: one Idempotency-Key per create body and per journal post, so a retry replays. As-built
  assignments keep one key per body the same way.
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
  `localStorage` drafts in the journal editor, the drill-down from a variance to the GL journal
  entry (wave 11) and the SSE live update of the costs (PRD 4.21.6) are later work.
- A checkbox primitive in the shell (wave 5) to offer the route-card `finished` flag; the API
  accepts it today.
- Serial and batch lookups from item tracking (wave 16) for the as-built form; until then the
  numbers are typed, and the kiosk assignment at RAF is wave 25.
- No npm dependencies added.
