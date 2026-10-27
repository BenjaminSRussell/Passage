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
| `/transport/tenders` | Tender board: status filter, overdue flag, "Cascade overdue tenders" runs the expiry sweep | TMS_TENDER |
| `/transport/tenders/new` | Start a tender: load, carriers in rank order with quoted rates, response time | TMS_TENDER |
| `/transport/tenders/:id` | Cascade attempts; record the pending carrier's acceptance or decline (TMS-0105, TMS-0113) | TMS_TENDER, TMS_CARRIER_RESPOND to respond |
| `/transport/tracking` | Latest status reports and a load-timeline lookup | TMS_VIEW |
| `/transport/tracking/new` | Record a status report by hand (source + reference is the idempotency key) | TMS_TRACK_INGEST |
| `/transport/tracking/:loadId` | Stop states (inferred pickups flagged, BR-34.9) and the event timeline | TMS_VIEW |
| `/transport/voyages` | Landed cost voyages, filter by status | TMS_LANDED_COST |
| `/transport/voyages/new` | Voyage form: legs, containers (TMS-0124 check digit), folios, cost lines, receipt lines | TMS_LANDED_COST |
| `/transport/voyages/:id` | Voyage detail; "Allocate and post" shows each receipt line's share per cost line | TMS_LANDED_COST |

Amounts stay decimal strings (`format.ts` rounds and sums on the digits). Create forms keep one
`Idempotency-Key` per form until a field changes. Tests: `npx vitest --run src/modules/transport`.
