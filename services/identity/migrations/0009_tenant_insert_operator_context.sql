-- Tenant creation is an operator-only act. The bare passage_app INSERT grant on identity_tenants let any
-- tenant-scoped session insert its own tenant row; this RESTRICTIVE policy additionally requires the
-- transaction to carry passage.operator_context naming a row of identity_operator (set only by the tenant
-- create handler after it has verified the caller is the operator tenant).
create policy identity_operator_context on identity_operator as permissive for select
  using (tenant_id = nullif(current_setting('passage.operator_context', true), '')::uuid);

create policy identity_tenants_operator_insert on identity_tenants as restrictive for insert
  with check (exists (
    select 1 from identity_operator
    where tenant_id = nullif(current_setting('passage.operator_context', true), '')::uuid));
