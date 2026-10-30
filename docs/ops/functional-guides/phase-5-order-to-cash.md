# Functional guide: Phase 5, Order-to-Cash

Waves 26-30. Audience: sales operations, customer service, credit and collections staff, billing clerks and the
accounts receivable lead. An order is priced, checked for credit and stock, shipped, invoiced, and collected. The
guide follows that path. Phase 5 ends the first manufacturer go-live loop: buy, make, sell and collect for one month
with every subledger reconciled to the GL.

This guide describes intended behaviour from the phase 5 PRD (`docs/prd/phase-5-order-to-cash.md`). It is not a
build status. Each wave has its own service and web module, named at the top of its section; what is built is in
that service's `README.md` and what is not is in its `NOT-IMPLEMENTED.md`, which is authoritative where it and this
guide differ. Where a later wave completes an item (advanced warehouse work in wave 31, EDI in wave 35, intercompany
invoicing in wave 40), the text says so.

## Wave 26: Pricing, discounts and trade agreements

Owning area: `services/pricing`. Screens: `apps/web/src/modules/pricing`.

What you do here:

- Set up price lists, discounts and surcharges as condition tables. An access sequence searches from the most specific
  agreement to the most general one.
- Run promotions and campaigns with validity dates, customer and product groups, quantity breaks and
  currency-specific prices.
- Set up rebate agreements. Rebates accrue on invoices and settle in wave 30.
- Simulate a price before it goes live, and ask the engine to explain which conditions applied.
- Run a mass price update. The update is atomic and can be reversed.
- Record vendor rebates, broker agreements and royalties. Accruals post on purchase and sales invoices, and the period
  claims settle through accounts payable.
- Plan trade promotion funds against a baseline, with lift and actuals.

Rules you can rely on:

- The same inputs give the same price every time, with an explain trace of every access tried (FR-26.4).
- Each line's net amount is rounded half-even to the currency's minor units, and document totals are the sums of the
  rounded lines; a percentage total discount rounds once on the total (BR-26.3).
- A negative net price is refused (PRC-0003); a 100% discount gives 0.00 with its trace.

## Wave 27: Sales orders, quotations, ATP and credit

Owning area: `services/sales`. Screens: `apps/web/src/modules/sales`.

What you do here:

- Quote a customer, then convert the quotation to an order. Sales agreements, scheduling agreements and one-time
  customer orders use the same order types.
- Check availability (ATP). Cumulative ATP counts available on-hand stock plus scheduled receipts less scheduled issues
  inside the time fence. A line is promised on the first date whose cumulative ATP covers it (BR-27.1). When a line is
  short, the order shows the backorder and any alternative items.
- Let the credit check run when an order is saved. An order that exceeds the customer's limit or aging rule is held,
  and a credit controller releases it.
- Place order holds for credit, fraud, price review or a manual reason. Change an order after confirmation through change
  management. Each line shows its own status.
- Use product allocation quotas and a backorder strategy (win, gain, redistribute, fill or lose) when stock is
  scarce. Preview the strategy before you apply it.
- Track consignment stock at the customer. Fill-up, issue, pick-up and return movements are recorded, and special
  stock segregation keeps the customer's stock apart.
- Configure a product at order entry. The order links to the production order or the planned order the product needs.

Rules you can rely on:

- A credit or fraud hold blocks confirmation and release; a manual or price-review hold blocks release only. A credit
  release is valid for 7 days, and an amount increase of more than 5% runs the credit check again (BR-27.4).
- Confirmation reserves the promised stock when the entity reserves on confirm (the default, BR-27.7).
- A change needs the current version (If-Match); a concurrent confirm and change gives the later call a conflict.

## Wave 28: Fulfilment: picking, packing and shipping

Owning area: `services/fulfillment`. Screens: `apps/web/src/modules/fulfillment`.

What you do here:

- Release orders to the warehouse. Release rules decide which lines go and when.
- Pick in waves or batches (basic picking; advanced warehouse work is in wave 31). Confirm each pick. Pack into
  containers and print SSCC labels.
- Post the packing slip or delivery note. Posting deducts inventory and books the cost of goods sold to the
  universal journal. A cancelled shipment reverses both.
- Ship drop-ship and direct-delivery lines, partial shipments and delivery schedules.
- Use the carrier interface to get rates, labels and tracking numbers. The advance shipping notice (ASN) is sent from
  the shipment.
- Track the shipment in the customer portal.
- Keep packaging material and returnable transport packaging balances per customer. The packaging fee report lists the
  EU packaging fees due.

Check before sign-off: the packing slip posting is balanced (inventory to COGS); a cancelled shipment reverses cleanly;
the SSCC check digit and the GS1-128 barcodes validate.

## Wave 29: Billing, invoicing and revenue recognition

Owning area: `services/billing`, with revenue contracts and schedules in `services/revrec`.
Screens: `apps/web/src/modules/billing`.

What you do here:

- Invoice from shipments or orders. Free-text invoices, consolidated invoices and credit notes follow the same posting
  rules.
- Set up performance obligations and the standalone selling price for bundles. Revenue schedules run ratably, on
  milestones or on usage. Deferred revenue posts to the deferred revenue account.
- Run subscriptions with recurring schedules, proration on start and cancellation, and usage lines. Proration is
  daily, including 29 February; a mid-period plan change credits the unused days of the old plan and bills the new
  plan's remaining days on one invoice (BR-29.8).
- Trace any invoice back to its shipment, its order and its price explanation (billing audit).
- Route each document to the customer by e-mail, portal, print or EDI, and keep a delivery log with resend.
- Split installment terms into separate open items when the invoice posts.

Check before sign-off: the invoice total equals the lines, charges and tax; the deferred revenue roll-forward ties to
the GL deferred revenue account. Intercompany invoicing completes in wave 40.

## Wave 30: Accounts receivable, collections, returns and the customer portal

Owning area: `services/receivables`, with the customer portal in `apps/customer-portal`.
Screens: `apps/web/src/modules/receivables`.

What you do here:

- Apply cash. Match payments by hand, by lockbox file (BAI2), by remittance advice, or from the bank statement
  (camt.053). Suggested matches are checked by a person before they post.
- Run dunning. Levels, grace days and blocks decide which customers get a letter. Record promises to pay, open
  disputes, and interest notes.
- Handle deductions and chargebacks. Each claim is matched, approved, denied, split or written off.
- Process a return order (RMA). The disposition decides whether the goods go back to stock, to scrap or to repair.
  The credit note posts to inventory and to receivables.
- Settle the rebate accruals from wave 26 and send customer statements and balance confirmations.
- Collect by SEPA direct debit. Mandates and pre-notification come first; returned debits are handled as
  R-transactions.
- Let customers view orders, invoices, payments, returns and documents in the portal, and pay by card through the
  payment adapter.

Check before sign-off (phase 5 exit): the demo manufacturer runs buy, make, sell and collect for one simulated month,
and every subledger reconciles to the GL.

## Month-end sequence for this phase

1. Ship and invoice all completed deliveries. Post the revenue schedules.
2. Apply the cash received and clear the open items that match.
3. Run the dunning cycle and confirm the blocked accounts.
4. Reconcile the AR subledger, the deferred revenue roll-forward and the rebate accruals to the GL.
5. Confirm the sales order backlog and the open holds, and release or cancel stale holds.

Related: [phase 3 guide](phase-3-procure-to-pay.md) for the purchasing side of the same loop,
[phase 4 guide](phase-4-manufacturing.md) for the production that feeds demand, and the
[phase 2 guide](phase-2-financial-core.md) for the period close the AR control account must pass.
