-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it). idempotency_keys,
-- commission_rule, commission_transaction, rebate_accrual, sales_group, sales_group_rep
-- and trade_fund_usage are append-only. Tables the store updates or row-locks
-- (FOR UPDATE / FOR SHARE / FOR NO KEY UPDATE need UPDATE) get SELECT, INSERT, UPDATE;
-- rebate_agreement is only locked, never updated. price_journal_line is replaced
-- wholesale on a journal edit, so it also gets DELETE. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON idempotency_keys, commission_rule, commission_transaction, rebate_accrual, sales_group, sales_group_rep, trade_fund_usage TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON campaign, condition_record, price_journal, pricing_procedure, promotion_plan, rebate_agreement, rebate_period, trade_fund, vendor_rebate_claim TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON price_journal_line TO passage_app';
  END IF;
END
$$;
