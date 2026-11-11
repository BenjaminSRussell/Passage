-- VAT recovery (#13 deliverable 7): policies for how much of an input tax is recoverable per tax code and, optionally, per item tax
-- group, effective-dated; and the recoverable part recorded with an input tax transaction, which the VAT reclaim totals.
create table tax_recovery_policy (
  tenant_id uuid not null,
  code text not null,
  item_group text not null default '' check (item_group ~ '^[A-Za-z0-9._-]{0,64}$'),
  recoverable_pct numeric(7,4) not null check (recoverable_pct between 0 and 100),
  valid_from date not null,
  valid_to date,
  note text not null default '' check (length(note) <= 200),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, code, item_group, valid_from),
  foreign key (tenant_id, code) references tax_code (tenant_id, code),
  check (valid_to is null or valid_to >= valid_from)
);
alter table tax_recovery_policy enable row level security;
alter table tax_recovery_policy force row level security;
create policy tax_recovery_policy_tenant_access on tax_recovery_policy as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table tax_transaction add column recoverable_amount numeric(20,6);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tax_recovery_policy TO passage_app';
  END IF;
END
$$;
