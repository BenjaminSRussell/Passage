-- Application role privileges. passage_app (no BYPASSRLS) works under the row-level
-- security policies. outbox, idempotency_keys and project_change_order_line are
-- append-only. Skipped when the role does not exist (developer databases without the
-- role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys, project_change_order_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON project, project_group, posting_profile, plan_version, wbs_node, wbs_dependency, project_change_order, project_risk_issue TO passage_app';
  END IF;
END
$$;
