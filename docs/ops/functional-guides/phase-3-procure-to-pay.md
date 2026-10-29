# Functional guide: Phase 3, Inventory & Procure-to-Pay

Waves 16-20. Audience: warehouse and purchasing leads, accounts payable clerks, costing analysts and the
controller who signs off inventory value. Goods move through the inventory ledger, purchasing creates the
obligation, receiving accrues it, and payables settles it. The guide follows that order.

This guide describes intended behaviour from the phase 3 PRD (`docs/prd/phase-3-procure-to-pay.md`). It is not a
build status. Each wave has its own service and web module, named at the top of its section; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ. Where a later wave completes an item (EDI and e-invoicing in wave 35), the text says so.

## Wave 16: Inventory ledger, dimensions and on-hand

Owning area: `services/inventory`. Screens: `apps/web/src/modules/inventory`.

What you do here:

- Read on-hand at any level: site, warehouse, location, batch, serial. Every quantity is an inventory transaction
  with a status: ordered, reserved, picked, deducted, received or posted.
- Post inventory journals: movement, adjustment, transfer, bill of materials consumption, and counting. Journals
  need approval where the policy says so and post a balanced entry to the general ledger.
- Reserve stock manually or automatically. Picking follows FEFO (first expired, first out) or FIFO for batches.
  Block a batch or move it to quarantine to stop it from being reserved.
- Plan physical inventory: cycle counting plans, count sheets, and variance posting after approval. Tag counting and
  periodic ABC classification support the plan.
- Move stock in transit between warehouses with transfer orders: ship, receive, and record under- and over-delivery.
- Keep vendor-consigned stock under the ownership dimension. Ownership changes are journaled, so the value stays with
  the vendor until it is used.

Rules you can rely on:

- Negative on-hand is refused when the item's negative inventory policy forbids it, even under concurrent issues.
- On-hand equals the sum of the transactions. A rebuild from the transactions must match the projection.

## Wave 17: Purchasing

Owning area: `services/purchasing`. Screens: `apps/web/src/modules/purchasing`.

What you do here:

- Raise purchase requisitions. The approval chain depends on amount band and category. Requisitions consolidate
  into purchase orders.
- Run requests for quotation: issue, let suppliers reply through the portal, compare offers and award. Sealed bids
  and weighted scoring are available for formal tenders.
- Maintain purchase agreements (blanket orders) with release tracking, and trade agreements (price lists). The
  most specific price wins: vendor, then group, then all vendors, within the effective dates.
- Confirm purchase orders. A change after confirmation creates a new version; policy may require re-approval.
- Set up scheduling agreements with firm and trade-off zones for just-in-time releases.
- Issue the order to the supplier as PDF or e-mail. An EDI 850 hook exists and is completed in wave 35.

Behaviour to know: the purchasing policy is scoped by the organization hierarchy, so a plant inherits its legal
entity's policy unless it has its own. Worker signing limits are recertified annually.

## Wave 18: Receiving and three-way match

Owning area: `services/receiving`. Screens: `apps/web/src/modules/receiving`.

What you do here:

- Register arrivals and post the product receipt. The receipt updates inventory and accrues the goods-received-not-
  invoiced liability (GR/IR) in the ledger.
- Capture vendor invoices manually, through the OCR adapter, or as electronic invoices (completed in wave 35).
- Let the matching engine compare the invoice with the order and the receipt. Two-way and three-way matching apply
  price, quantity and total tolerances. A mismatch goes to the exception workflow.
- Use evaluated receipt settlement where the supplier invoices from the receipt (self-billing).
- Enter service sheets for services and accept them before the invoice matches.
- Use the random-sample audit block when a sampled invoice needs an auditor release.

Check before sign-off: the GR/IR account nets to zero once every line is invoiced; a serialized item cannot be
received twice; an invoice cannot post into a closed AP period.

## Wave 19: Accounts payable, settlement and payments

Owning area: `services/payables`. Screens: `apps/web/src/modules/payables`.

What you do here:

- Settle open vendor items in full or in part, across currencies, with cash discounts taken or lost and write-offs.
- Create a payment proposal: select invoices by due date, optimize discounts, apply holds and route the proposal for
  approval.
- Choose the payment method per vendor: ACH or SEPA credit transfers (ISO 20022 pain.001), check, card or wire.
  Export positive-pay files for the bank.
- Ingest the bank's payment status (pain.002) and send payment advices to vendors.
- Handle prepayments, vendor credit notes and payment reversals.
- Manage vendor bank accounts under change control. A change needs dual approval, out-of-band notification to the
  vendor, and a cooling-off period before the first payment to the new account.
- Send vendor statements, payment notices and open-item lists, and run balance confirmations.

Behaviour to know: a payment run can be restarted after a crash. A run that was already paid is not paid twice,
because each payment carries an idempotency key. Aging buckets reconcile to the AP control account per vendor group.

## Wave 20: Inventory costing and supplier collaboration

Owning area: `services/costing`, with the supplier portal as a separate app in `apps/supplier-portal`.
Screens: `apps/web/src/modules/costing`.

What you do here:

- Choose the costing method per item group and ledger: FIFO, LIFO, weighted average (running or periodic) or standard
  cost. Standard cost records purchase price and other variances on receipt and invoice.
- Run the inventory close and recalculation. Issues settle against receipts, and the GL adjustments post.
- Read the inventory value report, which reconciles on-hand value to the inventory accounts by dimension. Use
  month-end snapshots and the aging report for trend review.
- Maintain costing versions and cost changes, and revalue stock when a cost changes.
- Keep parallel valuation ledgers when one group needs a second method or currency.
- Use split valuation and batch valuation (a valuation type dimension with its own accounts).
- Let suppliers self-serve in the supplier portal: acknowledge orders, send advance shipping notices, submit invoices,
  exchange documents, and register as prospective vendors with questionnaires.

Check before sign-off: the value report ties to the general ledger to the cent; standard cost variances on a sample of
receipts match the hand-calculated figures.

## Month-end sequence for this phase

1. Post all receipts and issues; complete counts.
2. Match open invoices and settle the approved payment proposals.
3. Run the inventory close and recalculation.
4. Reconcile the inventory value report and the GR/IR account to the general ledger.
5. Age the AP ledger and confirm the control account before the period closes (see the [phase 2 guide](phase-2-financial-core.md)).

Related: [phase 1 guide](phase-1-master-data.md) for the parties and products these documents use, and the
[implementer guide](../implementer-guide.md) for the parallel-run checks before cutover.
