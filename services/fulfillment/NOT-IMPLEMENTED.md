# NOT-IMPLEMENTED: fulfillment (PSG-28, wave 28)

Slice 1 builds the release, pick, pack and packing-slip core, the SSCC and GS1
rules, the posting matrix for stocked lines (physical posting and COGS at
shipment), cancellation, the shipment and packing-slip read APIs, the OpenAPI
and AsyncAPI contracts, the chart, and the web screens: fulfillment workspace,
release board, shipments list, shipment detail (confirm pick, pack container,
post packing slip with a journal preview), packing slip journal and detail
(cancel), and the GS1-128 scan page.

Slice 2 adds packing configuration and auto-pack (container types with tare,
returnable flag and packaging material; packing rules per product and optional
customer; `:auto-pack` with FUL-0012 warnings and FUL-0013), packaging
materials per product unit, country fee rates, the statistical packaging fee
lines on the packing slip (posted with a ship-to `countryCode`, mirrored on
cancel, carried in the posted and cancelled events), the packaging report per
country and month, returnable transport packaging balances per customer and
container type (raised on post, lowered on cancel and on a standalone return
receipt, never negative), the matching OpenAPI and AsyncAPI, and the web
screens: container types, packing rules, packaging materials, packaging fees
with the report, returnable packaging, auto-pack on the shipment detail, the
ship-to country on the post form, and the fee lines on the packing slip.

Everything listed below is missing. Each row gives a status and a reason.
Status is `later slice of PSG-28` (same service, next slice) or `handoff`
(needs another wave, the kernel, or a path outside this wave's ownership).

## Important gaps (read first)

| Item | Status | Reason |
|---|---|---|
| Packing-slip post does **not** create inventory transactions. On-hand is not decremented. AC-28.1 (on-hand −10) and AC-28.2 (on-hand +10 on cancel) are not met. | handoff to inventory (wave 16) | Inventory issue is owned by wave 16. This slice emits `dev.passage.fulfillment.packingslip.posted.v1` with lines and quantities, and inventory must consume it. The slice does not write inventory tables. |
| The journal (Dr 1405 / Cr 1400) is stored on the packing slip and in the event. It is **not** posted to the universal journal. | handoff to ledger (wave 11) and kernel `ledgerclient` | The ledger.Post client is a kernel item (`ledgerclient`) not yet built. The journal lines are ready to send with Idempotency-Key = packing_slip_id. |
| Posting is a single local transaction, not a Temporal saga. There is no PENDING state, no compensation at step 3, and no 10-minute watchdog. | later slice of PSG-28 | The saga needs the Temporal worker, which is in the eventing-backbone scope. The store is written so that the ledger step can be added as a saga step. |
| Post moves the shipment straight to `SHIPPED`, and the post requires `PACKED`. A partial post therefore closes the shipment, and the remainder cannot ship. The PRD requires `PARTIALLY_SHIPPED` until remaining_qty = 0 (FR-28.8). | later slice of PSG-28 | Partial shipment state is not modelled. The state machine in `domain` has no PARTIALLY_SHIPPED state yet. |
| Post does not check for an ACTIVE order hold (§5.28.5 validation, FUL-0001 "no active hold"). Holds are only honoured at release (`held` on the release line). | handoff to sales (wave 27) for the hold read; later slice of PSG-28 for the check | The order hold API of wave 27 is not consumed yet. |
| A shipment whose lines are all short-picked to 0 stays `PICKED`: there is nothing to pack, and an empty slip is refused (FUL-0044). It needs the shipment cancel (BR-28.6 CANCELLED) to close. | later slice of PSG-28 | Shipment cancel is not built. |
| The cost on each line comes from the caller (`costs[]`), not from wave 20 cost snapshots. | handoff to inventory costing (wave 20) | Cost snapshot ownership is outside this wave. The caller supplies `costSnapshotId` and `unitCost`. |
| Short pick sets qty_picked below qty_planned. The remainder backorder and release of its reservation (AC-28.4) are not created. | handoff to inventory reservations (wave 16) for the release; later slice of PSG-28 for the backorder line | Reservations are owned by wave 16. |
| The packaging fee is statistical only. The `packaging_fee_billable` SURCHARGE / FREIGHT line with its own tax code (BR-28.12) is not created here. | handoff to billing (wave 29) | Billing owns invoice lines. The fee lines are on the slip and in `packingslip.posted.v1` (`packaging_fees`), so wave 29 can bill them when `packaging_fee_billable` is set. The setting itself is not modelled here. |
| Auto-pack takes unit weights per shipment line from the caller (`unitWeights`); a line without one is limited by quantity only. | handoff to product (wave 8) for unit weights | Product weights and packaging units belong to the product master (wave 8). |
| The ship-to country of the packaging fee comes from the caller (`countryCode` on the post), not from the order's ship-to address. | handoff to sales (wave 27) | The order ship-to is not read yet. Without a country no fee is posted. |

## Deliverables (PRD section 5.28 and ticket PSG-28)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Order release rules (horizon, ATP, hold, one shipment per order × warehouse × date) | built (domain + `POST /releases:run` + release board screen) | A line already on a shipment that is not cancelled is skipped as `already_released`, and runs per warehouse are serialised. Release priority ordering and consolidation (BR-28.8, `fulfillment.consolidation`) are not built. The caller supplies the confirmed lines; reading them from sales (wave 27) is a later slice. |
| D1 | Wave and batch picking, wave creation, wave pick-list print | later slice of PSG-28 | No `pick_work` or `wave` tables yet. Pick confirmation runs on shipment lines. |
| D1 | Pick confirmation with batch/serial/location, `short_pick_action` | partial | Quantity confirmation is built. Batch, serial and location capture (BR-28.10) and the `short_pick_action` setting are not. |
| D1 | Packing with containers, nested pallet → carton, SSCC with mod-10 | partial | SSCC generation and container creation (manual and auto-pack) are built; a known container type gives the tare, returnable flag and material. Nesting (`parent_container_id`) is not. Containers are created CLOSED in the pack call; a separate `:close` action is not exposed. Container dimensions are not recorded. |
| D1 | GS1-128 label rendering (PDF, ZPL) | later slice of PSG-28 | No label rendering or label storage. |
| D2 | Packing slip posting: inventory deduction and COGS journal | partial | Journal (both BR-28.1 stocked rows), `PS-` numbering gap-free per legal entity, containers marked SHIPPED, and the event are built. Inventory deduction is a handoff (see above). Ledger post is a handoff. |
| D2 | Cancellation with mirror journal and reversal_of | built | A reversing slip carries negative quantities, costs and journal amounts; the shipment returns to PACKED and its containers to CLOSED; an INVOICED slip is refused with FUL-0010. Inventory mirror is a handoff. Billing (wave 29) sets INVOICED. |
| D3 | Carrier abstraction (rate, label, manifest, track) with EasyPost/Shippo/file adapters | handoff to `carrier-gateway` (TS service) and later slice of PSG-28 | carrier-gateway is a separate deployable outside `services/fulfillment/`. Its owner must build it. |
| D3 | ASN output (`dev.passage.fulfillment.asn.issued.v1`) | later slice of PSG-28 | FR-28.9 is Should. EDI mapping is owned by wave 35 (open question). |
| D4 | Drop-ship linked to PO, vendor confirm, COGS at PO cost (Dr 5000 / Cr 2110) | later slice of PSG-28 | Needs PO links (wave 17). The `drop_ship` event and vendor-confirm endpoint are not built. |
| D4 | Partial shipments and delivery schedule lines | partial | Shipments are per (order, warehouse, ship date). Schedule lines are not modelled. Partial shipment state is not (see table above). |
| D5 | Tracking visible to the customer portal; `tracking-updated` event; tracking poll and webhook | later slice of PSG-28 | Depends on carrier-gateway. |
| D6 | Picking workbench (grouped waves with preview, saved filters, `WAVE_MAX_LINES`) | later slice of PSG-28 | No workbench endpoints or wave tables. |
| D6 | Packing instructions and auto-pack (`packing_rule`, BR-28.11) | partial | Built: container types, packing rules per product with an optional customer (the customer rule wins), `POST /shipments/{id}:auto-pack` filling containers most specific rule first and closing on quantity or max weight, FUL-0012 warnings for lines without a rule, FUL-0013 when one unit exceeds the max weight, manual repack, and the screens. Not built: rules per product group (needs wave 8 groups), nesting levels, the `auto_pack_enabled` warehouse setting, serial-tracked lines (the domain never splits a serial unit, but the store does not know which lines are serial-tracked until BR-28.10 tracking dimensions are built), and a one-click accept of a proposed plan (auto-pack packs directly; the result lists what it did). |
| D7 | Packaging materials, packaging fee per kg and country, statistical packaging line | partial | Built: packaging material weight per product unit, container types with a material, country fee rates valid from a date, the fee per material (product packaging × shipped units, plus the tare of non-returnable containers of a material) rounded half-even per material, FUL-0014 when a shipped material has no rate, the mirror lines on cancel, the packaging report per country and month (optionally per legal entity) and the screens. Not built: material attributes (type, recyclable flag, unit) beyond the code; the billable SURCHARGE (handoff above); the authoritative DE/FR/ES rate tables, LUCID/CITEO exports and the period statement document (handoff to the country packs, wave 48); the monthly accrual job. |
| D7 | Returnable transport packaging balances (`RTP_NEGATIVE`) | partial | Built: balance per (customer, container type) raised by returnable containers on post, lowered on cancel and by `POST /returnable-packaging:return`, refused below zero with FUL-0015, the balance list and the screen. Not built: the aging inquiry (days outstanding) and its nightly job, the charge hook after `rtp_charge_days` (handoff to wave 30), return receipts from wave 30 RMA lines (handoff to wave 30), manual balance adjustments (`ReturnablePackagingMaintain`), the `rtp_tracking_enabled` setting, and an event for return receipts. |
| Deploy | Fulfillment workspace (tiles, release board, packing station, shipment detail) | partial | Built: workspace tiles (released, picking, packed not shipped, shipped), release board, shipments list, shipment detail with pick, auto-pack, pack and post (with ship-to country), packing slip journal and detail with packaging fees, scan page, container types, packing rules, packaging materials, packaging fees and report, returnable packaging. Not built: carrier exceptions and DNI tiles, wave / pick list screen, packing-station scan-to-pack, bulk release on multi-select, per-warehouse layouts. They need the carrier, DNI and wave endpoints. |
| Deploy | Mobile pick UI (basic PWA) in the demo warehouse | handoff to pwa-apps (owned elsewhere) | The PWA is outside this wave's owned paths. The GS1 parser (`apps/web/src/modules/fulfillment/gs1.ts`) can be reused. |
| Deploy | Demo warehouse seed data | handoff to deployment (`deploy/`) | `deploy/` is not owned by this wave. |
| Deploy | Feature flags `fulfillment.consolidation`, `fulfillment.asn`, `fulfillment.mobile_pick` | later slice of PSG-28 | No flag system wired in this slice. |

## Acceptance criteria

| AC | Status | Reason |
|---|---|---|
| AC1 | partial | Posting is balanced in `domain` and tested. The store test checks Dr 1405 = 125.00 and the mirror on cancel. The 14-cell COGS matrix (`cogs-matrix-14`) is not built. Only the stocked physical-posting cell is tested. Contract tests against the ledger are not built. |
| AC2 | later slice of PSG-28 | The 1,000-order simulated warehouse E2E (`warehouse-sim-1k` fixture) is not built. Zero mismatch needs inventory (handoff). |
| AC3 | built | SSCC check digit and GS1 scan parsing are covered by domain property tests and web tests (`sscc` and `gs1.test.ts`). **Spec discrepancy:** the AC example `0614141` + serial `0123456789` gives a 19-digit string, which is not a valid SSCC-18 (with a 7-digit prefix the serial reference has 9 digits). The tests use the GS1 General Specifications example instead: extension 1, prefix 0614141, serial 123456789 give `106141411234567897`, which scans back from `(00)`. The PRD example needs correction. |
| AC4 | handoff | Carrier adapter contract tests and recorded fixtures belong to carrier-gateway. |
| AC5 | built | This document lists each deliverable not built, with a reason. |
| AC6 | built | `go vet`, `go test`, gofmt, `helm lint`, `vitest` and `tsc` pass. The migration and the store tests were run against Postgres 16 as a NOBYPASSRLS application role. OpenAPI and AsyncAPI are checked against the routes and event types by tests. |

AC-28.11 (auto-pack 12, 12, 6 with valid SSCCs and gross weights), AC-28.12
(10.00 EUR statistical fee and 20 kg cardboard in the DE report) and AC-28.13
(balance 6 after 10 out and 4 back; a return of 7 refused) are covered by store
tests against Postgres (`internal/store/packing_test.go`), domain tests and web
tests. The rejection code is `FUL-0015` from the message catalog (section
5.28.18), not the `RTP_NEGATIVE` string in BR-28.13. Other ACs in section 5.28.8
(AC-28.4 through AC-28.10) map to deliverables above and are not built yet.

## Test plan items (section 5.28.9)

| Item | Status | Reason |
|---|---|---|
| Posting matrix, 14 cells, cancel mirrors | partial | One cell built and tested. |
| Saga compensation (Temporal) | later slice of PSG-28 | No saga yet (see table above). |
| SSCC and GS1-128 property tests | built | `domain/sscc_test.go` and `web gs1.test.ts`. |
| Pick-pack-ship 1k E2E | later slice of PSG-28 | Needs inventory (handoff). |
| Carrier adapters (recordings) | handoff | carrier-gateway. |
| DNI equals GL 1405 (daily job) | later slice of PSG-28 | No DNI projection or 02:00 job. Needs ledger (handoff). |
| Fixtures `warehouse-sim-1k`, `sscc-200`, `carrier-recorded`, `dropship-30`, `cogs-matrix-14` under `testdata/` | later slice of PSG-28 | Not built. |

## Operations and API surface

| Item | Status | Reason |
|---|---|---|
| Endpoints `:rate`, `:label`, `:manifest`, tracking GET, drop-ship vendor-confirm, carrier webhook | later slice of PSG-28 and handoff (carrier-gateway) | Carrier work is not in this slice. |
| Waves and pick endpoints (`/waves`, `/waves/{id}:confirm-pick`) | later slice of PSG-28 | Slice 1 exposes `/shipments/{id}:confirm-pick` instead. |
| `PackingSlipPost` cancel requires `If-Match` (convention) | deviation | A posted slip is immutable and has no version. Only Idempotency-Key is required. |
| Idempotency keys are scoped per tenant and key, not per principal | deviation | The stored fingerprint covers the method, path and body, so a key reused on another resource is refused, not replayed. The table matches `dbx.IdempotencyDDL`. Records are not expired after 24 hours yet (no cleanup job). |
| Duties on handlers (`x-passage-duty`) | built as route-level checks | OPA decision point is handled by the kernel `authz` package. Slice 2 uses `PackingRuleMaintain` (container types, packing rules, packaging materials), `PackagingFeeMaintain` (rates and report) and `WarehouseReceive` (return receipts); the config lists are `WarehouseView`. The `WarehouseShip`, `CarrierAdmin` and `ReturnablePackagingMaintain` duties are not used yet (no carrier or balance-adjustment endpoints). Segregation of duties (WarehousePick with PackingSlipPost under `pick_post_separation`; PackingSlipPost with SalesOrderMaintain; ReturnablePackagingMaintain with WarehouseReceive; PackagingFeeMaintain with InvoiceMaintain) is not enforced. The web nav carries the duty per entry. |
| Returnable balances are kept per (customer, container type), not per (customer, packaging material) as FR-28.16 states | deviation | A returnable container type is the item the customer holds and returns (for example EUR-PALLET), and a return receipt names it. Two returnable types of one material keep separate balances. |
| Cancelling a slip writes the mirror packaging fee lines in the month of the reversal, not the month of the original | deviation | It matches the reversing journal, which is dated on the cancel day, and leaves a reported month unchanged once filed. Within one month the report nets to zero. |
| Cancelling a slip whose returnable containers the customer has already returned is refused with FUL-0015 | deviation | The balance would go negative (BR-28.13). Reverse the return first, or post a manual adjustment once adjustments exist. |
| Container types, packing rules, product packaging and fee rates are saved without a version (`If-Match`) | deviation | They are set-up data saved by key (code; product and customer; product and material; country, material and date): a save replaces the row, last writer wins, and the Idempotency-Key makes a retry safe. Delete endpoints are not built; a weight of 0 stops product packaging counting. |
| `/metrics`, `/v1/fulfillment/openapi.yaml`, `/error-codes` | later slice of PSG-28 | Kernel-standard endpoints, not wired in slice 1. |
| Health `/healthz`, `/readyz` | built | Kernel `httpx.NewRouter` default. |
| Rate-limit and `X-Passage-Api-Build` headers | handoff to kernel `httpx` | |
| Event type naming | deviation | Events use `dev.passage.fulfillment.packingslip.*` (the kernel accepts one segment per name part). The PRD uses `passage.fulfillment.packing-slip.*`, which is not a valid kernel type. |
| Config `COGS_AT_SHIPMENT` per legal entity | later slice of PSG-28 | Slice 1 takes `cogsAtShipment` per request. |
| `GS1_COMPANY_PREFIX` per legal entity (BR-28.5) | deviation | Slice 1 has one prefix per process. It is not per legal entity. The serial counter is per tenant and prefix, gap-free, and a serial that no longer fits the prefix is refused. |
| SSCC no reuse for 12 months (`sscc_no_reuse_months`) | later slice of PSG-28 | No register or reuse check. Uniqueness per tenant is enforced by the database. |
| Period check (FUL-0002), negative on-hand (FUL-0003), tolerance (FUL-0005), tracking dims (FUL-0006) on post | later slice of PSG-28 | Period control is wave 14. Negative on-hand is wave 16. |
| Consolidation (BR-28.8, FUL-0011) | later slice of PSG-28 | |
| Shipment cancel before SHIPPED (BR-28.6, CANCELLED state) | later slice of PSG-28 | Only packing-slip cancel is built. |
| DNI projection and daily reconciliation job (FR-28.12) | later slice of PSG-28 | Needs ledger handoff. |
| Release job every 15 min per warehouse | later slice of PSG-28 | Release is on-demand through the API only. |
| Saga watchdog, carrier retries (BR-28.9), manifest cutoff | later slice of PSG-28 | |
| Outbox events `shipment.released`, `pick.confirmed`, `packingslip.posted`, `packingslip.cancelled` | built | Written in the same transaction as the state change. Consumers are not in this slice. |
| `dev.passage.fulfillment.asn.issued.v1`, `shipment.tracking-updated.v1` | later slice of PSG-28 | |
| Container `:close` endpoint and label reference | later slice of PSG-28 | See D1. |
| Migration from AX/SAP | later slice of PSG-28 | |
| Reporting: DNI, SSCC register; RTP aging | later slice of PSG-28 | Shipments list, release board, packing slip journal, packaging weights per country and month, and RTP balances are built. |
| Documents: packing slip PDF, delivery note, GS1 label, carrier label, manifest, BOL, packaging fee statement | later slice of PSG-28 | Document rendering is wave 5 (docgen). |

## Known risks

- The store's `PostPackingSlip` and `CancelPackingSlip` run inside one `dbx.WithTenantTx` transaction. This is correct for slice 1. A future saga must not add a second transaction that breaks the atomicity assumed here.
- Release de-duplication reads shipments through row-level security, so a line already released under a legal entity outside the caller's scope is not seen. Release runs are expected to use an all-entities principal (the scheduled job) until sales hands lines over by event.
- `GS1_COMPANY_PREFIX` defaults to empty in the chart, so the pod exits at start until a prefix is configured; this is deliberate (no made-up prefix).
