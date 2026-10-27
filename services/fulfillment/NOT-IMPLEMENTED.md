# NOT-IMPLEMENTED: fulfillment (PSG-28, wave 28)

Slice 1 builds the release, pick, pack and packing-slip core, the SSCC and GS1
rules, the posting matrix for stocked lines (physical posting and COGS at
shipment), cancellation, the shipment and packing-slip read APIs, the OpenAPI
and AsyncAPI contracts, the chart, and the web screens: fulfillment workspace,
release board, shipments list, shipment detail (confirm pick, pack container,
post packing slip with a journal preview), packing slip journal and detail
(cancel), and the GS1-128 scan page.
Everything listed below is missing from this slice. Each row gives a status
and a reason. Status is `later slice of PSG-28` (same service, next slice) or
`handoff` (needs another wave, the kernel, or a path outside this wave's ownership).

## Important gaps in slice 1 (read first)

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

## Deliverables (PRD section 5.28 and ticket PSG-28)

| # | Deliverable | Status | Reason |
|---|---|---|---|
| D1 | Order release rules (horizon, ATP, hold, one shipment per order × warehouse × date) | built (domain + `POST /releases:run` + release board screen) | A line already on a shipment that is not cancelled is skipped as `already_released`, and runs per warehouse are serialised. Release priority ordering and consolidation (BR-28.8, `fulfillment.consolidation`) are not built. The caller supplies the confirmed lines; reading them from sales (wave 27) is a later slice. |
| D1 | Wave and batch picking, wave creation, wave pick-list print | later slice of PSG-28 | No `pick_work` or `wave` tables yet. Pick confirmation runs on shipment lines. |
| D1 | Pick confirmation with batch/serial/location, `short_pick_action` | partial | Quantity confirmation is built. Batch, serial and location capture (BR-28.10) and the `short_pick_action` setting are not. |
| D1 | Packing with containers, nested pallet → carton, SSCC with mod-10 | partial | SSCC generation and container creation are built. Nesting (`parent_container_id`) is not. Containers are created CLOSED in the pack call; a separate `:close` action is not exposed. |
| D1 | GS1-128 label rendering (PDF, ZPL) | later slice of PSG-28 | No label rendering or label storage. |
| D2 | Packing slip posting: inventory deduction and COGS journal | partial | Journal (both BR-28.1 stocked rows), `PS-` numbering gap-free per legal entity, containers marked SHIPPED, and the event are built. Inventory deduction is a handoff (see above). Ledger post is a handoff. |
| D2 | Cancellation with mirror journal and reversal_of | built | A reversing slip carries negative quantities, costs and journal amounts; the shipment returns to PACKED and its containers to CLOSED; an INVOICED slip is refused with FUL-0010. Inventory mirror is a handoff. Billing (wave 29) sets INVOICED. |
| D3 | Carrier abstraction (rate, label, manifest, track) with EasyPost/Shippo/file adapters | handoff to `carrier-gateway` (TS service) and later slice of PSG-28 | carrier-gateway is a separate deployable outside `services/fulfillment/`. Its owner must build it. |
| D3 | ASN output (`dev.passage.fulfillment.asn.issued.v1`) | later slice of PSG-28 | FR-28.9 is Should. EDI mapping is owned by wave 35 (open question). |
| D4 | Drop-ship linked to PO, vendor confirm, COGS at PO cost (Dr 5000 / Cr 2110) | later slice of PSG-28 | Needs PO links (wave 17). The `drop_ship` event and vendor-confirm endpoint are not built. |
| D4 | Partial shipments and delivery schedule lines | partial | Shipments are per (order, warehouse, ship date). Schedule lines are not modelled. Partial shipment state is not (see table above). |
| D5 | Tracking visible to the customer portal; `tracking-updated` event; tracking poll and webhook | later slice of PSG-28 | Depends on carrier-gateway. |
| D6 | Picking workbench (grouped waves with preview, saved filters, `WAVE_MAX_LINES`) | later slice of PSG-28 | No workbench endpoints or wave tables. |
| D6 | Packing instructions and auto-pack (`packing_rule`, BR-28.11) | later slice of PSG-28 | No rule tables. |
| D7 | Packaging materials, packaging fee per kg and country, statistical packaging line | later slice of PSG-28 | Fee tables are country-pack data (wave 48). |
| D7 | Returnable transport packaging balances (`RTP_NEGATIVE`) | later slice of PSG-28 | No balance tables. |
| Deploy | Fulfillment workspace (tiles, release board, packing station, shipment detail) | partial | Built: workspace tiles (released, picking, packed not shipped, shipped), release board, shipments list, shipment detail with pick, pack and post, packing slip journal and detail, scan page. Not built: carrier exceptions and DNI tiles, wave / pick list screen, packing-station scan-to-pack, bulk release on multi-select, per-warehouse layouts. They need the carrier, DNI and wave endpoints. |
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

Other ACs in section 5.28.8 (AC-28.4 through AC-28.13) map to deliverables above and
are not built in this slice.

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
| Duties on handlers (`x-passage-duty`) | built as route-level checks | OPA decision point is handled by the kernel `authz` package. The `WarehouseShip`, `PackingRuleMaintain`, `CarrierAdmin`, `PackagingFeeMaintain` and `ReturnablePackagingMaintain` duties are not used yet. Segregation of duties (WarehousePick with PackingSlipPost under `pick_post_separation`; PackingSlipPost with SalesOrderMaintain) is not enforced. The web nav carries the duty per entry. |
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
| Reporting: shipments list, release board, packing slip journal, DNI, SSCC register, packaging weights, RTP balances | later slice of PSG-28 | |
| Documents: packing slip PDF, delivery note, GS1 label, carrier label, manifest, BOL, packaging fee statement | later slice of PSG-28 | Document rendering is wave 5 (docgen). |

## Known risks

- The store's `PostPackingSlip` and `CancelPackingSlip` run inside one `dbx.WithTenantTx` transaction. This is correct for slice 1. A future saga must not add a second transaction that breaks the atomicity assumed here.
- Release de-duplication reads shipments through row-level security, so a line already released under a legal entity outside the caller's scope is not seen. Release runs are expected to use an all-entities principal (the scheduled job) until sales hands lines over by event.
- `GS1_COMPANY_PREFIX` defaults to empty in the chart, so the pod exits at start until a prefix is configured; this is deliberate (no made-up prefix).
