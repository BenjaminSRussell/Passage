-- Wave 36 (PSG-36) FR-36.12: EBICS H004 subscribers. A subscriber is one user at one bank for one bank
-- account. Only the names of the Vault keys and the bank's public keys are stored; no private key is.
-- READY needs the bank key hashes to have been confirmed by a named person other than the one who downloaded
-- them, and both transit key versions pinned. The key names are not chosen by callers: the table only accepts
-- the names derived from the row's own tenant, legal entity and id, so no subscriber can name another
-- tenant's keys. One bank account has one subscriber. No row is deleted.
create table ebics_subscriber (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  host_id text not null check (host_id ~ '^[A-Za-z0-9._-]{1,35}$'),
  partner_id text not null check (partner_id ~ '^[A-Za-z0-9._-]{1,35}$'),
  user_id text not null check (user_id ~ '^[A-Za-z0-9._-]{1,35}$'),
  url text not null check (url ~ '^https://' or url ~ '^http://(localhost|127\.0\.0\.1|\[::1\])(:|/|$)'),
  a006_key_name text not null,
  x002_key_name text not null,
  e002_kv_path text not null,
  a006_key_version int not null default 0 check (a006_key_version >= 0),
  x002_key_version int not null default 0 check (x002_key_version >= 0),
  state text not null default 'NEW' check (state in ('NEW', 'INI_SENT', 'HIA_SENT', 'BANK_KEYS_PENDING', 'READY')),
  bank_x002 bytea,
  bank_e002 bytea,
  fetched_by text,
  keys_confirmed boolean not null default false,
  confirmed_by text,
  confirmed_at timestamptz,
  parked_code text,
  parked_text text,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id),
  unique (tenant_id, host_id, partner_id, user_id),
  unique (tenant_id, bank_account_id),
  check (a006_key_name = 'ebics-' || tenant_id::text || '-' || legal_entity_id::text || '-' || id::text || '-a006'),
  check (x002_key_name = 'ebics-' || tenant_id::text || '-' || legal_entity_id::text || '-' || id::text || '-x002'),
  check (e002_kv_path = 'ebics/' || tenant_id::text || '/' || legal_entity_id::text || '/' || id::text || '/e002'),
  check (state <> 'READY' or (keys_confirmed and bank_x002 is not null and bank_e002 is not null and confirmed_by is not null and confirmed_at is not null)),
  check (state <> 'READY' or (fetched_by is not null and fetched_by <> confirmed_by and a006_key_version > 0 and x002_key_version > 0)),
  check (state not in ('BANK_KEYS_PENDING', 'READY') or (bank_x002 is not null and bank_e002 is not null))
);
alter table ebics_subscriber enable row level security;
alter table ebics_subscriber force row level security;
create policy ebics_subscriber_tenant_access on ebics_subscriber as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy ebics_subscriber_legal_entity_scope on ebics_subscriber as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ebics_subscriber TO passage_app';
  END IF;
END
$$;
