-- revenue_schedule checks the owning contract's legal entity directly instead of
-- relying on performance_obligation's policy applying inside the subquery.
-- The outbox is not exempt from RLS: 0004 scopes it to the transaction tenant.
DROP POLICY revenue_schedule_legal_entity_scope ON revenue_schedule;

CREATE POLICY revenue_schedule_legal_entity_scope ON revenue_schedule AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1
      FROM performance_obligation o
      JOIN revenue_contract c ON c.tenant_id = o.tenant_id AND c.id = o.contract_id
      WHERE o.tenant_id = revenue_schedule.tenant_id
        AND o.id = revenue_schedule.obligation_id
        AND c.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1
      FROM performance_obligation o
      JOIN revenue_contract c ON c.tenant_id = o.tenant_id AND c.id = o.contract_id
      WHERE o.tenant_id = revenue_schedule.tenant_id
        AND o.id = revenue_schedule.obligation_id
        AND c.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );
