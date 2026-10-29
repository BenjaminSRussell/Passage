-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. Tables the service updates carry UPDATE; the rest are insert-only.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, cost_object, cost_element, cost_entry, top_down_run, internal_order_settlement, statistical_measure, allocation_template_version, template_run, cost_behavior_rule, cost_object_hierarchy, cost_hierarchy_node, hierarchy_distribution_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON allocation_template TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON allocation_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON statistical_measure_value TO passage_app';
  END IF;
END
$$;
