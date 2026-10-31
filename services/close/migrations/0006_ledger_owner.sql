create table ledger_owner (
  tenant_id uuid not null,
  ledger_id uuid not null,
  legal_entity_id uuid not null,
  primary key (tenant_id, ledger_id)
);
alter table ledger_owner enable row level security;
alter table ledger_owner force row level security;
create policy ledger_owner_tenant_access on ledger_owner as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
insert into ledger_owner(tenant_id, ledger_id, legal_entity_id)
  select distinct on (tenant_id, ledger_id) tenant_id, ledger_id, legal_entity_id from period_status
  order by tenant_id, ledger_id, legal_entity_id
on conflict do nothing;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON ledger_owner TO passage_app';
  END IF;
END
$$;
