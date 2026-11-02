create table tax_settlement_run (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  period_from date not null,
  period_to date not null,
  version integer not null check (version > 0),
  output_tax_total numeric(20,6) not null,
  input_tax_total numeric(20,6) not null,
  net_amount numeric(20,6) not null,
  lines jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, legal_entity_id, period_from, period_to, version),
  check (period_from <= period_to)
);
alter table tax_settlement_run enable row level security;
alter table tax_settlement_run force row level security;
create policy tax_settlement_run_tenant_access on tax_settlement_run as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy tax_settlement_run_legal_entity_scope on tax_settlement_run as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table tax_transaction (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  code text not null,
  direction text not null check (direction in ('output', 'input', 'use')),
  tax_date date not null,
  amount numeric(20,6) not null,
  settlement_run_id uuid,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, settlement_run_id) references tax_settlement_run (tenant_id, id)
);
create index tax_transaction_period_idx on tax_transaction (tenant_id, legal_entity_id, tax_date);
alter table tax_transaction enable row level security;
alter table tax_transaction force row level security;
create policy tax_transaction_tenant_access on tax_transaction as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy tax_transaction_legal_entity_scope on tax_transaction as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table tax_recommit_queue (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  provider text not null,
  reason text not null,
  request jsonb not null,
  status text not null default 'pending' check (status in ('pending', 'done')),
  attempts integer not null default 0,
  last_error text,
  result jsonb,
  created_at timestamptz not null default now(),
  completed_at timestamptz,
  primary key (tenant_id, id)
);
create index tax_recommit_queue_pending_idx on tax_recommit_queue (tenant_id, created_at) where status = 'pending';
alter table tax_recommit_queue enable row level security;
alter table tax_recommit_queue force row level security;
create policy tax_recommit_queue_tenant_access on tax_recommit_queue as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy tax_recommit_queue_legal_entity_scope on tax_recommit_queue as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);
alter table outbox enable row level security;
alter table outbox force row level security;
create policy outbox_tenant_access on outbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
