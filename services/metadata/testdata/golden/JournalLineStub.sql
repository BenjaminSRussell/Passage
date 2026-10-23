create table "journal_line_stubs" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "legal_entity_id" uuid not null,
  "journal_entry_id" uuid not null,
  "account" varchar(16) not null,
  "amount" numeric(20,6) not null,
  "posting_date" date not null,
  "memo" varchar(255),
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id)
);
create index "ix_journal_line_stubs_01" on "journal_line_stubs" ("tenant_id", "journal_entry_id");
alter table journal_line_stubs enable row level security;
alter table journal_line_stubs force row level security;
create policy journal_line_stubs_tenant_access on journal_line_stubs as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy journal_line_stubs_legal_entity_scope on journal_line_stubs as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
