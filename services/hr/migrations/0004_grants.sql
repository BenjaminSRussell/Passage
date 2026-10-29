DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON audit_event, worker_pii, employment, position_hierarchy, position_assignment, absence_plan, absence_accrual_period, absence_accrual_line, absence_close_line, benefit_plan, benefit_plan_option, benefit_life_event TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON worker, hr_position, absence_balance, absence_request, benefit_enrollment TO passage_app';
  END IF;
END
$$;
