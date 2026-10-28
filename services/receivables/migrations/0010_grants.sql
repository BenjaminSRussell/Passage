-- Application role privileges. passage_app (no BYPASSRLS) reads and writes through the
-- RLS policies; it never owns the tables and never deletes. The tables below the first
-- grant are append-only (SELECT, INSERT); the rest are also updated in place. Skipped when
-- the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON open_item, payment, collection_case, balance_confirmation, down_payment, customer_balance_snapshot, dispute, dunning_run, dunning_letter, deduction TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON settlement, outbox, idempotency_keys, balance_confirmation_run, lockbox_batch, down_payment_clearing, payer_account_binding, payer_name_binding, promise_to_pay, interest_note, bank_statement_import TO passage_app';
  END IF;
END
$$;
