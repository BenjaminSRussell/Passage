# Record a customer payment

For accounts receivable clerks. You record money from a customer and settle it against their open invoices.

## Before you start

- The customer's invoices are posted and show under **Open items**.
- You know how the customer paid and the amount received.

## Record the payment

1. Open **Receivables** and choose **Payments**, then **Record payment**.
2. Choose the **Legal entity**. Choose the **Customer** if you know who paid.
3. Pick the **Payment mode**: Check, ACH, Wire, Card or Cash.
4. Enter the **Amount** and **Currency**.
5. Enter the **Received date** and the **Value date**.
6. Open **More options** to add the **Check or bank reference**, **Payer name** and **Payer bank account** if they are on the remittance.
7. If the customer paid less than billed, give the **Short-pay reason** under **More options**.
8. If the customer listed invoices, select **Add reference** for each.
9. Select **Record payment**. The payment starts as Received.

## Settle it against invoices

1. Open the payment from **Payments**.
2. In **Match candidates**, select **Find matches**. Choose **Use ...** next to a suggestion to fill in the plan.
3. In **Open items to settle**, check each row. Change **Apply** for the amount that goes to that invoice.
4. Add **Cash discount** or **Write-off** where it applies.
5. Select **Apply payment**. You see "Payment applied".
6. Anything left over stays on account. Use **Clear plan** to start again.

## Look at what is owed

- **Open items** lists unpaid invoices. Filter by **Customer** or **Show**.
- **Payments** lists every payment. Filter by status.

## Import a remittance advice

A remittance advice is the customer's own list of the invoices a payment covers. Importing it puts those invoice numbers on the payment, so the matcher can settle it without you.

1. Open **Remittance advice**.
2. Choose the **Legal entity** and the **Format** (CSV, JSON or EDI 820), then choose the file or paste its content.
3. Tick **Apply the payments the matcher is sure of** if you want the clear matches settled at once.
4. Tick **Record lines with no bank credit yet as advised payments** when the advice arrives before the money does. Enter the **Currency** of those payments.
5. Select **Import advice**. The result lists each payment reference and what happened to it.

An advised payment books no cash and cannot be applied. When the bank credit arrives with the same reference, currency and amount, it takes the advice's invoice numbers and the advised payment is marked Confirmed.

## Check the journals posted to the ledger

**Ledger journals** lists what receivables owes the general ledger: cash received, payments applied, fees, interest and write-offs.

- **Posted** journals are in the ledger.
- **Waiting** journals are posted automatically at the next pass. Select **Post due journals** to post them now.
- **Parked** journals were refused by the ledger, usually because the period is closed. Open the journal, read the last problem, and select **Retry posting**. Enter a **New posting date** if the original period is closed.

## What the statuses mean

| Status | Meaning |
| --- | --- |
| Received | Recorded, not yet settled. |
| Suggested | Matches were found for it. |
| Applied | Fully settled against invoices. |
| On account | Money held for the customer, not tied to an invoice. |
| Reversed | Undone. |
| Advised | Listed on a customer's remittance advice, but the bank credit has not arrived. It books no cash and cannot be applied. |
| Confirmed | An advised payment whose bank credit has arrived. The bank payment is the one to apply. |

Only Received, Suggested and On account payments can be applied.

## Common messages

| Message | What to do |
| --- | --- |
| An open item belongs to another legal entity. | Settle it from a payment recorded for that legal entity. |
| Another user has locked an open item. | Wait a moment and try again, or ask who is working on it. |
| The write-off is more than the tolerance allows. | Lower the write-off, or ask a manager. |
| Someone else changed this record after you opened it. | Reload the page to see the latest version, then make your change again. |
| You don't have permission to do this. | Ask an administrator for access if you need it. |

## Not available yet

- **Plain match explanations.** The match card shows rule numbers and scores meant for specialists.

## Glossary

- Open item: an unpaid invoice or debit.
- On account: money received but not yet applied to an invoice.
- Short pay: the customer paid less than billed.
- Value date: the day the bank counts the money.

More guides: [Take an order through to invoice](order-desk.md), [All guides](README.md).
