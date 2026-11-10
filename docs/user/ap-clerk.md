# Enter and pay a vendor invoice

For accounts payable clerks. You match a vendor's invoice to goods that have arrived, post it, and (when payment runs arrive) pay it.

## Before you start

- The purchase order was approved and sent to the vendor. See [Raise a purchase order and receive goods](buyer.md).
- The goods have arrived and the receipt is posted, so they show up as open items waiting for an invoice.
- You have access to Vendor invoices. If a menu item is missing, ask an administrator.

## Record the goods arriving

Skip this if the warehouse already posted the receipt.

1. Open **Receiving** and choose **Receipts**, then **Record arrival**.
2. Fill in **Vendor ID**, **Purchase order ID** and **Legal entity ID**. These are typed in, not picked from a list.
3. Enter the **Packing slip** number from the vendor. Each packing slip can be used once per vendor.
4. Add the **Receipt date** and **Currency**. Carrier and pallet count are optional.
5. For each line, choose the purchase order line and enter the quantity.
6. Select **Record arrival**. The receipt starts as Arrived.
7. On the receipt, fill in the quantity received, location and stock status, then register it.
8. Select **Post receipt**, then **Confirm post**. Posting adds the goods to stock and opens an item waiting for the invoice.

A posted receipt can be corrected with **Correct quantities** or removed with **Cancel receipt**.

## Enter a vendor invoice

1. Open **Vendor invoices** and choose **GR/IR open items**. This lists goods received but not yet invoiced.
2. Filter by **Vendor**, **Status** or **Older than (days)** to find the lines on the invoice.
3. Tick the lines the invoice covers. All lines must be from one vendor and one currency.
4. Select **Invoice selected items**. The **Post vendor invoice** form opens with the vendor, currency and lines filled in.
5. Enter the **Invoice number** exactly as printed. It must be unique for the vendor.
6. Check the **Invoice date** and **Posting date**. The posting date must fall in an open payables period.
7. Check each line's quantity and unit price against the paper invoice.
8. Select **Post invoice**. You see "Invoice ... posted". Choose **Post another invoice** to carry on.

To start without the open items list, open **Vendor invoices** and select **Post vendor invoice**, then add each line with **Add line**. You need the receipt and receipt line for each one.

## Pay the invoice

Not available yet. See below.

## What the statuses mean

| Status | Meaning |
| --- | --- |
| Posted | The invoice is booked to the ledger. |
| Held for audit | Nothing posts to the ledger until an auditor releases it. |
| Rejected after audit | An auditor turned the invoice down. |

Open items on the goods-received list:

| Status | Meaning |
| --- | --- |
| Open | Goods received, no invoice yet. |
| Partial | Part of the quantity is invoiced. |
| Cleared | Fully invoiced. |
| Written off | Closed without an invoice. |

## Common messages

| Message | What to do |
| --- | --- |
| This invoice number has already been posted for the vendor. | Check the vendor invoice list. If it is a new invoice, use its own number. |
| The invoice quantity is more than has been received and not yet invoiced. | Lower the quantity, or ask the warehouse to post the missing receipt. |
| The invoice is held for audit, so it can't be changed. | Ask an auditor to release it first. |
| Someone else changed this record after you opened it. | Reload the page to see the latest version, then make your change again. |
| You don't have permission to do this. | Ask an administrator for access if you need it. |
| This packing slip was already used on another receipt. | Look for the earlier receipt before recording a new one. |
| The quantity is more than the purchase order line allows. | Receive no more than the open quantity plus its tolerance. |
| Select items from one vendor and one currency to put on the same invoice. | Untick items until only one vendor and currency remain. |

## Not available yet

- **Paying vendors.** The Payment runs page is a placeholder. You cannot yet review invoices that are due, choose what to pay or release a payment from the app.
- **Pick-lists for ids.** Vendor, purchase order, legal entity, ledger and location are typed in as ids on the receiving and invoice forms.

## Glossary

- Goods received, not invoiced (GR/IR): goods that arrived and are waiting for an invoice.
- Packing slip: the vendor's delivery note that comes with the goods.
- Posting date: the date the invoice lands in your books.

More guides: [Raise a purchase order and receive goods](buyer.md), [Set up a company](tenant-admin.md), [All guides](README.md).
