create table identity_tenants (
  tenant_id uuid primary key,
  realm_name text not null unique,
  display_name text not null,
  status text not null default 'active' check (status in ('active', 'suspended', 'deprovisioned')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table identity_users (
  tenant_id uuid not null,
  user_id uuid not null,
  idp_subject text not null,
  email text,
  display_name text not null default '',
  status text not null default 'active' check (status in ('active', 'deprovisioned')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, user_id),
  unique (tenant_id, idp_subject),
  foreign key (tenant_id) references identity_tenants (tenant_id)
);

create table identity_legal_entities (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  code text not null,
  display_name text not null,
  status text not null default 'active' check (status in ('active', 'inactive')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id),
  unique (tenant_id, code),
  foreign key (tenant_id) references identity_tenants (tenant_id)
);
