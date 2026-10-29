-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. Tables the service updates carry UPDATE; the rest are insert-only.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, budget_control_rule, budget_register_entry, budget_check_result, budget_reservation_consumption, investment_carry_forward TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_control_balance TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON encumbrance TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON budget_reservation_number TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON program_position TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON investment_program TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON program_assignment TO passage_app';
  END IF;
END
$$;
