-- Migration factory, wave 49 (49-3): the AP lines of an open_items run are loaded into payables as opening batches
-- (POST /v1/payables/opening-batches, all or nothing, amounts in minor units). target_row keeps the AP rows next to
-- the AR rows (ledger = 'AP', payload = the payables item); payables_load is the AP side of the load (its own
-- status and lease, because receivables and payables answer separately), payables_loaded what payables accepted per
-- legacy reference (a rerun of an unchanged snapshot sends nothing) and payables_batch each batch that was sent.
-- Every table carries tenant_id first and is held to the tenant by forced row-level security and to the caller's
-- legal-entity scopes, like target_load.

create table payables_load (
  tenant_id uuid not null,
  run_id uuid not null,
  legal_entity_id uuid not null,
  import_source text not null check (import_source <> '' and length(import_source) <= 40),
  status text not null check (status in ('STAGED', 'LOADING', 'PARTIAL', 'UNRECONCILED', 'MISMATCH', 'RECONCILED', 'FAILED')),
  staged int not null check (staged >= 0),
  loaded int not null default 0 check (loaded >= 0),
  unchanged int not null default 0 check (unchanged >= 0),
  skipped int not null default 0 check (skipped >= 0),
  rejected int not null default 0 check (rejected >= 0),
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

create table payables_loaded (
  tenant_id uuid not null,
  import_source text not null,
  reference text not null,
  legal_entity_id uuid not null,
  currency char(3) not null,
  amount_minor bigint not null check (amount_minor > 0),
  row_hash text not null,
  batch_key text not null,
  batch_id uuid not null,
  run_id uuid not null,
  loaded_at timestamptz not null default now(),
  primary key (tenant_id, import_source, reference),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);
create index payables_loaded_totals on payables_loaded (tenant_id, import_source, legal_entity_id, currency);

create table payables_batch (
  tenant_id uuid not null,
  run_id uuid not null,
  batch_key text not null,
  import_source text not null,
  legal_entity_id uuid not null,
  batch_id uuid not null,
  item_count int not null check (item_count between 1 and 1000),
  totals_minor jsonb not null,
  replayed boolean not null default false,
  sent_at timestamptz not null default now(),
  primary key (tenant_id, run_id, batch_key),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

alter table payables_load enable row level security;
alter table payables_load force row level security;
create policy payables_load_tenant_access on payables_load as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy payables_load_legal_entity_scope on payables_load as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table payables_loaded enable row level security;
alter table payables_loaded force row level security;
create policy payables_loaded_tenant_access on payables_loaded as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy payables_loaded_legal_entity_scope on payables_loaded as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table payables_batch enable row level security;
alter table payables_batch force row level security;
create policy payables_batch_tenant_access on payables_batch as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy payables_batch_legal_entity_scope on payables_batch as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON payables_load, payables_loaded TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON payables_batch TO passage_app';
  END IF;
END
$$;
