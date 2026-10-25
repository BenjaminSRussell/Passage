CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE TABLE IF NOT EXISTS party (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_number text NOT NULL CHECK (char_length(party_number) <= 20),
  party_type text NOT NULL CHECK (party_type IN ('organization', 'person')),
  name text NOT NULL CHECK (char_length(name) <= 200),
  search_name text NOT NULL CHECK (char_length(search_name) <= 200),
  org_legal_name text,
  org_trade_name text,
  org_registration_number text,
  org_industry_code text,
  person_given_name text,
  person_middle_name text,
  person_family_name text,
  person_prefix text,
  person_suffix text,
  person_dob date,
  person_gender text,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'merged')),
  merged_into_id uuid,
  owner_user_id uuid,
  owner_team_id uuid,
  language_code text,
  time_zone text,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, party_number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, merged_into_id) REFERENCES party (tenant_id, id),
  CHECK (party_type <> 'organization' OR org_legal_name IS NOT NULL),
  CHECK (party_type <> 'person' OR (person_given_name IS NOT NULL AND person_family_name IS NOT NULL)),
  CHECK ((status = 'merged') = (merged_into_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS party_search_name_trgm ON party USING gin (search_name public.gin_trgm_ops);
CREATE INDEX IF NOT EXISTS party_type_status ON party (tenant_id, party_type, status);
CREATE INDEX IF NOT EXISTS party_search_name_block ON party (tenant_id, left(search_name, 4));

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  partition_key text,
  headers jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

CREATE TABLE IF NOT EXISTS payment_term (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  net_days integer CHECK (net_days >= 0),
  day_of_month integer CHECK (day_of_month BETWEEN 1 AND 31),
  offset_months integer NOT NULL DEFAULT 0 CHECK (offset_months >= 0),
  discount1_percent numeric(5,2),
  discount1_days integer,
  discount2_percent numeric(5,2),
  discount2_days integer,
  discount3_percent numeric(5,2),
  discount3_days integer,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  CHECK ((net_days IS NULL) <> (day_of_month IS NULL))
);

CREATE TABLE IF NOT EXISTS posting_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  receivable_account_id uuid,
  payable_account_id uuid,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code)
);

CREATE TABLE IF NOT EXISTS customer_role (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  account_number text NOT NULL CHECK (char_length(account_number) <= 20),
  customer_group_id uuid NOT NULL,
  payment_term_id uuid NOT NULL,
  delivery_term_id uuid,
  delivery_mode_id uuid,
  posting_profile_id uuid NOT NULL,
  invoice_account_party_id uuid,
  currency_code char(3) NOT NULL,
  credit_limit numeric(18,2),
  credit_currency char(3),
  credit_rating text,
  credit_hold boolean NOT NULL DEFAULT false,
  blocked text NOT NULL DEFAULT 'none' CHECK (blocked IN ('none', 'invoice', 'all')),
  defaulted_from_group text[] NOT NULL DEFAULT '{}',
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, party_id, legal_entity_id),
  UNIQUE (tenant_id, legal_entity_id, account_number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, party_id) REFERENCES party (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_term_id) REFERENCES payment_term (tenant_id, id),
  FOREIGN KEY (tenant_id, posting_profile_id) REFERENCES posting_profile (tenant_id, id)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['party', 'outbox', 'consumer_inbox', 'idempotency_keys', 'payment_term', 'posting_profile', 'customer_role'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['posting_profile', 'customer_role'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  END LOOP;
END
$$;
