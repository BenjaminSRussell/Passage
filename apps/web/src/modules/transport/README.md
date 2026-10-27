# transport web module (wave 34, PSG-34)

Screens for the transport service (`services/transport`), through the web BFF at
`/api/v1/transport` (service routes `/v1/transport/...`).

| Route | Page | Duty |
|---|---|---|
| `/transport` | Workspace with links to each area | TMS_VIEW |
| `/transport/rate-shop` | Rate a shipment, compare carrier services, read the explain trace | TMS_RATE |
| `/transport/rate-cards` | Rate card list, filter by carrier | TMS_RATE |
| `/transport/rate-cards/new` | Rate card form with rate rows and accessorials (TMS-0102 on overlap) | TMS_RATE_APPROVE |
| `/transport/freight-bills` | Freight bill list, filter by match status | TMS_VIEW |
| `/transport/freight-bills/new` | Expected charges for a load | TMS_RECONCILE |
| `/transport/freight-bills/:id` | Bill detail with the expected-vs-invoiced grid; lines outside tolerance highlighted | TMS_VIEW |
| `/transport/carrier-invoices` | Carrier invoices; UNEXPECTED_INVOICE is the exception queue | TMS_RECONCILE |
| `/transport/carrier-invoices/new` | Register an invoice; prefilled from a bill's detail page | TMS_RECONCILE |

Amounts stay decimal strings (`format.ts` rounds on the digits). Create forms keep one
`Idempotency-Key` per form until a field changes. Tests: `npx vitest --run src/modules/transport`.
