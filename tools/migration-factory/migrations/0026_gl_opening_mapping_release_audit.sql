-- Opening-journal target, explicit and audited (#549). A cutover manager records, per legacy company, the
-- legal entity and ledger its opening journal is booked to (gl_opening_mapping, under a cutover plan);
-- the first posting no longer decides the target for a mapped company. Releasing a pin that holds no
-- posting is an admin act and leaves an audit row (gl_opening_target_release).
create table gl_opening_mapping (
  tenant_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  plan_id uuid not null,
  target_legal_entity_id uuid not null,
  ledger_id text not null check (ledger_id <> ''),
  set_by text not null,
  set_at timestamptz not null default now(),
  primary key (tenant_id, source_system, source_company),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);
alter table gl_opening_mapping enable row level security;
alter table gl_opening_mapping force row level security;
create policy gl_opening_mapping_tenant_access on gl_opening_mapping as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table gl_opening_target_release (
  release_id uuid primary key,
  tenant_id uuid not null,
  source_system text not null,
  source_company text not null,
  released_legal_entity_id uuid not null,
  released_ledger_id text not null,
  reason text not null check (reason <> ''),
  released_by text not null,
  released_at timestamptz not null default now()
);
alter table gl_opening_target_release enable row level security;
alter table gl_opening_target_release force row level security;
create policy gl_opening_target_release_tenant_access on gl_opening_target_release as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON gl_opening_mapping TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON gl_opening_target_release TO passage_app';
  END IF;
END
$$;
