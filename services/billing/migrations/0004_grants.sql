-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it); invoice_line,
-- invoice_installment and proposal_batch are append-only; invoices and the number
-- sequence are updated but never deleted. Skipped when the role does not exist
-- (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON invoice_line, invoice_installment, proposal_batch TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON invoice, number_sequence_counter TO passage_app';
  END IF;
END
$$;
