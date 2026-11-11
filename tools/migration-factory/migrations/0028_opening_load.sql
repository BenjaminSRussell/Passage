-- Migration factory (#49, H-S9-1 and H-S10-1): the staged opening stock (onhand) and opening bank balances
-- (bank_balances) of a run are loaded into the inventory ledger (POST /v1/inventory/opening-batches) and into treasury
-- (POST /v1/treasury/opening-balances). opening_load is the run's load (its own status and lease, one target per run),
-- opening_load_row what the target took per staged balance (the staged tables stay insert-only), opening_load_batch each
-- batch that was sent. Every table carries tenant_id first and is held to the tenant by forced row-level security and to
-- the caller's legal-entity scopes, like payables_load.

create table opening_load (
  tenant_id uuid not null,
  run_id uuid not null,
  target text not null check (target in ('inventory', 'treasury')),
  legal_entity_id uuid not null,
  status text not null check (status in ('STAGED', 'LOADING', 'PARTIAL', 'UNRECONCILED', 'MISMATCH', 'RECONCILED', 'FAILED')),
  staged int not null check (staged >= 0),
  loaded int not null default 0 check (loaded >= 0),
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

create table opening_load_row (
  tenant_id uuid not null,
  run_id uuid not null,
  balance_id uuid not null,
  legal_entity_id uuid not null,
  status text not null check (status in ('LOADED', 'REJECTED')),
  batch_key text not null default '',
  reject_code text not null default '',
  reject_detail text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, balance_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);
create index opening_load_row_run on opening_load_row (tenant_id, run_id, status);

create table opening_load_batch (
  tenant_id uuid not null,
  run_id uuid not null,
  batch_key text not null,
  legal_entity_id uuid not null,
  item_count int not null check (item_count between 1 and 1000),
  replayed boolean not null default false,
  -- PLANNED is written before the batch is sent, with the items exactly as sent: an ambiguous outcome (a timeout) is resolved
  -- by sending the same key and items again, never by forming a new batch from rows the target may already hold.
  status text not null default 'ACCEPTED' check (status in ('PLANNED', 'ACCEPTED', 'DROPPED')),
  currency text not null default '',
  posting_date text not null default '',
  payload jsonb,
  sent_at timestamptz not null default now(),
  primary key (tenant_id, run_id, batch_key),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

create table opening_load_batch_row (
  tenant_id uuid not null,
  run_id uuid not null,
  batch_key text not null,
  seq int not null check (seq >= 1),
  balance_id uuid not null,
  primary key (tenant_id, run_id, batch_key, seq),
  foreign key (tenant_id, run_id, batch_key) references opening_load_batch (tenant_id, run_id, batch_key)
);
create index opening_load_batch_row_balance on opening_load_batch_row (tenant_id, balance_id);

alter table opening_load enable row level security;
alter table opening_load force row level security;
create policy opening_load_tenant_access on opening_load as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy opening_load_legal_entity_scope on opening_load as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table opening_load_row enable row level security;
alter table opening_load_row force row level security;
create policy opening_load_row_tenant_access on opening_load_row as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy opening_load_row_legal_entity_scope on opening_load_row as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table opening_load_batch_row enable row level security;
alter table opening_load_batch_row force row level security;
create policy opening_load_batch_row_tenant_access on opening_load_batch_row as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table opening_load_batch enable row level security;
alter table opening_load_batch force row level security;
create policy opening_load_batch_tenant_access on opening_load_batch as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy opening_load_batch_legal_entity_scope on opening_load_batch as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON opening_load, opening_load_row TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON opening_load_batch TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON opening_load_batch_row TO passage_app';
  END IF;
END
$$;
