DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON open_item, payment, settlement, outbox, idempotency_keys, collection_case, balance_confirmation_run, balance_confirmation, lockbox_batch, down_payment, down_payment_clearing, payer_account_binding, payer_name_binding, customer_balance_snapshot, dispute, promise_to_pay, dunning_run, dunning_letter, interest_note, bank_statement_import, deduction TO passage_app';
  END IF;
END
$$;
