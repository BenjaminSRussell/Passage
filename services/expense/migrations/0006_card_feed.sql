-- Slice 5 (PSG-43): card feed import persistence (FR-43.13, EXP-CARD-002, 8.43.4).
-- One import per file hash per tenant ("key per file hash", 8.43.5). Rows the parser
-- rejected and rows whose card has no cardholder are kept on the import as its
-- quarantine. Each accepted row is one card transaction in its worker's UNASSIGNED pool.

create table card_import (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  format text not null check (format in ('CSV', 'OFX')),
  file_hash text not null check (file_hash ~ '^[0-9a-f]{64}$'),
  imported int not null default 0 check (imported >= 0),
  quarantined jsonb not null default '[]'::jsonb,
  imported_by text not null default '',
  create_key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  constraint card_import_file_hash unique (tenant_id, file_hash),
  constraint card_import_create_key unique (tenant_id, create_key)
);

create table card_transaction (
  tenant_id uuid not null,
  id uuid not null,
  import_id uuid not null,
  legal_entity_id uuid not null,
  worker_id uuid not null,
  source_line int not null check (source_line > 0),
  card_last4 text not null check (card_last4 ~ '^[0-9]{4}$'),
  txn_date date not null,
  post_date date not null,
  merchant text not null default '' check (length(merchant) <= 200),
  mcc text not null default '' check (length(mcc) <= 4),
  amount numeric(20,6) not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  status text not null default 'UNASSIGNED' check (status in ('UNASSIGNED', 'ASSIGNED', 'DISPUTED', 'PERSONAL')),
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  constraint card_transaction_import_fk foreign key (tenant_id, import_id) references card_import (tenant_id, id),
  constraint card_transaction_source unique (tenant_id, import_id, source_line)
);

create index card_transaction_worker_status on card_transaction (tenant_id, worker_id, status);

alter table card_import enable row level security;
alter table card_import force row level security;
create policy card_import_tenant_access on card_import as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy card_import_legal_entity_scope on card_import as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table card_transaction enable row level security;
alter table card_transaction force row level security;
create policy card_transaction_tenant_access on card_transaction as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy card_transaction_legal_entity_scope on card_transaction as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
