-- Outbox writers can only create events for the tenant in their transaction.
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Child rows inherit legal-entity scope through their owning contract.
CREATE POLICY performance_obligation_legal_entity_scope ON performance_obligation AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM revenue_contract c
      WHERE c.tenant_id = performance_obligation.tenant_id
        AND c.id = performance_obligation.contract_id
        AND c.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM revenue_contract c
      WHERE c.tenant_id = performance_obligation.tenant_id
        AND c.id = performance_obligation.contract_id
        AND c.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );

CREATE POLICY revenue_schedule_legal_entity_scope ON revenue_schedule AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM performance_obligation o
      WHERE o.tenant_id = revenue_schedule.tenant_id
        AND o.id = revenue_schedule.obligation_id
    )
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM performance_obligation o
      WHERE o.tenant_id = revenue_schedule.tenant_id
        AND o.id = revenue_schedule.obligation_id
    )
  );
