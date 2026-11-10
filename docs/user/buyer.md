# Raise a purchase order and receive goods

For buyers. You turn a need into a purchase order, get it approved, send it to the vendor and see the goods arrive.

## Before you start

- The vendor exists under **Parties**. See [Set up a company](tenant-admin.md).
- You know the product, quantity and delivery date.

## Ask for what you need

Use a requisition when someone else approves the buying.

1. Open **Purchasing** and choose **Requisitions**, then **New requisition**.
2. Pick the **Legal entity** and **Currency**.
3. Select **Add line**. Choose the **Product** (or type a **Description**), then enter **Quantity**, **Unit**, **Estimated unit price** and **Requested date**.
4. Select **Save requisition**, then **Submit for approval**.
5. Approvers decide under **Requisition approvals**. They can **Approve** or **Send back** (a comment is needed to send back).

Approved lines wait on the **Consolidation** page. Choose a vendor per line, then **Create purchase orders**, or **Start RFQ for selected lines** to ask vendors for prices.

## Raise a purchase order

1. Open **Purchasing**, choose **Purchase orders**, then **New purchase order**.
2. In the **Order** card, choose the **Legal entity**, **Vendor** and **Currency**.
3. Select **Add line**. Pick the **Product**, enter **Quantity** and **Unit**, and set the **Delivery date**.
4. Leave **Unit price** blank to use the price from the trade agreement, or type one.
5. Use **Add charge** for freight or other extra costs.
6. Select **Save purchase order**. It is saved as Draft.
7. On the order, select **Submit for approval**.
8. An approver opens the order and selects **Approve** or **Reject**. Rejected orders go back to you to edit and submit again.
9. Once approved, select **Confirm and send**. The vendor receives this version.

## Change a confirmed order

1. Open the order and select **Change order**, or **Update prices** to reprice.
2. A change of more than 5% to a quantity or price, or a new line, needs approval again.
3. You cannot lower a quantity below what is already received or invoiced.

Use **Cancel order** to cancel. Agreement releases and requisition lines are returned.

## Receive the goods

1. Open **Receiving**, choose **Receipts**, then **Record arrival**.
2. Enter the **Vendor ID**, **Purchase order ID** and **Legal entity ID**, plus the vendor's **Packing slip** number.
3. Choose the purchase order line and quantity for each line, then select **Record arrival**.
4. Register the quantities, then select **Post receipt** and **Confirm post**.

The invoice clerk takes it from here: [Enter and pay a vendor invoice](ap-clerk.md).

## What the statuses mean

| Status | Meaning |
| --- | --- |
| Draft | Being written. Not yet submitted. |
| In review | Waiting for an approver. |
| Approved | Ready to confirm and send. |
| Rejected | Sent back. Edit and submit again. |
| Confirmed | Sent to the vendor. |
| Finalized | Complete. |
| Cancelled | Stopped. |

Receipts: Arrived (recorded), Registered (quantities checked), Posted (goods in stock).

## Common messages

| Message | What to do |
| --- | --- |
| No price was found for this product. | Enter a unit price, or ask a buyer to set up a trade agreement for the product and vendor. |
| You can't approve or reject your own requisition. | Ask another approver to decide it. |
| Only the requester can change or submit this requisition. | Ask the requester, or create your own. |
| A line has no estimated price. | Enter an estimated unit price on each line. |
| Only an approved purchase order can be confirmed. | Get the order approved first, then confirm it. |
| A line quantity can't go below what has already been received or invoiced. | Keep the quantity at or above what has been received or invoiced. |
| Someone else changed this purchase order after you opened it. | Reload the page, then make your change again. |
| The purchase order isn't approved or confirmed. | Confirm the order before recording an arrival. |
| The purchase order is placed with another vendor. | Check the vendor on the arrival. |
| The receipt currency differs from the purchase order currency. | Use the order's currency. |

## Not available yet

- **Pick-lists on receiving.** Vendor, purchase order and legal entity are typed in as ids.
- **Paying the vendor.** See [Enter and pay a vendor invoice](ap-clerk.md#not-available-yet).

## Glossary

- Requisition: an internal request to buy.
- RFQ: a request to vendors for a price.
- Trade agreement: agreed prices for a vendor and product.
- Receipt: the record that goods arrived.

More guides: [All guides](README.md).
