-- One row per SCIM deprovisioning (active=false or DELETE) that still has to reach Keycloak. The row is written in
-- the same transaction as the identity_users status change, so a committed deprovisioning always leaves a pending
-- Keycloak call; the worker (keycloak_deprovision.go) disables the Keycloak user and logs out its sessions, retries
-- with backoff, and parks the row after too many failures. idp_subject and user_name are copied at request time:
-- they are what the worker looks the Keycloak user up by. last_error holds the Keycloak status and error code only,
-- never a token or secret.
create table identity_keycloak_deprovisions (
  tenant_id uuid not null references identity_tenants (tenant_id),
  deprovision_id uuid not null,
  user_id uuid not null,
  idp_subject text not null,
  user_name text,
  reason text not null check (reason in ('scim_deactivated', 'scim_deleted')),
  requested_by text not null check (requested_by <> ''),
  requested_at timestamptz not null default now(),
  attempts int not null default 0 check (attempts >= 0),
  next_attempt_at timestamptz not null default now(),
  last_error text,
  completed_at timestamptz,
  outcome text check (outcome in ('disabled', 'not_found', 'superseded')),
  keycloak_user_id text,
  parked_at timestamptz,
  primary key (tenant_id, deprovision_id),
  foreign key (tenant_id, user_id) references identity_users (tenant_id, user_id),
  check ((completed_at is null) = (outcome is null)),
  check (completed_at is null or parked_at is null)
);

create index identity_keycloak_deprovisions_due_idx on identity_keycloak_deprovisions (tenant_id, next_attempt_at)
  where completed_at is null and parked_at is null;
create index identity_keycloak_deprovisions_user_idx on identity_keycloak_deprovisions (tenant_id, user_id)
  where completed_at is null;

alter table identity_keycloak_deprovisions enable row level security;
alter table identity_keycloak_deprovisions force row level security;
create policy identity_keycloak_deprovisions_tenant_access on identity_keycloak_deprovisions as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON identity_keycloak_deprovisions TO passage_app';
  END IF;
END
$$;
