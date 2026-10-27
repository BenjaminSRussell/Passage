# Purchasing web module

Procurement workspace for wave 17 (PSG-17), wired to `services/purchasing` through
`/api/v1/procurement` (see `api/client.ts` and the service's `api/openapi.yaml`).

| Route | Page | Service calls |
|---|---|---|
| `/purchasing` | Workspace: tiles for requisitions in review, approved requisitions, lines needing a vendor, draft POs, POs in review, POs to confirm, effective purchase agreements, active price rows; links to each area | `GET requisitions`, `requisition-lines`, `purchase-orders`, `agreements`, `trade-agreements` (filters, `limit=200`; a full page shows `200+`) |
| `/purchasing/requisitions` | List with status and "only mine" filters, load more | `GET /requisitions` |
| `/purchasing/requisitions/new` | Header and line editor; blank estimates default from the preferred vendor's trade agreement; PRC-REQ-003 warnings shown on the detail page | `POST /requisitions` (Idempotency-Key) |
| `/purchasing/requisitions/:id` | Summary, approver chain, lines with the PO line that orders each consolidated line, Submit for approval | `GET /requisitions/{id}`, `POST /requisitions/{id}:submit` (If-Match + Idempotency-Key) |
| `/purchasing/consolidation` | Consolidation board: open lines of approved requisitions (all / needs sourcing / has a vendor), select lines, override the vendor per line, set the delivery window, create POs and link to them | `GET /requisition-lines`, `POST /requisitions:consolidate` (Idempotency-Key) |
| `/purchasing/purchase-orders` | List with approval-status filter, load more | `GET /purchase-orders` |
| `/purchasing/purchase-orders/new` | Header, lines (blank unit price = agreement override or trade agreement; optional agreement line), header charges | `POST /purchase-orders` |
| `/purchasing/purchase-orders/:id` | Summary, totals panel, lines with price source, received quantity and status, charges, version history with each change in business terms; Submit / Approve / Reject / Confirm and send / Cancel order (with a confirmation step) as the status allows; PRC-AGR-004 warnings after create | `GET /purchase-orders/{id}`, `GET /purchase-orders/{id}/versions`, `POST /purchase-orders/{id}:submit|approve|reject|confirm|cancel` (If-Match + Idempotency-Key) |
| `/purchasing/purchase-orders/:id/edit` | Edit (before confirmation) or change order (after): existing lines keep their price when the unit price is left blank, a line left out is cancelled, new lines are added; says whether the change was confirmed at once or needs re-approval; PRC-PO-009 when someone else changed the order | `GET /purchase-orders/{id}`, `PATCH /purchase-orders/{id}` (If-Match) |
| `/purchasing/agreements` | Purchase agreement list with status filter and highest release %, load more | `GET /agreements` |
| `/purchasing/agreements/new` | Header, validity, start as draft or effective; commitment lines whose fields follow the commitment type | `POST /agreements` (Idempotency-Key) |
| `/purchasing/agreements/:id` | Summary, lines with committed / released / remaining / %, releases linked to their POs, Activate / Put on hold / Close | `GET /agreements/{id}`, `POST /agreements/{id}:activate|hold|close` (If-Match + Idempotency-Key) |
| `/purchasing/trade-agreements` | Price row journal, load more | `GET /trade-agreements` |
| `/purchasing/trade-agreements/new` | New PRICE row; PRC-TA-002 overlap shown inline | `POST /trade-agreements` |
| `/purchasing/trade-agreements/:id` | One price row | `GET /trade-agreements/{id}` |
| `/purchasing/pricing` | Price lookup with the explain table | `GET /pricing/lookup` |

Pages use the shell primitives from `src/shell/ui` and `pages.module.css` tokens only.
Every data page has loading, empty and error states; errors show the service's stable
problem code. Amounts stay decimal strings from the service and are never parsed to
floats. Ids are typed as UUIDs until the vendor, product and legal-entity pickers from
waves 6 to 8 are available to modules.

Tests: `npx vitest --run src/modules/purchasing` (`pages.test.tsx`, `agreements.test.tsx`,
`consolidation.test.tsx` and `orderLifecycle.test.tsx` drive every route against a stubbed `fetch` through
`testHarness.tsx`).
