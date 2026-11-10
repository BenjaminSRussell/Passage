-- #506-2: receipts, vendor invoices, sales orders, packing slips and customer
-- invoices are searchable. Each belongs to one legal entity, so it lives in
-- search_record under the same tenant and legal-entity row-level security.
alter table search_record drop constraint search_record_record_type_check;
alter table search_record add constraint search_record_record_type_check
  check (record_type in ('purchase_order', 'receipt', 'vendor_invoice', 'sales_order', 'packing_slip', 'customer_invoice'));
