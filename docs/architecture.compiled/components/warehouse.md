# Warehouse

## Purpose & Boundaries
Context `warehouse` (database `passage_warehouse`; waves 28, 31-34). Deployables `warehouse` (Go: directives, work assignment), `warehouse-ts` (mobile flows, label printing, freight), `warehouse-py` (VRP via OR-Tools; stateless). Owns locations, license plates, work, waves, replenishment, loads, routes, rates, freight.

## Interfaces
Prefix `/v1/warehouse/`: `license-plates/{id}:move|:nest`; `waves` (+`:release` -> directives create work); `work:next` (device class; p95 < 1 s NFR-P12), `work/{id}:start|:complete|:cancel` (complete emits `work.completed.v1`); `replenishment-runs` (202); `mobile/sync` (ordered idempotent offline replay, per-op results); `labels:render` (ZPL/PDF); `loads/{id}:plan|:rate|:tender|:ship` (ship emits `load.shipped.v1`; tender timeout falls back to next carrier); `route-plans:solve` (VRP via warehouse-py, 202); `freight-invoices/{id}:reconcile`; `carrier-events` (Sig:HMAC carrier). Generated CRUD for locations (zone/aisle/rack/bin), license plates, location directives, work templates, replenishment templates, loads, carriers, rate tables, mobile flow definitions.

## Data Ownership
EN-065..068 + 9.5 supporting: location (UQ (tenant_id, warehouse_id, location_code); level zone|aisle|rack|bin), license_plate (lp_number UQ, sscc char(18) GS1 check digit, parent_lp_id nesting), lp_line, location_directive(+line), work_template, replenishment_rule, `work` (UQ document_number WRK; work_type put|pick|replenish|count|move; IX-WH-09 partial on assigned_user_id,status for task assignment `FOR UPDATE SKIP LOCKED`), work_line, pick_wave, load(+line), route, carrier, carrier_rate, freight_invoice(+line), mobile_flow_definition (UQ (flow_code, version)). Indexes IX-WH-01..16.

## Events Produced
`warehouse.work.completed.v1` (workId, workType, orderRef, lines[]), `warehouse.load.shipped.v1` (loadId, shipmentIds, licensePlateIds, carrierId, trackingNumber).

## Events Consumed
`warehouse-receipts` (productreceipt.posted -> put-away work), `warehouse-orders` (salesorder.confirmed), `warehouse-prodorder` (productionorder.released -> component pick work), `warehouse-reservations`, `warehouse-qualityblock` (qualityorder.decided), `warehouse-changeorder`, `warehouse-partymerge`, `warehouse-periodcache`, `warehouse-numbersequence`.

## Dependencies
internal: inventory, sales, procurement, manufacturing, product, platform; device-bff and warehouse-mobile PWA. external: carriers (rating, tendering, tracking; fall back to next tender), label printers.

## Gotchas & Constraints
- Offline: 200-transaction sync target (wave 32); steps flagged offline-allowed run against cached packet; offline print jobs queue.
- Blocked stock (quality) never available to ATP/pick.
- Landed-cost allocation conserves total; freight reconciles against loads.
- HID keyboard-wedge scanning primary; GS1 AIs parsed by `@passage/gs1` (frontend package).
- Sales-shipping posts shipments on `load.shipped`.
- `warehouse_ref` read models (product, warehouse, sales_demand).

## Source Sections
- Backend 5.13 (833-856), 3.2 (293); Data 9.5 (3038-3097); Events 3.9 (4071-4072), consumer rows (4224-4225, 4285, 4287); Frontend 7.6.2 (5524-5534)
