-- Migration factory, wave 49 slice 6 (integration track S8, decisions B4 and B6): load the
-- open_items cutover snapshot into the receivables service. A run that names a target stages its
-- accepted rows (target_row); the load step sends them to POST /v1/receivables/open-items:batch,
-- records each row's outcome and the open-item id the target assigned, and reconciles the totals
-- against GET /v1/receivables/open-item-imports (target_load). target_loaded remembers what the
-- target last accepted per (import source, legacy key), so a rerun of an unchanged snapshot sends
-- nothing. Every table carries tenant_id first and is held to the tenant by forced row-level
-- security; all three carry legal_entity_id and are also held to the caller's legal-entity scopes.

create table target_load (
  tenant_id uuid not null,
  run_id uuid not null,
  target text not null check (target in ('receivables')),
  legal_entity_id uuid not null,
  import_source text not null check (import_source <> '' and length(import_source) <= 40),
  status text not null check (status in ('STAGED', 'LOADING', 'PARTIAL', 'UNRECONCILED', 'MISMATCH', 'RECONCILED', 'FAILED')),
  staged int not null check (staged >= 0),
  created int not null default 0 check (created >= 0),
  updated int not null default 0 check (updated >= 0),
  unchanged int not null default 0 check (unchanged >= 0),
  skipped int not null default 0 check (skipped >= 0),
  rejected int not null default 0 check (rejected >= 0),
  not_available int not null default 0 check (not_available >= 0),
  pending int not null default 0 check (pending >= 0),
  attempts int not null default 0 check (attempts >= 0),
  last_error text not null default '',
  lease_until timestamptz,
  started_by text not null default '',
  started_at timestamptz,
  finished_at timestamptz,
  version int not null default 1 check (version >= 1),
  created_at timestamptz not null default now(),
  primary key (tenant_id, run_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);
create index target_load_status on target_load (tenant_id, status);

-- One staged row per accepted record of the run; payload is the receivables row as it is sent.
-- PENDING rows are sent; SKIPPED rows equal what the target already accepted (a rerun of an
-- unchanged snapshot); NOT_AVAILABLE rows are open AP, which payables cannot take yet. The row
-- carries its load's legal entity so it is held to the caller's legal-entity scopes like the load.
create table target_row (
  tenant_id uuid not null,
  run_id uuid not null,
  legal_entity_id uuid not null,
  seq int not null check (seq >= 1),
  legacy_id text not null check (legacy_id <> ''),
  ledger text not null check (ledger in ('AR', 'AP')),
  row_hash text not null,
  payload jsonb not null,
  status text not null check (status in ('PENDING', 'LOADED', 'UNCHANGED', 'SKIPPED', 'REJECTED', 'NOT_AVAILABLE')),
  outcome text not null default '',
  passage_id uuid,
  code text not null default '',
  detail text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, run_id, seq),
  unique (tenant_id, run_id, legacy_id),
  foreign key (tenant_id, run_id) references target_load (tenant_id, run_id)
);
create index target_row_status on target_row (tenant_id, run_id, status, seq);

-- What the target last accepted for a legacy key: the basis of "nothing new to load" and of the
-- expected side of the reconciliation. Amounts are magnitudes with the side, as receivables takes them.
create table target_loaded (
  tenant_id uuid not null,
  import_source text not null,
  legacy_id text not null,
  legal_entity_id uuid not null,
  currency char(3) not null,
  side text not null check (side in ('DEBIT', 'CREDIT')),
  open_amount numeric(28,6) not null check (open_amount > 0),
  row_hash text not null,
  passage_id uuid not null,
  run_id uuid not null,
  loaded_at timestamptz not null default now(),
  primary key (tenant_id, import_source, legacy_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);
create index target_loaded_totals on target_loaded (tenant_id, import_source, legal_entity_id, currency);

-- legacy_xref stays append-only except for one step: when the target assigns the open item its id,
-- passage_id (the factory's provisional id until then) takes that id and target_confirmed is set,
-- once. Nothing else in a row may change.
alter table legacy_xref add column target_confirmed boolean not null default false;

create function legacy_xref_confirm_target() returns trigger language plpgsql as $$
begin
  if old.target_confirmed or not new.target_confirmed
     or (new.tenant_id, new.xref_id, new.source_system, new.source_company, new.source_table, new.source_id,
         new.passage_entity, new.run_id, new.amount, new.currency, new.created_at)
        is distinct from
        (old.tenant_id, old.xref_id, old.source_system, old.source_company, old.source_table, old.source_id,
         old.passage_entity, old.run_id, old.amount, old.currency, old.created_at) then
    raise exception 'legacy_xref is append-only: only the one-time confirmation of the target id may update a row';
  end if;
  return new;
end
$$;
create trigger legacy_xref_confirm_target before update on legacy_xref
  for each row execute function legacy_xref_confirm_target();

alter table target_load enable row level security;
alter table target_load force row level security;
create policy target_load_tenant_access on target_load as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy target_load_legal_entity_scope on target_load as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table target_row enable row level security;
alter table target_row force row level security;
create policy target_row_tenant_access on target_row as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy target_row_legal_entity_scope on target_row as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table target_loaded enable row level security;
alter table target_loaded force row level security;
create policy target_loaded_tenant_access on target_loaded as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy target_loaded_legal_entity_scope on target_loaded as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges. The load step updates the load header, its rows and the remembered
-- target state; legacy_xref gets the column-level update the trigger above narrows further. Nothing
-- is deleted. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON target_load, target_row, target_loaded TO passage_app';
    EXECUTE 'GRANT UPDATE (passage_id, target_confirmed) ON legacy_xref TO passage_app';
  END IF;
END
$$;
