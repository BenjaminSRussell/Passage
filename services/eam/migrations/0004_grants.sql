-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. The outbox is insert-only (Debezium relays it).
-- criticality_assessment, counter_reading, maintenance_request_triage, asset_warranty,
-- downtime_window, maintenance_plan, work_order_fault, work_order_labor and
-- work_order_transition are append-only. Tables the store updates or row-locks
-- (FOR UPDATE needs UPDATE) get SELECT, INSERT, UPDATE; fault codes and
-- combinations are replaced wholesale on a catalog edit, so they also get DELETE.
-- Asset and work-order numbers come from sequences. work_order_part and
-- idempotency_keys are not used by this service. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON criticality_assessment, counter_reading, maintenance_request_triage, asset_warranty, downtime_window, maintenance_plan, work_order_fault, work_order_labor, work_order_transition TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON asset, counter, plan_line, work_order, maintenance_request, condition_rule, fault_catalog TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON fault_code, fault_combination TO passage_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE asset_number_seq, work_order_number_seq TO passage_app';
  END IF;
END
$$;
