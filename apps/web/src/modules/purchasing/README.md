# Purchasing web module

Procurement workspace for wave 17 (PSG-17), wired to `services/purchasing` through
`/api/v1/procurement` (see `api/client.ts` and the service's `api/openapi.yaml`).

| Route | Page | Service calls |
|---|---|---|
| `/purchasing` | Workspace: tiles for requisitions in review, approved requisitions, draft POs, active price rows; links to each area | `GET requisitions`, `purchase-orders`, `trade-agreements` (status filters, `limit=200`; a full page shows `200+`) |
| `/purchasing/requisitions` | List with status and "only mine" filters, load more | `GET /requisitions` |
| `/purchasing/requisitions/new` | Header and line editor; blank estimates default from the preferred vendor's trade agreement; PRC-REQ-003 warnings shown on the detail page | `POST /requisitions` (Idempotency-Key) |
| `/purchasing/requisitions/:id` | Summary, approver chain, lines, Submit for approval | `GET /requisitions/{id}`, `POST /requisitions/{id}:submit` (If-Match + Idempotency-Key) |
| `/purchasing/purchase-orders` | List with approval-status filter, load more | `GET /purchase-orders` |
| `/purchasing/purchase-orders/new` | Header, lines (blank unit price = trade agreement), header charges | `POST /purchase-orders` |
| `/purchasing/purchase-orders/:id` | Summary, totals panel, lines with price source, charges | `GET /purchase-orders/{id}` |
| `/purchasing/trade-agreements` | Price row journal, load more | `GET /trade-agreements` |
| `/purchasing/trade-agreements/new` | New PRICE row; PRC-TA-002 overlap shown inline | `POST /trade-agreements` |
| `/purchasing/trade-agreements/:id` | One price row | `GET /trade-agreements/{id}` |
| `/purchasing/pricing` | Price lookup with the explain table | `GET /pricing/lookup` |

Pages use the shell primitives from `src/shell/ui` and `pages.module.css` tokens only.
Every data page has loading, empty and error states; errors show the service's stable
problem code. Amounts stay decimal strings from the service and are never parsed to
floats. Ids are typed as UUIDs until the vendor, product and legal-entity pickers from
waves 6 to 8 are available to modules.

Tests: `npx vitest --run src/modules/purchasing` (`pages.test.tsx` drives every route
against a stubbed `fetch`).
