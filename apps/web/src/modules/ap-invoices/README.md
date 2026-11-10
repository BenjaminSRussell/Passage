# AP invoices

Goods received and not yet invoiced (GR/IR), posting a vendor invoice, the vendor invoice list and the invoice detail.

- List (`GET /v1/receiving/invoices`): posted, held and rejected invoices with a Due date column. Below it, documents captured but not yet posted (`GET /v1/receiving/invoice-captures`, status Captured or Needs review) with the reasons in words.
- Due date: receiving stores none, so it comes from the payables open item (`GET /v1/payables/open-items?source_invoices=`, read through `shell/api/openItems.ts`). "(no payment terms yet)" marks a date that is only the invoice date. A user without `open-item.read` sees "Not available to you".
- Detail (`GET /v1/receiving/invoices/{id}`): lines, the match with the receipt and payment status. The match is computed on read with the default tolerances, so it is not a saved decision. Payment status (not due, due, in a payment proposal, paid, on hold with reason) comes from the open item and links to the proposal when the user holds `payment-proposal.read`; without `open-item.read` it says "Payment status is not available to you".

## Not yet built

- Posting a captured document as an invoice from the list (a person still posts it by hand).
- A per-entity matching policy; the default tolerances apply to everyone.
