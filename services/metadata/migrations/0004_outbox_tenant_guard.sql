ALTER POLICY outbox_tenant_access ON outbox
  USING (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
