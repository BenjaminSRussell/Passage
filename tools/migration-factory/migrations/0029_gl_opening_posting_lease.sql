-- A posting lease for the opening journal (#560, from #549): a call that is posting a legacy company's opening journals holds a
-- lease row from before it reads the company's pin until its last posting row is recorded, and the administrator's release of the
-- pin refuses while an unexpired lease exists. Without it a release could run between the ledger booking a journal and the
-- posting row being written, see no posting, delete the pin and let the company be booked to another legal entity.
-- Leases are shared (several calls for one company may post at once; the ledger books each key once) and expire, so a crashed
-- process cannot block a release for longer than the lease.
create table gl_opening_posting_lease (
  tenant_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  holder uuid not null,
  taken_by text not null,
  taken_at timestamptz not null default now(),
  expires_at timestamptz not null,
  primary key (tenant_id, source_system, source_company, holder)
);
alter table gl_opening_posting_lease enable row level security;
alter table gl_opening_posting_lease force row level security;
create policy gl_opening_posting_lease_tenant_access on gl_opening_posting_lease as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON gl_opening_posting_lease TO passage_app';
  END IF;
END
$$;
