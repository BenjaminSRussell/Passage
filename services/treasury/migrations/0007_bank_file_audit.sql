-- Wave 36 (PSG-36) AC4: audit of every bank file sent. Append-only: a 'prepared' row is
-- written before a file is signed and sent, then a 'sent', 'failed' or 'unknown' (the bank may have received it) row for the same file_id.
-- The foreign key includes legal_entity_id, so an audit row cannot name another entity's account.
alter table bank_account add constraint bank_account_tenant_id_legal_entity_key unique (tenant_id, id, legal_entity_id);

create table bank_file_audit (
  tenant_id uuid not null,
  id uuid not null,
  file_id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  event text not null check (event in ('prepared', 'sent', 'failed', 'unknown')),
  protocol text not null check (protocol in ('ebics', 'sftp')),
  file_name text not null,
  order_type text not null,
  sha256 char(64) not null check (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes int not null check (size_bytes >= 0),
  key_name text not null,
  key_version int,
  bank_reference text,
  error text,
  sent_by text not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id)
);
create index ix_bank_file_audit_file on bank_file_audit (tenant_id, file_id, created_at);
alter table bank_file_audit enable row level security;
alter table bank_file_audit force row level security;
create policy bank_file_audit_tenant_access on bank_file_audit as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_file_audit_legal_entity_scope on bank_file_audit as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON bank_file_audit TO passage_app';
  END IF;
END
$$;
