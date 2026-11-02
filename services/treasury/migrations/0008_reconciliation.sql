-- Wave 36 (PSG-36) FR-36.3: stored reconciliation rules and the match table.
-- A match groups statement lines with the open items they settle (ledger, AR or AP): one line to
-- many items (N:1 batch receipts) or one item to many lines. A line and an item are in at most one
-- active match; an unmatch keeps the row (active = false) as history.

create table reconciliation_rule (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid,
  name text not null check (length(name) between 1 and 80),
  priority int not null check (priority between 1 and 1000),
  weight_amount int not null check (weight_amount between 20 and 100),
  weight_date int not null check (weight_date between 0 and 80),
  weight_reference int not null check (weight_reference between 0 and 80),
  weight_counterparty int not null check (weight_counterparty between 0 and 80),
  amount_tolerance numeric(20,6) not null default 0 check (amount_tolerance >= 0),
  date_window_days int not null default 0 check (date_window_days between 0 and 30),
  strict boolean not null default false,
  active boolean not null default true,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, legal_entity_id, name),
  check (weight_amount + weight_date + weight_reference + weight_counterparty = 100),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id)
);
create index ix_reconciliation_rule_scope on reconciliation_rule (tenant_id, legal_entity_id, bank_account_id) where active;

create table reconciliation_match (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  statement_id uuid not null,
  kind text not null check (kind in ('auto', 'manual')),
  status text not null default 'matched' check (status in ('matched', 'unmatched')),
  rule_name text not null default '',
  score int check (score is null or score between 0 and 100),
  difference numeric(20,6) not null default 0,
  reason text not null default '',
  unmatch_reason text,
  unmatched_by text,
  created_by text not null,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, statement_id) references bank_statement (tenant_id, id),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id)
);
create index ix_reconciliation_match_statement on reconciliation_match (tenant_id, statement_id, created_at);

create table reconciliation_match_line (
  tenant_id uuid not null,
  match_id uuid not null,
  statement_id uuid not null,
  line_no int not null,
  legal_entity_id uuid not null,
  amount numeric(20,6) not null,
  active boolean not null default true,
  primary key (tenant_id, match_id, statement_id, line_no),
  foreign key (tenant_id, match_id) references reconciliation_match (tenant_id, id),
  foreign key (tenant_id, statement_id, line_no) references bank_statement_line (tenant_id, statement_id, line_no)
);
create unique index ux_reconciliation_match_line_active on reconciliation_match_line (tenant_id, statement_id, line_no) where active;

create table reconciliation_match_item (
  tenant_id uuid not null,
  match_id uuid not null,
  legal_entity_id uuid not null,
  item_type text not null check (item_type in ('ledger', 'ar', 'ap')),
  item_id text not null check (length(item_id) between 1 and 100),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  amount numeric(20,6) not null,
  active boolean not null default true,
  primary key (tenant_id, match_id, item_type, item_id),
  foreign key (tenant_id, match_id) references reconciliation_match (tenant_id, id)
);
create unique index ux_reconciliation_match_item_active on reconciliation_match_item (tenant_id, legal_entity_id, item_type, item_id) where active;

alter table reconciliation_rule enable row level security;
alter table reconciliation_rule force row level security;
create policy reconciliation_rule_tenant_access on reconciliation_rule as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy reconciliation_rule_legal_entity_scope on reconciliation_rule as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table reconciliation_match enable row level security;
alter table reconciliation_match force row level security;
create policy reconciliation_match_tenant_access on reconciliation_match as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy reconciliation_match_legal_entity_scope on reconciliation_match as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table reconciliation_match_line enable row level security;
alter table reconciliation_match_line force row level security;
create policy reconciliation_match_line_tenant_access on reconciliation_match_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy reconciliation_match_line_legal_entity_scope on reconciliation_match_line as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table reconciliation_match_item enable row level security;
alter table reconciliation_match_item force row level security;
create policy reconciliation_match_item_tenant_access on reconciliation_match_item as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy reconciliation_match_item_legal_entity_scope on reconciliation_match_item as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The statement line keeps its append-only grant except for the two columns a match moves.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON reconciliation_rule, reconciliation_match, reconciliation_match_line, reconciliation_match_item TO passage_app';
    EXECUTE 'GRANT UPDATE (status, version) ON bank_statement_line TO passage_app';
  END IF;
END
$$;
