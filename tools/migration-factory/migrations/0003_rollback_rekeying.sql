-- Migration factory, wave 49 slice 3 (FR-49.9): the re-keying export of a rollback. When a
-- cutover rolls back, the Passage postings made since T-0 are kept as journal and document lines
-- for re-keying in the legacy system, retained with the plan "until re-keyed and reconciled"
-- (9.49.15). Lines are write-once; the plan carries the counts.

alter table cutover_plan add column rekey_journal_lines int not null default 0 check (rekey_journal_lines >= 0);
alter table cutover_plan add column rekey_document_lines int not null default 0 check (rekey_document_lines >= 0);
alter table cutover_plan add column rekey_excluded int not null default 0 check (rekey_excluded >= 0);

create table rekey_line (
  tenant_id uuid not null,
  plan_id uuid not null,
  kind text not null check (kind in ('JOURNAL', 'DOCUMENT')),
  seq int not null check (seq >= 1),
  entity text not null check (entity <> ''),
  document_ref text not null check (document_ref <> ''),
  line int not null check (line >= 1),
  posted_at timestamptz not null,
  account text not null check (account <> ''),
  dimensions text not null default '',
  currency char(3) not null,
  amount numeric(28,6) not null,
  text text not null default '',
  created_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, kind, seq),
  unique (tenant_id, plan_id, kind, entity, document_ref, line),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

alter table rekey_line enable row level security;
alter table rekey_line force row level security;
create policy rekey_line_tenant_access on rekey_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Re-keying lines are insert-only; the counts on cutover_plan are
-- written by the rollback's existing UPDATE grant. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON rekey_line TO passage_app';
  END IF;
END
$$;
