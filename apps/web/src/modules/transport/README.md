# transport web module (wave 34, PSG-34)

Screens for the transport service (`services/transport`), through the web BFF at
`/api/v1/transport` (service routes `/v1/transport/...`).

| Route | Page | Duty |
|---|---|---|
| `/transport` | Workspace with links to each area | TMS_VIEW |
| `/transport/loads` | Loads by status with weight and pallet fill against capacity | TMS_VIEW |
| `/transport/loads/build` | Build loads from shipments by rule (TMS-0115, TMS-0116); shows the loads planned | TMS_PLAN |
| `/transport/loads/:id` | Load detail: capacity, shipments, latest DG check with findings and shipping papers, DG check form, DG history, party screening (a HOLD links to its screening hold); "Start tender" hidden while the latest DG check is FAIL | TMS_VIEW, TMS_PLAN to check and screen |
| `/transport/rate-shop` | Rate a shipment, compare carrier services, read the explain trace | TMS_RATE |
| `/transport/rate-cards` | Rate card list, filter by carrier | TMS_RATE |
| `/transport/rate-cards/new` | Rate card form with rate rows and accessorials (TMS-0102 on overlap) | TMS_RATE_APPROVE |
| `/transport/freight-bills` | Freight bill list, filter by match status | TMS_VIEW |
| `/transport/freight-bills/new` | Expected charges for a load | TMS_RECONCILE |
| `/transport/freight-bills/:id` | Bill detail with the expected-vs-invoiced grid; lines outside tolerance highlighted; approve (MATCHED, VARIANCE, DISPUTED) and dispute (VARIANCE) with a reason and `If-Match`; decision history | TMS_VIEW, TMS_RECONCILE_APPROVE to approve, TMS_RECONCILE to dispute |
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
| `/transport/voyages/:id` | Voyage detail; "Allocate and post" shows each receipt line's share per cost line; links to its goods in transit | TMS_LANDED_COST |
| `/transport/goods-in-transit` | Goods-in-transit orders with goods value and estimated landed cost; status filter, `?voyageId=` for one voyage | TMS_LANDED_COST |
| `/transport/goods-in-transit/new` | Open an order: voyage (prefilled from `?voyageId=`), origin Incoterm, destination site, receipt lines (empty for all); TMS-0119 and TMS-0120 shown | TMS_LANDED_COST |
| `/transport/goods-in-transit/:id` | Order detail with each line's estimate per cost type; "Ship" with the departure time moves ownership (TMS-0121) | TMS_LANDED_COST |
| `/transport/gate` | Gate log: trucks in the yard by default; filter by status, site, check-in day and carrier; arrival and detention per visit | TMS_GATE |
| `/transport/gate/check-in` | Check-in form: direction, carrier, trailer, seal, load, driver, appointment and time (or walk-in), paper-fallback arrival time, on-time window and free time | TMS_GATE |
| `/transport/gate/:id` | Visit detail: arrival (TMS-0127), seal at load confirmation, check-out with the declared seal and detention rate (TMS-0129 blocks and the attempt appears in the log, TMS-0126 detention), gate log | TMS_GATE |
| `/transport/screening-holds` | Screening holds: open by default, status filter, held parties, who released | TMS_VIEW |
| `/transport/screening-holds/:id` | Hold detail with the party results; release with a reason (a BLOCKED party cannot be released, TMS-0133) | TMS_VIEW, TRADE_COMPLIANCE_RELEASE to release |
| `/transport/routes` | Route plans with stops, vehicles used, dropped stops and distance | TMS_PLAN |
| `/transport/routes/new` | Optimize routes: depot, stops with windows and quantities, vehicles, solver time (5 to 25 s), speed, lateness penalty; TMS-0123 when the optimizer is unavailable | TMS_PLAN |
| `/transport/routes/:id` | Plan detail: each used vehicle's stops in order with window, arrival and lateness; dropped stops with TMS-0104 | TMS_PLAN |
| `/transport/load-plans` | Load plan: trailer and pallets; top view (SVG, nose at the top), floor positions, load sequence, kingpin and rear-axle estimate, pallets that do not fit | TMS_PLAN |

Amounts stay decimal strings (`format.ts` rounds and sums on the digits). Create forms keep one
`Idempotency-Key` per form until a field changes. Tests: `npx vitest --run src/modules/transport`.
