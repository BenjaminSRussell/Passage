-- Wave 36 (PSG-36) slice 1: bank accounts, statements, statement lines, cash forecast entries.
-- Every tenant table: tenant_id first, PK (tenant_id, id), permissive tenant policy (dbx.RLSPolicy)
-- and restrictive legal-entity policy (dbx.LegalEntityScopePolicy). Money numeric(20,6) + currency char(3).

create table bank_account (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  account_code text not null check (account_code ~ '^[A-Z0-9-]{1,20}$'),
  iban_hash bytea not null,
  iban_masked text not null,
  iban_last4 char(4) not null,
  bic text check (bic is null or bic ~ '^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$'),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  main_account_id uuid not null,
  status text not null default 'draft' check (status in ('draft', 'active', 'inactive', 'closed')),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, legal_entity_id, account_code),
  unique (tenant_id, iban_hash)
);
alter table bank_account enable row level security;
alter table bank_account force row level security;
create policy bank_account_tenant_access on bank_account as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_account_legal_entity_scope on bank_account as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table bank_statement (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  statement_id text not null,
  sequence int,
  format text not null check (format like 'camt.053.%'),
  file_hash text not null,
  currency char(3) not null,
  opening_date date not null,
  opening_amount numeric(20,6) not null,
  closing_date date not null,
  closing_amount numeric(20,6) not null,
  line_count int not null default 0 check (line_count >= 0),
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, file_hash),
  unique (tenant_id, bank_account_id, statement_id),
  foreign key (tenant_id, bank_account_id) references bank_account (tenant_id, id)
);
create index ix_treasury_statement_sequence on bank_statement (tenant_id, bank_account_id, sequence) where sequence is not null;
alter table bank_statement enable row level security;
alter table bank_statement force row level security;
create policy bank_statement_tenant_access on bank_statement as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_statement_legal_entity_scope on bank_statement as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table bank_statement_line (
  tenant_id uuid not null,
  statement_id uuid not null,
  line_no int not null check (line_no > 0),
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  booking_date date not null,
  value_date date not null,
  amount numeric(20,6) not null,
  currency char(3) not null,
  btc_code text not null default '',
  end_to_end_ref text not null default '',
  reference text not null default '',
  counterparty_name text not null default '',
  status text not null default 'unmatched' check (status in ('unmatched', 'suggested', 'matched', 'created', 'in_transit')),
  raw_fragment text not null,
  version int not null default 1,
  primary key (tenant_id, statement_id, line_no),
  foreign key (tenant_id, statement_id) references bank_statement (tenant_id, id)
);
create index ix_treasury_statement_line_value_date on bank_statement_line (tenant_id, bank_account_id, value_date);
create index ix_treasury_statement_line_unmatched on bank_statement_line (tenant_id, bank_account_id) where status = 'unmatched';
alter table bank_statement_line enable row level security;
alter table bank_statement_line force row level security;
create policy bank_statement_line_tenant_access on bank_statement_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_statement_line_legal_entity_scope on bank_statement_line as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Monthly partitioning by expected_date (FR-36.9 retention 24 months) is deferred; see NOT-IMPLEMENTED.md.
create table cash_forecast_entry (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  source_type text not null check (source_type in ('AP', 'AR', 'PO', 'SO', 'Recurring', 'Manual', 'Payroll', 'Project')),
  source_id text not null,
  expected_date date not null,
  amount numeric(20,6) not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  confidence numeric(9,6) not null default 1 check (confidence >= 0 and confidence <= 1),
  computed_at timestamptz not null default now(),
  primary key (tenant_id, id)
);
create index ix_treasury_forecast_entry_date on cash_forecast_entry (tenant_id, legal_entity_id, expected_date);
alter table cash_forecast_entry enable row level security;
alter table cash_forecast_entry force row level security;
create policy cash_forecast_entry_tenant_access on cash_forecast_entry as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy cash_forecast_entry_legal_entity_scope on cash_forecast_entry as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Idempotency-Key store (dbx.IdempotencyDDL) for create bank account and create forecast entry.
create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);
alter table idempotency_keys enable row level security;
alter table idempotency_keys force row level security;
create policy idempotency_keys_tenant_access on idempotency_keys as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Transactional outbox (dbx.OutboxDDL); excluded from RLS, relayed by Debezium.
create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);
alter table outbox replica identity full;
