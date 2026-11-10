-- Wave 36 (PSG-36) FR-36.16: cash desks and the cash journal. A desk holds physical cash in one currency; each
-- business day is opened with the counted cash carried over, takes receipts and payments, and is closed against a count.

create table cash_desk (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  desk_code text not null check (desk_code ~ '^[A-Z0-9-]{1,20}$'),
  name text not null check (length(name) between 1 and 120),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  main_account_id uuid not null,
  max_balance numeric(20,6) not null check (max_balance > 0),
  status text not null default 'active' check (status in ('active', 'inactive')),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, legal_entity_id, desk_code)
);

create table cash_desk_day (
  tenant_id uuid not null,
  desk_id uuid not null,
  business_date date not null,
  legal_entity_id uuid not null,
  opening_balance numeric(20,6) not null,
  status text not null default 'open' check (status in ('open', 'closed')),
  counted_balance numeric(20,6),
  book_balance numeric(20,6),
  difference numeric(20,6),
  difference_reason text,
  opened_by text not null,
  closed_by text,
  opened_at timestamptz not null default now(),
  closed_at timestamptz,
  primary key (tenant_id, desk_id, business_date),
  foreign key (tenant_id, desk_id) references cash_desk (tenant_id, id),
  check ((status = 'open') = (closed_at is null))
);
-- Only one day of a desk is open at a time.
create unique index ux_cash_desk_day_open on cash_desk_day (tenant_id, desk_id) where status = 'open';

create table cash_desk_entry (
  tenant_id uuid not null,
  id uuid not null,
  desk_id uuid not null,
  business_date date not null,
  legal_entity_id uuid not null,
  line_no int not null check (line_no >= 1),
  kind text not null check (kind in ('receipt', 'payment')),
  amount numeric(20,6) not null check (amount > 0),
  description text not null check (length(description) between 1 and 200),
  reference text not null default '',
  created_by text not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, desk_id, business_date, line_no),
  foreign key (tenant_id, desk_id, business_date) references cash_desk_day (tenant_id, desk_id, business_date)
);

alter table cash_desk enable row level security;
alter table cash_desk force row level security;
create policy cash_desk_tenant_access on cash_desk as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy cash_desk_legal_entity_scope on cash_desk as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table cash_desk_day enable row level security;
alter table cash_desk_day force row level security;
create policy cash_desk_day_tenant_access on cash_desk_day as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy cash_desk_day_legal_entity_scope on cash_desk_day as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table cash_desk_entry enable row level security;
alter table cash_desk_entry force row level security;
create policy cash_desk_entry_tenant_access on cash_desk_entry as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy cash_desk_entry_legal_entity_scope on cash_desk_entry as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The journal is append-only; a day moves from open to closed once.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON cash_desk, cash_desk_day TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON cash_desk_entry TO passage_app';
  END IF;
END
$$;
