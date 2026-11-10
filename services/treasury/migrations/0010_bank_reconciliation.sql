-- Wave 36 (PSG-36) FR-36.5: the bank reconciliation of one statement (one account and period) with its close and reopen,
-- lines excluded with a reason, and bank transactions created from a statement line and posted to the ledger.
-- Closing locks the statement's lines and matches against change: the triggers below refuse any write to them while the
-- header is closed. No row is deleted.

alter table bank_statement_line drop constraint if exists bank_statement_line_status_check;
alter table bank_statement_line add constraint bank_statement_line_status_check
  check (status in ('unmatched', 'suggested', 'matched', 'created', 'in_transit', 'excluded'));

create table bank_reconciliation (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  statement_id uuid not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  period_from date not null,
  period_to date not null,
  opening_balance numeric(20,6) not null,
  closing_balance numeric(20,6) not null,
  book_balance numeric(20,6) not null,
  outstanding_amount numeric(20,6) not null default 0,
  state text not null default 'open' check (state in ('open', 'closed', 'reopened')),
  closed_by text,
  closed_at timestamptz,
  reopened_by text,
  reopened_at timestamptz,
  reopen_reason text,
  version int not null default 1,
  created_by text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, statement_id),
  foreign key (tenant_id, statement_id) references bank_statement (tenant_id, id),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id),
  check (period_from <= period_to),
  check (state <> 'closed' or (closed_by is not null and closed_at is not null)),
  check (reopened_at is null or (reopened_by is not null and reopen_reason is not null))
);
create index ix_bank_reconciliation_account_period on bank_reconciliation (tenant_id, bank_account_id, period_to desc, id desc);
create index ix_bank_reconciliation_entity_period on bank_reconciliation (tenant_id, legal_entity_id, period_to desc, id desc);

create table bank_reconciliation_event (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  reconciliation_id uuid not null,
  event text not null check (event in ('created', 'closed', 'reopened')),
  actor text not null check (actor <> ''),
  reason text,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, reconciliation_id) references bank_reconciliation (tenant_id, id)
);
create index ix_bank_reconciliation_event_reconciliation on bank_reconciliation_event (tenant_id, reconciliation_id, created_at);

create table reconciliation_line_exclusion (
  tenant_id uuid not null,
  statement_id uuid not null,
  line_no int not null,
  legal_entity_id uuid not null,
  amount numeric(20,6) not null,
  reason text not null check (length(reason) between 1 and 200),
  excluded_by text not null,
  active boolean not null default true,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, statement_id, line_no),
  foreign key (tenant_id, statement_id, line_no) references bank_statement_line (tenant_id, statement_id, line_no)
);

create table bank_line_transaction (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  reconciliation_id uuid not null,
  statement_id uuid not null,
  line_no int not null,
  kind text not null check (kind in ('bank_fee', 'interest', 'unknown_receipt')),
  amount numeric(20,6) not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  posting_date date not null,
  status text not null default 'pending' check (status in ('pending', 'posted', 'refused')),
  refusal_code text,
  ledger_entry_id text,
  match_id uuid,
  created_by text not null,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, statement_id, line_no),
  foreign key (tenant_id, reconciliation_id) references bank_reconciliation (tenant_id, id),
  foreign key (tenant_id, statement_id, line_no) references bank_statement_line (tenant_id, statement_id, line_no),
  check (status <> 'posted' or (ledger_entry_id is not null and match_id is not null))
);

create function reconciliation_closed_guard() returns trigger language plpgsql as $$
begin
  if exists (select 1 from bank_reconciliation where tenant_id = new.tenant_id and statement_id = new.statement_id and state = 'closed') then
    raise exception 'the reconciliation of this statement is closed' using errcode = 'TRC01';
  end if;
  return new;
end
$$;

create function reconciliation_pending_guard() returns trigger language plpgsql as $$
begin
  if exists (select 1 from bank_line_transaction where tenant_id = new.tenant_id and statement_id = new.statement_id and line_no = new.line_no and status = 'pending') then
    raise exception 'a transaction is being created from this statement line' using errcode = 'TRC02';
  end if;
  return new;
end
$$;

create trigger bank_statement_line_closed_guard before update on bank_statement_line
  for each row execute function reconciliation_closed_guard();
create trigger reconciliation_match_closed_guard before insert or update on reconciliation_match
  for each row execute function reconciliation_closed_guard();
create trigger reconciliation_match_line_closed_guard before insert or update on reconciliation_match_line
  for each row execute function reconciliation_closed_guard();
create trigger reconciliation_line_exclusion_closed_guard before insert or update on reconciliation_line_exclusion
  for each row execute function reconciliation_closed_guard();
create trigger bank_line_transaction_closed_guard before insert or update on bank_line_transaction
  for each row execute function reconciliation_closed_guard();
create trigger reconciliation_match_line_pending_guard before insert on reconciliation_match_line
  for each row execute function reconciliation_pending_guard();

alter table bank_reconciliation enable row level security;
alter table bank_reconciliation force row level security;
create policy bank_reconciliation_tenant_access on bank_reconciliation as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_reconciliation_legal_entity_scope on bank_reconciliation as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table bank_reconciliation_event enable row level security;
alter table bank_reconciliation_event force row level security;
create policy bank_reconciliation_event_tenant_access on bank_reconciliation_event as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_reconciliation_event_legal_entity_scope on bank_reconciliation_event as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table reconciliation_line_exclusion enable row level security;
alter table reconciliation_line_exclusion force row level security;
create policy reconciliation_line_exclusion_tenant_access on reconciliation_line_exclusion as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy reconciliation_line_exclusion_legal_entity_scope on reconciliation_line_exclusion as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table bank_line_transaction enable row level security;
alter table bank_line_transaction force row level security;
create policy bank_line_transaction_tenant_access on bank_line_transaction as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_line_transaction_legal_entity_scope on bank_line_transaction as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON bank_reconciliation, reconciliation_line_exclusion, bank_line_transaction TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON bank_reconciliation_event TO passage_app';
  END IF;
END
$$;
