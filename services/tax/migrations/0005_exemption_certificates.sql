-- Stored exemption certificates (#13). A certificate exempts the codes it names for the dates it
-- is valid; a document names it by number in party.certificate and a stored_config calculation
-- reads it from here instead of taking it inline.
create table tax_exemption_certificate (
  tenant_id uuid not null,
  number text not null check (number ~ '^[A-Za-z0-9._/-]{1,64}$'),
  party_ref text not null default '' check (length(party_ref) <= 64),
  note text not null default '' check (length(note) <= 200),
  codes text[] not null check (cardinality(codes) > 0),
  valid_from date not null,
  valid_to date,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, number),
  check (valid_to is null or valid_to >= valid_from)
);
alter table tax_exemption_certificate enable row level security;
alter table tax_exemption_certificate force row level security;
create policy tax_exemption_certificate_tenant_access on tax_exemption_certificate as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tax_exemption_certificate TO passage_app';
  END IF;
END
$$;
