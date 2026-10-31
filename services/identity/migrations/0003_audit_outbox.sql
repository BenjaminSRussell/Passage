create table identity_audit (
  tenant_id uuid not null,
  audit_id uuid not null,
  actor text not null check (actor <> ''),
  action text not null,
  entity_type text not null,
  entity_id uuid not null,
  detail jsonb not null,
  occurred_at timestamptz not null default now(),
  primary key (tenant_id, audit_id)
);
create index identity_audit_entity_idx on identity_audit (tenant_id, entity_type, entity_id, occurred_at);

create table outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);

alter table identity_audit enable row level security;
alter table identity_audit force row level security;
create policy identity_audit_tenant_access on identity_audit as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

alter table outbox enable row level security;
alter table outbox force row level security;
create policy outbox_tenant_access on outbox as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON identity_audit, outbox TO passage_app';
  END IF;
END
$$;
