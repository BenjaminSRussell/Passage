# Take an order through to invoice

For the order desk. You enter a customer's order, confirm it, get it shipped and billed.

## Before you start

- The customer and the products exist. See [Set up a company](tenant-admin.md).
- You know the legal entity, customer, products, quantities and requested ship date.

## Enter the order

1. Open **Sales** and choose **Orders**, then **New sales order**.
2. Fill in **Legal entity** and **Customer**. These are typed in as ids, not picked from a list.
3. Check **Invoice account**, **Order type** and **Currency**.
4. Set the **Requested ship date** and, if the customer gave one, the **Customer reference**.
5. Tick **One-time customer** only for a customer who orders once.
6. Add each line: **Product**, **Quantity**, **Unit**, **Unit price**, **Discount %**, **Site**, **Warehouse** and a line **Requested ship date**.
7. Select **Save draft order**. The order is saved as a draft.

## Confirm the order

1. Open the order and select **Confirm order**.
2. The credit check runs. If it fails, a credit hold is placed: "The credit check failed, so a credit hold was placed. A credit manager must release it."
3. A credit manager opens **Credit release queue** and releases or rejects the hold. A comment is needed to reject.
4. Other holds (fraud, manual, price review) show in the **Holds** card. Use **Release** when you are allowed to, or **Apply hold** with a reason to stop an order.

To change or cancel, select **Change or cancel**. Use **Cancel the remainder** to drop what has not shipped.

## Ship it

1. Open **Fulfillment** and choose **Release lines**. Enter the warehouse and the order lines to release.
2. Open **Picking workbench** to group lines and create pick waves. Under **Pick waves**, assign a picker and confirm the picks.
3. Open the shipment from **Shipments**. Select **Confirm pick** for each line. Use **Pick the remainder** with a short-pick reason if you could not find everything.
4. Pack with **Auto-pack by packing rule**, or **Pack a container** and **Close container**.
5. Select **Post packing slip** to ship. Check the **Journal preview** first.

## Invoice it

1. Open **Billing** and select **Invoice shipments**.
2. Enter the legal entity, customer, invoice account, currency, invoice date and payment terms.
3. Add each packing slip and its lines, then select **Create proposals**.
4. Open the proposal and select **Post invoice**. The invoice gets its number and goes to receivables and the ledger.
5. To undo part of it, select **Credit invoice**.

After a sale, record what the customer pays in [Record a customer payment](ar-clerk.md).

## What the statuses mean

| Order status | Meaning |
| --- | --- |
| Draft | Saved, not confirmed. |
| Open | Entered and waiting. |
| Confirmed | Passed the credit check. |
| Partially shipped | Some lines left. |
| Shipped | All lines left. |
| Invoiced | Billed. |
| Closed | Finished. |
| Cancelled | Stopped. |

| Shipment status | Meaning |
| --- | --- |
| Open, Released | Waiting to be picked. |
| Picking, Picked | Being gathered. |
| Packed | Waiting to ship. |
| Shipped, Delivered | On its way or arrived. |
| Cancelled | Stopped. |

| Invoice status | Meaning |
| --- | --- |
| Proposal | Drafted, not posted. |
| Posted | Numbered and sent on. |
| Partially settled, Settled | Paid in part or in full. |
| Reversed | Undone. |

## Common messages

| Message | What to do |
| --- | --- |
| The order is on credit hold. | Ask credit management to release the hold. |
| A product isn't listed for this customer. | Add the product to the customer's listing, or ask for an override. |
| The order is over the one-time customer limit. | Set the customer up as a regular customer. |
| The order can't be confirmed in its current state. | Open the order and check its status and holds. |
| The requested date is in the past. | Choose a date from today. |
| The shipment or order isn't in a state that allows this step. | It may not be packed yet, or an active hold blocks it. |
| The picked quantity is more than was planned for the line. | Pick no more than the planned quantity. |
| A batch or serial number is needed for the quantity being shipped. | Enter the batch or serial numbers. |
| The packed quantity is more than the picked quantity. | Pack only what was picked. |
| The tax moved by more than the tolerance since the invoice was proposed. | Review the invoice, then post it again. |
| An invoice line has no revenue account. | Enter a revenue account on the line. |
| The invoice date is before the packing slip date. | Use a date on or after the packing slip. |
| A packing slip line is already on another invoice. | Remove it from this proposal. |

## Not available yet

- **Pick-lists for customer, legal entity and product** on the order form. They are typed in as ids.
- **Automatic hand-off to the warehouse.** Release lines and Invoice shipments need order, shipment and packing slip details typed in by hand. Confirmed order lines and posted packing slips do not carry over yet.
- **Plain names for recording a delivery or invoice on an order.** The screen asks for access by an internal name.
- **Unit cost on the packing slip** is typed in by hand.

## Glossary

- Credit hold: a stop placed when a customer is over their limit.
- Pick wave: a batch of lines picked together.
- Packing slip: the shipping document posted when goods leave.
- Credit note: an invoice that reduces what the customer owes.

More guides: [All guides](README.md).
