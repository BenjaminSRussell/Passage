-- Mobile warehouse execution, wave 32: versioned flows, role and site menus,
-- devices and their sessions, the offline transaction log, the transactional
-- outbox and idempotency keys. Every table carries tenant_id first and forced
-- row-level security (dbx.RLSPolicy).

create table mobile_flow (
  tenant_id uuid not null,
  flow_id uuid not null,
  code text not null check (code ~ '^[A-Z][A-Z0-9_]{1,39}$'),
  version int not null check (version > 0),
  status text not null check (status in ('DRAFT', 'PUBLISHED', 'RETIRED')),
  entry_step_id text not null,
  steps jsonb not null,
  warehouses uuid[] not null default '{}',
  created_by text not null default '',
  published_at timestamptz,
  published_by text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, flow_id),
  unique (tenant_id, code, version)
);
create unique index ux_mobile_flow_one_draft on mobile_flow (tenant_id, code) where status = 'DRAFT';
create unique index ux_mobile_flow_one_published on mobile_flow (tenant_id, code) where status = 'PUBLISHED';

-- A published version never changes (invariant of 6.32.4): only its status may
-- move on, from PUBLISHED to RETIRED.
create function mobile_flow_guard() returns trigger language plpgsql as $$
begin
  if old.status <> 'DRAFT' then
    if (new.code, new.version, new.entry_step_id, new.steps, new.warehouses, new.published_at, new.published_by)
       is distinct from (old.code, old.version, old.entry_step_id, old.steps, old.warehouses, old.published_at, old.published_by) then
      raise exception 'a published flow version is immutable' using errcode = '23514';
    end if;
    if new.status <> old.status and not (old.status = 'PUBLISHED' and new.status = 'RETIRED') then
      raise exception 'a published flow version can only be retired' using errcode = '23514';
    end if;
  end if;
  return new;
end
$$;
create trigger mobile_flow_guard before update on mobile_flow for each row execute function mobile_flow_guard();

create table mobile_menu (
  tenant_id uuid not null,
  menu_id uuid not null,
  key text not null check (key ~ '^[a-z][a-z0-9_-]{0,39}$'),
  name text not null check (char_length(name) between 1 and 80),
  role text not null default '',
  warehouse_id uuid,
  flow_codes text[] not null default '{}',
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, menu_id)
);
create unique index ux_mobile_menu_scope on mobile_menu (tenant_id, key, role, coalesce(warehouse_id, '00000000-0000-0000-0000-000000000000'::uuid));

create table mobile_device (
  tenant_id uuid not null,
  device_id text not null check (char_length(device_id) between 1 and 80 and device_id !~ ':'),
  model text not null default '',
  scanner_profile text not null default 'ZEBRA_DATAWEDGE' check (scanner_profile in ('ZEBRA_DATAWEDGE', 'HONEYWELL_DC', 'KEYBOARD_WEDGE')),
  app_version text not null default '',
  warehouse_id uuid,
  menu_key text not null default '',
  skew_ms bigint not null default 0,
  last_seen_at timestamptz,
  registered_at timestamptz not null default now(),
  primary key (tenant_id, device_id)
);

create table device_session (
  tenant_id uuid not null,
  session_id uuid not null,
  device_id text not null,
  user_id text not null,
  warehouse_id uuid not null,
  menu_key text not null default 'default',
  current_work_id text not null default '',
  state jsonb not null default '{}',
  status text not null check (status in ('ACTIVE', 'STALE', 'ENDED')),
  end_reason text not null default '',
  version int not null default 1,
  started_at timestamptz not null default now(),
  last_seen_at timestamptz not null default now(),
  ended_at timestamptz,
  primary key (tenant_id, session_id),
  foreign key (tenant_id, device_id) references mobile_device (tenant_id, device_id)
);
-- A device has at most one open session (6.32.4).
create unique index ux_device_session_open on device_session (tenant_id, device_id) where status in ('ACTIVE', 'STALE');
create index ix_device_session_user on device_session (tenant_id, user_id) where status = 'ACTIVE';
create index ix_device_session_work on device_session (tenant_id, current_work_id) where current_work_id <> '' and status in ('ACTIVE', 'STALE');
create index ix_device_session_sweep on device_session (tenant_id, last_seen_at) where status = 'ACTIVE';

create table offline_transaction (
  tenant_id uuid not null,
  idempotency_key text not null,
  device_id text not null,
  session_id uuid,
  user_id text not null,
  seq bigint not null check (seq > 0),
  command text not null check (char_length(command) between 1 and 60),
  payload jsonb not null,
  payload_hash text not null,
  captured_at timestamptz not null,
  received_at timestamptz not null default now(),
  replay_status text not null check (replay_status in ('PENDING', 'APPLIED', 'CONFLICT', 'REJECTED')),
  conflict_reason text not null default '',
  resolution text not null default '' check (resolution in ('', 'REAPPLY', 'REJECT')),
  resolved_by text not null default '',
  resolved_at timestamptz,
  version int not null default 1,
  primary key (tenant_id, idempotency_key),
  unique (tenant_id, device_id, seq)
);
create index ix_offline_conflict on offline_transaction (tenant_id, captured_at) where replay_status = 'CONFLICT' and resolution = '';
create index ix_offline_pending on offline_transaction (tenant_id, device_id, seq) where replay_status = 'PENDING';

create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);

create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);

alter table mobile_flow enable row level security;
alter table mobile_flow force row level security;
create policy mobile_flow_tenant_access on mobile_flow as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table mobile_menu enable row level security;
alter table mobile_menu force row level security;
create policy mobile_menu_tenant_access on mobile_menu as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table mobile_device enable row level security;
alter table mobile_device force row level security;
create policy mobile_device_tenant_access on mobile_device as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table device_session enable row level security;
alter table device_session force row level security;
create policy device_session_tenant_access on device_session as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table offline_transaction enable row level security;
alter table offline_transaction force row level security;
create policy offline_transaction_tenant_access on offline_transaction as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table outbox enable row level security;
alter table outbox force row level security;
create policy outbox_tenant_access on outbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table idempotency_keys enable row level security;
alter table idempotency_keys force row level security;
create policy idempotency_keys_tenant_access on idempotency_keys as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
