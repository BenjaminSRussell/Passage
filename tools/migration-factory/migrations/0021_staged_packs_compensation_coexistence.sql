-- Migration factory, wave 49 slice 17 (#484): the staged packs wip_prod, fixed_assets and
-- payroll_ytd (D1a), the compensating journals of a rolled-back cutover (D9), and the
-- entity-by-entity coexistence flag (D6). Every table carries tenant_id first and is held to the
-- tenant by forced row-level security.
SET LOCAL lock_timeout = '5s';

-- Staged opening lines of the packs whose owning service has no opening-balance import yet. One
-- line per natural key per (pack, system, company); measures are the amounts the control totals
-- sum, keyed by measure name; group holds the fields they group by. Write-once, like legacy_xref.
create table staged_opening_line (
  tenant_id uuid not null,
  line_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  pack text not null check (pack in ('wip_prod', 'fixed_assets', 'payroll_ytd')),
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legacy_id text not null check (legacy_id <> ''),
  natural_key text not null check (natural_key <> ''),
  currency char(3) not null,
  grouping jsonb not null default '{}',
  measures jsonb not null default '{}',
  created_at timestamptz not null default now(),
  primary key (tenant_id, line_id),
  unique (tenant_id, legacy_xref_id),
  unique (tenant_id, pack, source_system, source_company, natural_key),
  unique (tenant_id, pack, source_system, source_company, legacy_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);

-- The ledger each entity of a rolled-back plan is compensated in, pinned on first use: a later run
-- naming another ledger or legal entity is refused, so one journal is never compensated twice in
-- two ledgers.
create table rollback_ledger (
  tenant_id uuid not null,
  plan_id uuid not null,
  entity text not null check (entity <> ''),
  legal_entity_id uuid not null,
  ledger_id text not null check (ledger_id <> ''),
  pinned_by text not null,
  pinned_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, entity),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

-- One compensating journal per re-keying journal (entity, document, currency) of a rolled-back
-- plan. Its lines are derived from rekey_line when it is posted; the idempotency key is fixed at
-- creation, so a retry or a second run posts the same entry once. A failure backs off
-- (next_attempt_at) and parks the row after the attempt limit or a rejection.
create table rollback_compensation (
  tenant_id uuid not null,
  plan_id uuid not null,
  entity text not null,
  document_ref text not null,
  currency char(3) not null,
  idempotency_key text not null,
  status text not null default 'PENDING' check (status in ('PENDING', 'POSTED', 'PARKED')),
  attempts int not null default 0 check (attempts >= 0),
  last_code text not null default '',
  next_attempt_at timestamptz not null default now(),
  entry_id text not null default '',
  posted_at timestamptz,
  created_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, entity, document_ref, currency),
  unique (tenant_id, idempotency_key),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

-- Which side masters an entity kind of a legacy company during coexistence (FR-49.7): LEGACY until
-- the entity is cut over, then PASSAGE; a rollback flips it back. Versioned writes.
create table coexistence_entity (
  tenant_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  entity text not null check (entity <> ''),
  mode text not null check (mode in ('LEGACY', 'PASSAGE')),
  cutover_at timestamptz,
  version int not null check (version >= 1),
  changed_by text not null,
  changed_at timestamptz not null default now(),
  primary key (tenant_id, source_system, source_company, entity)
);

alter table staged_opening_line enable row level security;
alter table staged_opening_line force row level security;
create policy staged_opening_line_tenant_access on staged_opening_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
alter table rollback_ledger enable row level security;
alter table rollback_ledger force row level security;
create policy rollback_ledger_tenant_access on rollback_ledger as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- A caller sees and pins only the ledgers of legal entities in its scope.
create policy rollback_ledger_legal_entity_scope on rollback_ledger as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
alter table rollback_compensation enable row level security;
alter table rollback_compensation force row level security;
create policy rollback_compensation_tenant_access on rollback_compensation as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
alter table coexistence_entity enable row level security;
alter table coexistence_entity force row level security;
create policy coexistence_entity_tenant_access on coexistence_entity as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Staged lines and pinned ledgers are insert-only; compensations and
-- coexistence flags are updated in place. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON staged_opening_line, rollback_ledger TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON rollback_compensation, coexistence_entity TO passage_app';
  END IF;
END
$$;
