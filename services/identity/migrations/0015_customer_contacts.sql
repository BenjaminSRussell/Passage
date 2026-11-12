-- Customer portal contacts (#30): the people who sign in to the customer portal for one customer. Keycloak holds the account; this
-- table is the registry an administrator works from: who was invited, for which customer and legal entities, and whether the
-- account is still enabled. The Keycloak user carries the same mapping as the attributes tenant_id, legal_entity_id and customer_id
-- (the claims the portal BFF reads), so a change here is written to both.
create table identity_customer_contacts (
  tenant_id uuid not null references identity_tenants (tenant_id),
  contact_id uuid not null,
  keycloak_user_id uuid not null,
  email text not null check (email <> '' and char_length(email) <= 254),
  display_name text not null default '' check (char_length(display_name) <= 200),
  customer_id uuid not null,
  legal_entity_ids uuid[] not null check (cardinality(legal_entity_ids) between 1 and 20),
  status text not null default 'enabled' check (status in ('enabled', 'disabled')),
  invited_by text not null check (invited_by <> ''),
  invited_at timestamptz not null default now(),
  last_invitation_sent_at timestamptz,
  updated_by text not null check (updated_by <> ''),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, contact_id)
);

-- One contact per address in a tenant, whatever the case of the address.
create unique index identity_customer_contacts_email on identity_customer_contacts (tenant_id, lower(email));
create unique index identity_customer_contacts_keycloak_user on identity_customer_contacts (tenant_id, keycloak_user_id);
create index identity_customer_contacts_customer on identity_customer_contacts (tenant_id, customer_id);

alter table identity_customer_contacts enable row level security;
alter table identity_customer_contacts force row level security;
create policy identity_customer_contacts_tenant_access on identity_customer_contacts as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON identity_customer_contacts TO passage_app';
  END IF;
END
$$;
