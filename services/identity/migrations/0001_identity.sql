create extension if not exists btree_gist;

create table identity_tenants (
  tenant_id uuid primary key,
  realm_name text not null unique,
  display_name text not null,
  status text not null default 'active' check (status in ('active', 'suspended', 'deprovisioned')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table identity_users (
  tenant_id uuid not null references identity_tenants (tenant_id),
  user_id uuid not null,
  idp_subject text not null,
  email text,
  display_name text not null default '',
  status text not null default 'active' check (status in ('active', 'deprovisioned')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, user_id),
  unique (tenant_id, idp_subject)
);

create table identity_legal_entities (
  tenant_id uuid not null references identity_tenants (tenant_id),
  legal_entity_id uuid not null,
  code text not null,
  display_name text not null,
  status text not null default 'active' check (status in ('active', 'inactive')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id),
  unique (tenant_id, code)
);

create table identity_role_assignments (
  tenant_id uuid not null,
  assignment_id uuid not null,
  user_id uuid not null,
  role text not null check (role <> ''),
  legal_entity_id uuid,
  valid_from timestamptz not null default now(),
  valid_to timestamptz,
  created_at timestamptz not null default now(),
  primary key (tenant_id, assignment_id),
  foreign key (tenant_id, user_id) references identity_users (tenant_id, user_id),
  foreign key (tenant_id, legal_entity_id) references identity_legal_entities (tenant_id, legal_entity_id),
  check (valid_to is null or valid_to > valid_from),
  constraint identity_role_assignments_no_overlap exclude using gist (
    tenant_id with =,
    user_id with =,
    role with =,
    (coalesce(legal_entity_id, '00000000-0000-0000-0000-000000000000'::uuid)) with =,
    tstzrange(valid_from, valid_to) with &&
  )
);

create index identity_role_assignments_user_idx on identity_role_assignments (tenant_id, user_id, valid_from);

-- A session reset leaves passage.tenant_id as '' rather than unset; nullif turns that into NULL so the
-- comparison matches no rows instead of failing the ''::uuid cast.
alter table identity_tenants enable row level security;
alter table identity_tenants force row level security;
create policy identity_tenants_tenant_access on identity_tenants as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

alter table identity_users enable row level security;
alter table identity_users force row level security;
create policy identity_users_tenant_access on identity_users as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

alter table identity_legal_entities enable row level security;
alter table identity_legal_entities force row level security;
create policy identity_legal_entities_tenant_access on identity_legal_entities as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
create policy identity_legal_entities_legal_entity_scope on identity_legal_entities as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table identity_role_assignments enable row level security;
alter table identity_role_assignments force row level security;
create policy identity_role_assignments_tenant_access on identity_role_assignments as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
-- Decision: a caller scoped to specific legal entities does not see tenant-wide assignments
-- (legal_entity_id is null); only a '*' scope does.
create policy identity_role_assignments_legal_entity_scope on identity_role_assignments as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
