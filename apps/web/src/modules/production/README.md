# production (web module)

Production control workspace for wave 21 (PSG-21, PRD 4.21.6). Module id `production`, discovered
by the shell through `module.ts`.

| Route | Page | Duty (nav) | API |
|---|---|---|---|
| `/production` | `ProductionOrdersPage` (`OrdersPage.tsx`): newest first, status and origin filters (rework and split orders on their own), cursor paging, planned and actual cost, a Rework badge; select orders to apply estimate, schedule, release, start, end or cancel to all of them (FR-21.12, cancel and end confirmed first) with a result row per order, or to merge them into the first one selected (FR-21.15, confirmed first) | `production.order.read`; `production.order.release` (and the command's duty) to change | `GET /api/v1/production/orders`, `POST …/orders:bulk-status`, `POST …/orders:merge` |
| `/production/new` | `CreateProductionOrderPage` (`CreateOrderPage.tsx`): order type (production or rework order, FR-21.20) with the rework reference (order, batch or serial), header, BOM lines and route, client-side checks that mirror the service, server field errors mapped back to the fields. "Create rework order" on a detail page opens it prefilled with the product as the first component and the order as the reference | `production.order.write` (rework: also `production.order.rework`) | `POST /api/v1/production/orders` |
| `/production/:id` | `ProductionOrderDetail` (`OrderDetailPage.tsx`): header, status actions (the commands BR-21.1 allows, report as finished with good and error quantities, reset to any earlier status), components with flushing and consumed quantity, route with reported hours, the journals panel, costs (planned and actual by group, WIP balance, planned against actual, variances after End), status log | `production.order.read` | `GET …/{id}`, `…/{id}/costs`, `…/{id}/status-log`, `POST …/{id}:{command}` |
| (on the detail page) | `SplitPanel` (`SplitPanel.tsx`): while Created to Released, split a quantity off into a new order with the item's optional minimum and multiple (FR-21.15), then open the new order; the order card links a split order to the order it came from and a merged order to its survivor, and the status log names splits and merges | `production.order.release` to change | `POST …/{id}:split` |
| (on the detail page) | `JournalsPanel` (`JournalsPanel.tsx`): journals of the order with Draft / Posted / Reversal / Reversed status and value; open one to see its lines and postings, post it or delete the draft (with confirmation); create a picking list, route card or report as finished with a proposal or with lines entered per component or operation | `production.journal.post` to change | `GET/POST …/{id}/journals`, `POST /api/v1/production/journals/{id}:post`, `DELETE …/journals/{id}` |
| (on the detail page) | `AsBuiltPanel` (`AsBuiltPanel.tsx`): the order's as-built records (FR-21.19); while Started or Reported as finished, assign a component serial or batch to a finished serial or batch, and detach a wrong one after confirmation | `production.journal.post` to change | `GET/POST …/{id}/as-built`, `POST /api/v1/production/as-built/{id}:detach` |
| (on the detail page) | `TriggerPointsPanel` (`TriggerPointsPanel.tsx`): the order's trigger points (FR-21.17) by operation and sequence with the condition in words, the function, its operations, once or every time and how often it fired; the firings with the feedback, fired or failed (PROD-0019) and what happened, with a link to a rework order a trigger created; until the order is Ended or Cancelled, define one ("when error quantity is more than 0, insert operations / create a rework order / notify", with the operations to insert or route) and delete one that never fired after confirmation. The journals panel takes an error quantity per operation on a route card and shows, after the post and on a posted route card, what its trigger points did | `production.order.read`; `production.setup.write` to change (a rework trigger also `production.order.rework`) | `GET/POST …/{id}/trigger-points`, `DELETE /api/v1/production/trigger-points/{id}` |
| (on the detail page) | Rework orders of the order (FR-21.20), with the reference of a rework order and a link to its original | `production.order.read` | `GET /api/v1/production/orders?reworkOf=` |
| (on the detail page) | `OrderEditPanel` (`OrderEditPanel.tsx`): while Created to Released, add, change or remove a component line or an operation of this order only (FR-21.18; a change sends only the fields filled in), then show the re-exploded lines, route and costs and a "BOM edited" badge; the edit history lists every edit with the line or operation before and after | `production.order.write`; on a Released order also `production.order.bom-after-release` | `PATCH …/{id}/bom`, `GET …/{id}/bom-edits` |
| `/production/wip-balances` | `WIPReportPage` (`WIPReportPage.tsx`): WIP balance by order (4.21.14) with status, as-of date and site filters, consumption, received value, settled and balance per order, totals per currency, load more, opens the order | `production.cost.read` | `GET /api/v1/production/wip-balances` |
| `/production/variances` | `VarianceReportPage` (`VarianceReportPage.tsx`): posted variances (4.21.14) with type, period and item group filters, amount, planned and % of planned, totals per type and the net per currency, load more, opens the order | `production.cost.read` | `GET /api/v1/production/variances` |
| `/production/subcontract-operations` | `SubcontractOperationsPage` (`SubcontractOperationsPage.tsx`): subcontract operations (4.21.14, FR-21.10) with operation status, order status, vendor resource and due-date filters, quantity, reported good, unit price, amount, due date and when release requested the vendor PO, totals per currency, load more, opens the order | `production.order.read` | `GET /api/v1/production/subcontract-operations` |
| `/production/as-built` | `AsBuiltInquiryPage` (`AsBuiltInquiryPage.tsx`): as-built of a finished serial or where-used of a component serial (4.21.14, AC-21.14), opens the order | `production.order.read` | `GET /api/v1/production/as-built` |
| `/production/posting-profiles` | `PostingProfilesPage` (`PostingProfilesPage.tsx`): profiles by item group × site with the optional rework variance account, create, edit under If-Match | `production.setup.read` | `GET/PUT /api/v1/production/posting-profiles` |

- `api/client.ts`: typed fetch client over `services/production/api/openapi.yaml`. Commands send
  `If-Match` with the order version and an `Idempotency-Key`; the detail page keeps one key per
  command, version and body so a retry after a lost response replays instead of applying twice,
  and the create form keeps one key per unchanged form.
- Amounts stay decimal strings end to end: `labels.ts` `amount` only pads to cents, and
  `addDecimals`/`negate` add exactly with `BigInt` (journal values, actual − planned); no floats.
- Bulk updates, merges and splits keep one Idempotency-Key per command and selection (or body and
  version), so retrying after a lost response replays: a bulk retry replays the orders already
  moved and retries the failed ones.
- Journals: one Idempotency-Key per create body and per journal post, so a retry replays. As-built
  assignments and trigger point definitions keep one key per body the same way.
- Pages use the shell primitives (`PageHeader`, `Card`, `DataTable`, `FormField`, `Input`, `Select`,
  `Button`, `Badge`, `Alert`, `EmptyState`, `Skeleton`, `StatGroup`, `StatTile`) and `pages.module.css` tokens. Every page has
  loading, empty and error states.

Tests: `npx vitest --run src/modules/production` and `npx tsc --noEmit` in `apps/web`. The page
tests render the module routes inside a stand-in shell frame and run the in-repo AA checks.

## Needs

- Lookups for product (wave 8), site and warehouse (wave 6), resource (wave 9) and the active BOM
  and route versions (wave 9). Until then the create form takes ids and the BOM and route lines
  by hand.
- Saved views, the `Ctrl+Shift+S` status menu,
  `localStorage` drafts in the journal editor, the drill-down from a variance to the GL journal
  entry (wave 11) and the SSE live update of the costs (PRD 4.21.6) are later work.
- A checkbox primitive in the shell (wave 5) to offer the route-card `finished` flag; the API
  accepts it today. The BOM editor offers round-up and the WIP report's legal entity as selects
  and fields for the same reason; subcontracting an operation is not offered in the editor yet.
- Serial and batch lookups from item tracking (wave 16) for the as-built form; until then the
  numbers are typed, and the kiosk assignment at RAF is wave 25.
- No npm dependencies added.
