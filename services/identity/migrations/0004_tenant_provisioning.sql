create table identity_operator (
  tenant_id uuid primary key references identity_tenants (tenant_id)
);

alter table identity_operator enable row level security;
alter table identity_operator force row level security;
create policy identity_operator_self on identity_operator as permissive for select
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

create policy identity_tenants_operator_read on identity_tenants as permissive for select
  using (exists (select 1 from identity_operator));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT ON identity_operator TO passage_app';
    EXECUTE 'GRANT INSERT ON identity_tenants TO passage_app';
  END IF;
END
$$;
