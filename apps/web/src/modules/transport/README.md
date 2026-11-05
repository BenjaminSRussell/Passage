# transport web module (wave 34, PSG-34)

Screens for the transport service (`services/transport`), through the web BFF at
`/api/v1/transport` (service routes `/v1/transport/...`).

| Route | Page | Duty |
|---|---|---|
| `/transport` | Workspace with links to each area | transport.view |
| `/transport/loads` | Loads by status with weight and pallet fill against capacity | transport.view |
| `/transport/loads/build` | Build loads from shipments by rule (TMS-0115, TMS-0116); shows the loads planned | transport.load.plan |
| `/transport/loads/:id` | Load detail: capacity, shipments, latest DG check with findings and shipping papers, DG check form, DG history, party screening (a HOLD links to its screening hold); "Start tender" hidden while the latest DG check is FAIL | transport.view, transport.load.plan to check and screen |
| `/transport/rate-shop` | Rate a shipment, compare carrier services, read the explain trace | transport.rate-card.maintain |
| `/transport/rate-cards` | Rate card list, filter by carrier | transport.rate-card.maintain |
| `/transport/rate-cards/new` | Rate card form with rate rows and accessorials (TMS-0102 on overlap) | transport.rate-card.approve |
| `/transport/freight-bills` | Freight bill list, filter by match status | transport.view |
| `/transport/freight-bills/new` | Expected charges for a load | transport.freight-bill.reconcile |
| `/transport/freight-bills/:id` | Bill detail with the expected-vs-invoiced grid; lines outside tolerance highlighted; approve (MATCHED, VARIANCE, DISPUTED) and dispute (VARIANCE) with a reason and `If-Match`; decision history | transport.view, transport.freight-bill.approve to approve, transport.freight-bill.reconcile to dispute |
| `/transport/carrier-invoices` | Carrier invoices; UNEXPECTED_INVOICE is the exception queue | transport.freight-bill.reconcile |
| `/transport/carrier-invoices/new` | Register an invoice; prefilled from a bill's detail page | transport.freight-bill.reconcile |
| `/transport/tenders` | Tender board: status filter, overdue flag, "Cascade overdue tenders" runs the expiry sweep | transport.load.tender |
| `/transport/tenders/new` | Start a tender: load, carriers in rank order with quoted rates, response time. With transport.load.tender the page reads the load and shows its status and version; "Release to the tender workflow" (PLANNED or RATED loads only) sends the same cascade to `:release` with the version as `If-Match` and one `Idempotency-Key` per form, then shows the load's resulting status (TENDERED). A stale version (412) or a status conflict (409, TMS-0114, TMS-0136) rereads the load; 404 and 422 (TMS-0130, TMS-0133) are shown as neighbouring actions show them | transport.load.tender |
| `/transport/tenders/:id` | Cascade attempts; record the pending carrier's acceptance or decline (TMS-0105, TMS-0113) | transport.load.tender, transport.tender.respond to respond |
| `/transport/tracking` | Latest status reports and a load-timeline lookup | transport.view |
| `/transport/tracking/new` | Record a status report by hand (source + reference is the idempotency key) | transport.tracking-event.ingest |
| `/transport/tracking/:loadId` | Stop states (inferred pickups flagged, BR-34.9) and the event timeline | transport.view |
| `/transport/voyages` | Landed cost voyages, filter by status | transport.landed-cost.maintain |
| `/transport/voyages/new` | Voyage form: legs, containers (TMS-0124 check digit), folios, cost lines, receipt lines | transport.landed-cost.maintain |
| `/transport/voyages/:id` | Voyage detail; "Allocate and post" shows each receipt line's share per cost line; links to its goods in transit | transport.landed-cost.maintain |
| `/transport/goods-in-transit` | Goods-in-transit orders with goods value and estimated landed cost; status filter, `?voyageId=` for one voyage | transport.landed-cost.maintain |
| `/transport/goods-in-transit/new` | Open an order: voyage (prefilled from `?voyageId=`), origin Incoterm, destination site, receipt lines (empty for all); TMS-0119 and TMS-0120 shown | transport.landed-cost.maintain |
| `/transport/goods-in-transit/:id` | Order detail with each line's estimate per cost type; "Ship" with the departure time moves ownership (TMS-0121) | transport.landed-cost.maintain |
| `/transport/gate` | Gate log: trucks in the yard by default; filter by status, site, check-in day and carrier; arrival and detention per visit | transport.gate.operate |
| `/transport/gate/check-in` | Check-in form: direction, carrier, trailer, seal, load, driver, appointment and time (or walk-in), paper-fallback arrival time, on-time window and free time | transport.gate.operate |
| `/transport/gate/:id` | Visit detail: arrival (TMS-0127), seal at load confirmation, check-out with the declared seal and detention rate (TMS-0129 blocks and the attempt appears in the log, TMS-0126 detention), gate log | transport.gate.operate |
| `/transport/screening-holds` | Screening holds: open by default, status filter, held parties, who released | transport.view |
| `/transport/screening-holds/:id` | Hold detail with the party results; release with a reason (a BLOCKED party cannot be released, TMS-0133) | transport.view, transport.screening-hold.release to release |
| `/transport/routes` | Route plans with stops, vehicles used, dropped stops and distance | transport.load.plan |
| `/transport/routes/new` | Optimize routes: depot, stops with windows and quantities, vehicles, solver time (5 to 25 s), speed, lateness penalty; TMS-0123 when the optimizer is unavailable | transport.load.plan |
| `/transport/routes/:id` | Plan detail: each used vehicle's stops in order with window, arrival and lateness; dropped stops with TMS-0104 | transport.load.plan |
| `/transport/load-plans` | Load plan: trailer and pallets; top view (SVG, nose at the top), floor positions, load sequence, kingpin and rear-axle estimate, pallets that do not fit | transport.load.plan |

Amounts stay decimal strings (`format.ts` rounds and sums on the digits). Create forms keep one
`Idempotency-Key` per form until a field changes. Tests: `npx vitest --run src/modules/transport`.
