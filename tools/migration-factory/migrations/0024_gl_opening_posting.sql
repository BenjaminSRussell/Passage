-- Migration factory, wave 49 (FR-49.1, FR-49.2): the opening journal of a legacy company's
-- gl_opening_balance rows, posted through the general ledger's ordinary posting API. One row per
-- (legacy company, currency) records the journal the ledger booked: its idempotency
-- key, the ledger and the hash of the lines it carried. A second post of the same balances finds the
-- row and sends nothing; the same key at the ledger makes a lost answer safe to retry.
create table gl_opening_posting (
  tenant_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legal_entity_id uuid not null,
  currency char(3) not null,
  ledger_id text not null check (ledger_id <> ''),
  posting_date date not null,
  idempotency_key text not null,
  content_hash text not null,
  line_count int not null check (line_count >= 2),
  debit numeric(28,6) not null,
  credit numeric(28,6) not null,
  entry_id text not null check (entry_id <> ''),
  run_id uuid not null,
  posted_by text not null,
  posted_at timestamptz not null default now(),
  primary key (tenant_id, source_system, source_company, currency),
  unique (tenant_id, idempotency_key),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

-- The one legal entity and ledger a legacy company's opening journal is booked to, fixed by the first
-- posting. Tenant-only row security: a caller scoped to another legal entity must still be refused,
-- and learns only that the company is already assigned. The column is not named legal_entity_id because
-- it is a pinned target, not the scope of the row.
create table gl_opening_target (
  tenant_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  target_legal_entity_id uuid not null,
  ledger_id text not null check (ledger_id <> ''),
  pinned_by text not null,
  pinned_at timestamptz not null default now(),
  primary key (tenant_id, source_system, source_company)
);
alter table gl_opening_target enable row level security;
alter table gl_opening_target force row level security;
create policy gl_opening_target_tenant_access on gl_opening_target as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table gl_opening_posting enable row level security;
alter table gl_opening_posting force row level security;
create policy gl_opening_posting_tenant_access on gl_opening_posting as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy gl_opening_posting_legal_entity_scope on gl_opening_posting as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON gl_opening_posting, gl_opening_target TO passage_app';
  END IF;
END
$$;
