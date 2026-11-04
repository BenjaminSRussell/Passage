-- Contract asset and liability (ASC 606-10-45, IFRS 15.105, #474): an invoice books the remaining revrec allocation of each
-- (order, product) it bills, and the difference to its billed nets moves the contract position of the order (2400 contract
-- liability when billed above allocated, the profile's contract asset account, 1250 by default, below). Both are summed at
-- posting time from the gl_split of the booked and held documents of the order.
--
-- gl_split is now written when a document is sent to the ledger, before the answer (a held booking): a retry after an answer
-- without verdict posts the same entry again, and the later documents of the contract count it as consumed. A verdict from the
-- ledger clears it. gl_contract keeps the contract position change of the booking: {"liability": credit to 2400 when positive,
-- "asset": debit to the contract asset account when positive}.

ALTER TABLE invoice ADD COLUMN IF NOT EXISTS gl_contract jsonb;

CREATE INDEX IF NOT EXISTS invoice_line_order_idx ON invoice_line (tenant_id, order_id, product_id)
  WHERE order_id IS NOT NULL;
