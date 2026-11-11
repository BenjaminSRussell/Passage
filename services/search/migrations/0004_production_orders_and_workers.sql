-- #506-2: production orders are searchable under their legal entity, like the
-- other documents; workers are tenant-wide (HR's worker list is not
-- legal-entity scoped), like parties and products.
alter table search_record drop constraint search_record_record_type_check;
alter table search_record add constraint search_record_record_type_check
  check (record_type in ('purchase_order', 'receipt', 'vendor_invoice', 'sales_order', 'packing_slip', 'customer_invoice', 'production_order'));
alter table search_record_tenant drop constraint search_record_tenant_record_type_check;
alter table search_record_tenant add constraint search_record_tenant_record_type_check
  check (record_type in ('party', 'product', 'worker'));
