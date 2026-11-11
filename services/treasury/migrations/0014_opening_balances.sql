-- Opening bank balances (#49, H-S10-1): the book balance of each bank account at the cutover date, loaded by the
-- migration factory in batches. A batch is all or nothing and is stored under its key, so a resend of the same content is
-- a replay; an account has one opening balance. Balances are whole minor units of the account's currency.

create table bank_opening_batch (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  batch_key text not null check (char_length(batch_key) between 1 and 200),
  content_hash text not null,
  as_of date not null,
  item_count int not null check (item_count between 1 and 1000),
  totals_minor jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, batch_key)
);
create index ix_bank_opening_batch_entity on bank_opening_batch (tenant_id, legal_entity_id, batch_key);

create table bank_opening_balance (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  batch_key text not null,
  reference text not null check (char_length(reference) between 1 and 255),
  bank_account_id uuid not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  balance_minor bigint not null,
  as_of date not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, bank_account_id),
  unique (tenant_id, legal_entity_id, reference),
  foreign key (tenant_id, batch_key) references bank_opening_batch (tenant_id, batch_key),
  foreign key (tenant_id, bank_account_id) references bank_account (tenant_id, id)
);

alter table bank_opening_batch enable row level security;
alter table bank_opening_batch force row level security;
create policy bank_opening_batch_tenant_access on bank_opening_batch as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_opening_batch_legal_entity_scope on bank_opening_batch as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table bank_opening_balance enable row level security;
alter table bank_opening_balance force row level security;
create policy bank_opening_balance_tenant_access on bank_opening_balance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_opening_balance_legal_entity_scope on bank_opening_balance as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON bank_opening_batch, bank_opening_balance TO passage_app';
  END IF;
END
$$;
