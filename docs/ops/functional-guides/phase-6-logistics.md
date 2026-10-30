# Functional guide: Phase 6, Warehouse, Quality and Logistics

Waves 31-35. Audience: warehouse managers, warehouse operatives on handhelds, quality engineers, transport planners,
freight accountants and the integration lead who runs trading-partner connections. The guide covers the physical
flow of goods (locations, work, handhelds), the quality gate that stops bad stock from moving, freight from load to
invoice, and the B2B and e-invoicing channels.

This guide describes intended behaviour from the phase 6 PRD (`docs/prd/phase-6-logistics.md`). It is not a
build status. Each wave has its own service and web module, named at the top of its section; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ. Where a later wave completes an item, the text says so.

## Wave 31: Advanced warehouse management

Owning area: `services/warehouse`. Screens: `apps/web/src/modules/warehouse`.

What you do here:

- Model the building: zones, aisles, racks and bins, each with its capacity and profile. Track stock on license plates
  (LPs), and nest handling units inside a plate.
- Set up location directives. A put-away directive picks the bin for an inbound LP; a pick directive picks the bin to
  take from. Work templates and wave templates package the rules for reuse. Replenishment runs on minimum and maximum
  levels, on demand, or on the wave.
- Work the task list. Tasks carry priorities, can be assigned to a user, and can be marked as exceptions. A short pick
  opens an exception task.
- Plan the dock. Load planning and dock scheduling sequence the trucks. An advance shipping notice drives the
  receiving of LPs.
- Cross-dock stock that is already allocated to an outbound order. Cross-dock templates plan it in advance; receipts
  cross-dock opportunistically.
- Sort outbound orders into sort positions. Kit items for value-added services (VAS) as work.
- Keep the LP identity when stock transfers between sites.
- Check labour standards. Planned against actual time is shown per user and shift. Incentive pay is not calculated.
- Store hazardous materials by storage class and fire-containment section. A move that breaks compatibility is refused.

Rules you can rely on:

- A location never holds more than its capacity. A directive that would break capacity picks another location.
- Wave release creates the picking work and the replenishment work for each order in the wave.

## Wave 32: Mobile warehouse execution

Owning area: the handheld app `apps/warehouse-mobile` and the print service `services/print`. Screens for flows,
devices and printers: `apps/web/src/modules/mobile-flows`.

What you do here:

- Receive, put away, pick, pack, move, count and inquire on a handheld scanner. The menus and steps come from the flow
  designer. A flow change reaches the devices without an app release.
- Scan barcodes with GS1-128 application identifiers. The parser reads the identifiers and checks the data.
- Work offline. Up to 500 transactions or 24 hours queue on the device and replay in order when the link returns.
  Each transaction carries an idempotency key, so no line is lost and none is duplicated.
- Print labels in ZPL through the print service. A queue fails over to its next printer after two failed attempts, and
  the job log records it (FR-32.13).
- Use voice picking and RFID reads where the site has them, and automation (AS/RS, conveyors, sorters) through the
  edge gateway.

Behaviour to know: offline replay stops at the first failed transaction and holds the rest until it is resolved,
because later steps may depend on it (BR-32.3). A session whose heartbeat is lost for more than 30 minutes releases its
work lock; resuming locks the work again if it is still open (BR-32.6). A supervisor PIN override authorises exactly
one action within 60 seconds (BR-32.7).

## Wave 33: Quality management and nonconformance

Owning area: `services/quality`. Screens: `apps/web/src/modules/quality`.

What you do here:

- Set up tests and test groups, sampling plans (AQL, per ANSI/ASQ Z1.4) and quality associations. An association says
  when a quality order is created: on receipt, on production, or on shipment.
- Work quality orders. Record results as variables or attributes, then take the usage decision. The decision blocks
  or releases the stock automatically.
- Open a nonconformance when a result fails. Run the correction and the corrective and preventive action (CAPA) with
  an 8D root-cause template. The effectiveness check closes the CAPA.
- Print certificates of analysis (CoA), and send them with shipments when the customer requires them.
- Score suppliers and plot SPC charts from the results.
- Track calibration of measuring instruments, and keep retention samples and stability pull points.
- Gate receipts on vendor certificates, and require source inspection before delivery where the contract says so.
- Check products against restricted-substance lists through the BOM roll-up, and send safety data sheets with the
  shipment.

Rules you can rely on:

- Blocked stock cannot be picked, shipped or consumed until the usage decision releases it. This holds on every path.

## Wave 34: Transportation management and freight

Owning area: `services/transport`, with route optimisation in `services/routing-py`.
Screens: `apps/web/src/modules/transport`.

What you do here:

- Build loads and routes. Capacity and time windows are the constraints; the planner sees the feasible plan and how far
  it is from the best known plan.
- Rate a shipment against the carrier's rate card, with accessorials and fuel surcharges. Rate shopping picks the
  carrier, and the tender goes out in order. A tender that times out passes to the next carrier.
- Track shipments from EDI 214 or the carrier API, and record the proof of delivery.
- Reconcile the carrier invoice against the expected charges. A match closes the item; a mismatch goes to dispute, and
  the accrual posts for the difference.
- Allocate landed cost (freight, duties) to the inventory it applies to, so the costing picks it up.
- Track containers and goods in transit. Estimates and actuals are kept side by side.
- Manage the yard and the gate: transport units, yard moves, detention, driver check-in and check-out, and the gate
  log.
- Run dangerous-goods checks before a load is released: segregation, ADR points and shipping papers.

Check before sign-off: freight reconciliation matches the carrier invoice; landed cost allocation adds up to the
invoice.

## Wave 35: B2B integration: EDI, e-invoicing and the integration hub

Owning area: `services/edi` (EDI and e-invoicing) and `services/integration-hub-ts` (APIs, webhooks, subscriptions),
with the developer portal in `apps/developer-portal`. Screens: `apps/web/src/modules/integration`.

What you do here:

- Set up a trading partner profile: transport (AS2 or SFTP), the transaction sets it uses, and its acknowledgment rules.
- Exchange X12 (850, 855, 856, 810, 820, 214) and EDIFACT (ORDERS, DESADV, INVOIC) documents. The message monitor shows
  each document and lets you replay it.
- Send and receive e-invoices through a Peppol access point (UBL 2.1) and Factur-X. The country mandate framework
  applies the local rules.
- Use the integration hub: public REST and GraphQL APIs, webhooks, Kafka topic subscriptions and API keys with quotas.
  The developer portal lists the APIs.
- Punch out to a supplier catalogue (cXML or SAP OCI). The selected lines return to purchasing as requisition lines.
- Exchange cXML orders, confirmations, ship notices and invoices with a network such as Ariba or Coupa.
- Take customer self-billing invoices (EDIFACT INVOIC 389 and VDA 4908) and match the lines. Mismatches go to the
  dispute worklist.
- Connect Salesforce, Shopify and Stripe through the reference connectors.

Rules you can rely on:

- Webhook delivery is at least once: 8 retries over 24 hours, then the dead-letter queue (FR-35.9, BR-35.7).
  Replayed events carry a replay flag and consumers handle them idempotently (BR-35.9).
- An inbound 850 re-sent with the same purchase order number returns the existing order (BR-35.6).

Check before sign-off (phase 6 exit): when the demo distributor sends an 850, the demo manufacturer ships and
invoices it, and the distributor's payables shows a matched vendor invoice from the 810 and the Peppol UBL, each within
15 minutes (AC-35.9).

## Month-end sequence for this phase

1. Close the warehouse for the period: complete the open tasks, clear the exceptions, and count the cycle-count plan.
2. Clear the quality holds that are past their usage-decision date.
3. Reconcile freight invoices and the landed cost accruals to the GL.
4. Check the message monitor for failed EDI and e-invoice messages, and replay or resolve each one.

Related: [phase 5 guide](phase-5-order-to-cash.md) for the shipments these processes produce,
[phase 3 guide](phase-3-procure-to-pay.md) for receipts and the purchasing side of EDI, and the
[implementer guide](../implementer-guide.md) for partner onboarding before cutover.
