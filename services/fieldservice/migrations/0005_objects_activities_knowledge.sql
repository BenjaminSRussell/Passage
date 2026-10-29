-- Field service, slice 3 (PSG-4503): installed-base service objects with their
-- warranty terms (FR-45.1, FR-45.2), activities on any record (FR-45.14) and
-- knowledge articles with versions and helpfulness votes (FR-45.13, BR-45.11).
-- Every table is tenant-scoped under RLS; service_object carries legal_entity_id
-- and adds the restrictive legal-entity scope policy.

CREATE TABLE IF NOT EXISTS service_object (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  serial text NOT NULL CHECK (char_length(serial) BETWEEN 1 AND 100),
  customer_party_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_address_id uuid,
  parent_id uuid,
  shipment_id uuid,
  shipped_on date,
  installed_on date,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE', 'RETURNED')),
  warranty_trigger text NOT NULL CHECK (warranty_trigger IN ('SHIP', 'INSTALL', 'REGISTRATION')),
  warranty_months integer NOT NULL DEFAULT 0 CHECK (warranty_months BETWEEN 0 AND 240),
  -- NULL until the trigger date is known (an INSTALL warranty before installation).
  warranty_starts_on date,
  parts_covered boolean NOT NULL DEFAULT false,
  labor_covered boolean NOT NULL DEFAULT false,
  travel_covered boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, item_id, serial),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES service_object (tenant_id, id),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
CREATE INDEX IF NOT EXISTS service_object_customer ON service_object (tenant_id, customer_party_id, created_at);
CREATE INDEX IF NOT EXISTS service_object_serial ON service_object (tenant_id, serial);
CREATE INDEX IF NOT EXISTS service_object_parent ON service_object (tenant_id, parent_id);
ALTER TABLE service_object ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_object FORCE ROW LEVEL SECURITY;
CREATE POLICY service_object_tenant_access ON service_object AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_object_legal_entity_scope ON service_object AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_activity (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('TASK', 'APPOINTMENT', 'CALL_NOTE', 'EMAIL_NOTE')),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
  owner_worker_id uuid NOT NULL,
  due_at timestamptz,
  start_at timestamptz,
  end_at timestamptz,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE', 'CANCELED')),
  reminder_minutes integer CHECK (reminder_minutes IS NULL OR reminder_minutes BETWEEN 0 AND 10080),
  -- [{"entityType": "case", "entityId": "<uuid>"}, ...]
  associations jsonb NOT NULL DEFAULT '[]',
  completed_at timestamptz,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (start_at IS NULL OR end_at IS NULL OR end_at >= start_at),
  CHECK ((status = 'DONE') = (completed_at IS NOT NULL))
);
-- "My activities" (PRD 8.45.14) reads one owner's activities earliest due first.
CREATE INDEX IF NOT EXISTS service_activity_owner ON service_activity (tenant_id, owner_worker_id, due_at);
-- Activities on one record (a case, a project, a work order) use containment.
CREATE INDEX IF NOT EXISTS service_activity_associations ON service_activity USING gin (associations jsonb_path_ops);
ALTER TABLE service_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_activity FORCE ROW LEVEL SECURITY;
CREATE POLICY service_activity_tenant_access ON service_activity AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per article version; number (KB-{seq}) groups the versions of one article.
CREATE TABLE IF NOT EXISTS service_kb_article (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  -- Optimistic-concurrency token of this version row (the ETag).
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  language text NOT NULL CHECK (language ~ '^[a-z]{2}(-[A-Za-z]{2,4})?$'),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  symptom text NOT NULL DEFAULT '',
  cause text NOT NULL DEFAULT '',
  solution text NOT NULL CHECK (char_length(solution) >= 1),
  applicability jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'IN_REVIEW', 'PUBLISHED', 'RETIRED')),
  author_id uuid NOT NULL,
  reviewer_id uuid,
  published_at timestamptz,
  search tsvector GENERATED ALWAYS AS (to_tsvector('simple',
    coalesce(title, '') || ' ' || coalesce(symptom, '') || ' ' || coalesce(cause, '') || ' ' || coalesce(solution, ''))) STORED,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number, version),
  UNIQUE (tenant_id, idempotency_key),
  -- BR-45.11: reviewer differs from author (KB-PUB-001).
  CHECK (reviewer_id IS NULL OR reviewer_id <> author_id),
  CHECK (status <> 'PUBLISHED' OR published_at IS NOT NULL)
);
-- BR-45.11: only one PUBLISHED version per (article, language).
CREATE UNIQUE INDEX IF NOT EXISTS service_kb_article_one_published ON service_kb_article (tenant_id, number, language)
  WHERE status = 'PUBLISHED';
-- One version of an article is being worked on at a time.
CREATE UNIQUE INDEX IF NOT EXISTS service_kb_article_one_open ON service_kb_article (tenant_id, number)
  WHERE status IN ('DRAFT', 'IN_REVIEW');
CREATE INDEX IF NOT EXISTS service_kb_article_search ON service_kb_article USING gin (search);
CREATE INDEX IF NOT EXISTS service_kb_article_status ON service_kb_article (tenant_id, status, created_at);
ALTER TABLE service_kb_article ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_kb_article FORCE ROW LEVEL SECURITY;
CREATE POLICY service_kb_article_tenant_access ON service_kb_article AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One helpfulness vote per user and article version (BR-45.11); voting again changes the vote.
CREATE TABLE IF NOT EXISTS service_kb_vote (
  tenant_id uuid NOT NULL,
  article_id uuid NOT NULL,
  voter_id uuid NOT NULL,
  helpful boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, article_id, voter_id),
  FOREIGN KEY (tenant_id, article_id) REFERENCES service_kb_article (tenant_id, id)
);
ALTER TABLE service_kb_vote ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_kb_vote FORCE ROW LEVEL SECURITY;
CREATE POLICY service_kb_vote_tenant_access ON service_kb_vote AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges, in the style of 0002. The store updates
-- service_object (:install), service_activity (complete, cancel, reschedule) and
-- service_kb_article (edit, submit, publish, retire), locking each FOR UPDATE, and
-- upserts service_kb_vote. Nothing is deleted. Skipped when the role does not
-- exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_object, service_activity, service_kb_article, service_kb_vote TO passage_app';
  END IF;
END
$$;
