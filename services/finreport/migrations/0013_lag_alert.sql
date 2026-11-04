-- PSG-1511 (wave 15, slice 11): read-model lag alert (FR-15.1, PRD §2.15.5).
-- One row per ledger whose read model is behind by more than the lag gate. The
-- row opens when the lag is first seen above the threshold and is deleted when
-- it recovers; alerted_at records that dev.passage.reporting.lag.exceeded.v1 was
-- written for this episode, so a long outage raises one event, not one per check.
CREATE TABLE IF NOT EXISTS reporting_lag_alert (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  first_exceeded_at timestamptz NOT NULL,
  alerted_at timestamptz,
  legal_entity_id uuid,
  PRIMARY KEY (tenant_id, ledger_id)
);
ALTER TABLE reporting_lag_alert ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_lag_alert FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_lag_alert_tenant_access ON reporting_lag_alert AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY reporting_lag_alert_legal_entity_scope ON reporting_lag_alert AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The check inserts a row, stamps alerted_at (UPDATE) and deletes the row on recovery.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON reporting_lag_alert TO passage_app';
  END IF;
END
$$;
