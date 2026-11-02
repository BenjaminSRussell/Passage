-- Process manufacturing, wave 24 slice 15: the electronic batch record with
-- review by exception (FR-24.21, BR-24.15, AC-24.16). A batch record profile
-- says an item's batches get an electronic batch record and whether releasing
-- a batch (disposition available) needs the record approved. The record is
-- compiled from a batch order and its registered batch, carries one exception
-- per value outside limits (and any exception a reviewer adds), is approved
-- only when every exception has a disposition and a signature and the
-- approver took no part in the review or the batch order, and is then sealed:
-- the SHA-256 of its canonical JSON document is stored with the document, and
-- a sealed record is never changed again (enforced by trigger).

CREATE TABLE IF NOT EXISTS batch_record_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  item_id uuid NOT NULL,
  approval_required_for_release boolean NOT NULL DEFAULT true,
  retired_at timestamptz,
  retired_by text,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK ((retired_at IS NULL) = (retired_by IS NULL))
);
-- One active profile per item and legal entity; the code is unique among the
-- active ones.
CREATE UNIQUE INDEX IF NOT EXISTS batch_record_profile_item_idx ON batch_record_profile (tenant_id, legal_entity_id, item_id) WHERE retired_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS batch_record_profile_code_idx ON batch_record_profile (tenant_id, legal_entity_id, code) WHERE retired_at IS NULL;

CREATE TABLE IF NOT EXISTS electronic_batch_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_order_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  profile_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'in-review' CHECK (status IN ('in-review', 'approved', 'sealed')),
  -- The compiled sections (order header, batch, attributes, dispositions,
  -- allocation) as of the last compile; the exceptions are rows of their own.
  content jsonb NOT NULL,
  compiled_at timestamptz NOT NULL DEFAULT now(),
  approved_by text,
  approved_at timestamptz,
  -- Set when sealed: the exact JSON document that was hashed, and its SHA-256.
  sealed_document text,
  content_hash bytea,
  sealed_by text,
  sealed_at timestamptz,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, batch_id),
  FOREIGN KEY (tenant_id, batch_order_id) REFERENCES batch_order (tenant_id, id),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES batch_record (tenant_id, id),
  FOREIGN KEY (tenant_id, profile_id) REFERENCES batch_record_profile (tenant_id, id),
  CHECK ((status = 'sealed') = (sealed_document IS NOT NULL AND content_hash IS NOT NULL AND sealed_at IS NOT NULL)),
  -- The stored hash is the SHA-256 of exactly the stored document (4.24.4).
  CHECK (sealed_document IS NULL OR content_hash = sha256(convert_to(sealed_document, 'UTF8'))),
  CHECK (status = 'in-review' OR (approved_at IS NOT NULL AND approved_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS electronic_batch_record_status_idx ON electronic_batch_record (tenant_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS ebr_exception (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  record_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  -- attribute: a recorded value outside its limits (source_id is the history
  -- row); manual: raised by a reviewer (source_id is its own id).
  source_type text NOT NULL CHECK (source_type IN ('attribute', 'manual')),
  source_id text NOT NULL,
  description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500),
  severity text NOT NULL DEFAULT 'major' CHECK (severity IN ('minor', 'major', 'critical')),
  disposition text CHECK (disposition IN ('accepted', 'deviation-raised', 'rejected')),
  disposition_comment text,
  -- The signature id is meant to reference the signature service (FR-24.22),
  -- which is not built yet: it is stored as given and NOT verified.
  signature_id uuid,
  dispositioned_by text,
  dispositioned_at timestamptz,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, record_id, source_type, source_id),
  FOREIGN KEY (tenant_id, record_id) REFERENCES electronic_batch_record (tenant_id, id),
  CHECK ((disposition IS NULL) = (signature_id IS NULL AND dispositioned_by IS NULL AND dispositioned_at IS NULL))
);

-- Immutability is held by the database, not only by the store: a sealed
-- record never changes and is never deleted; an approved record only moves to
-- sealed, with the content that was approved; a record keeps its identity.
CREATE FUNCTION electronic_batch_record_guard() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'electronic_batch_record % is never deleted', OLD.id;
  END IF;
  IF OLD.status = 'sealed' THEN
    RAISE EXCEPTION 'electronic_batch_record % is sealed and immutable', OLD.id;
  END IF;
  IF (NEW.tenant_id, NEW.id, NEW.legal_entity_id, NEW.batch_order_id, NEW.batch_id, NEW.profile_id, NEW.created_by, NEW.created_at)
     IS DISTINCT FROM (OLD.tenant_id, OLD.id, OLD.legal_entity_id, OLD.batch_order_id, OLD.batch_id, OLD.profile_id, OLD.created_by, OLD.created_at) THEN
    RAISE EXCEPTION 'electronic_batch_record % keeps its identity', OLD.id;
  END IF;
  IF OLD.status = 'approved' AND (NEW.status <> 'sealed' OR NEW.content IS DISTINCT FROM OLD.content
     OR (NEW.approved_by, NEW.approved_at, NEW.compiled_at) IS DISTINCT FROM (OLD.approved_by, OLD.approved_at, OLD.compiled_at)) THEN
    RAISE EXCEPTION 'electronic_batch_record % is approved: it is only sealed, with the approved content', OLD.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER electronic_batch_record_immutable
  BEFORE UPDATE OR DELETE ON electronic_batch_record
  FOR EACH ROW EXECUTE PROCEDURE electronic_batch_record_guard();

-- An exception is added or decided only while its record is in review, a
-- decision is final, and nothing is deleted.
CREATE FUNCTION ebr_exception_guard() RETURNS trigger AS $$
DECLARE
  st text;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'ebr_exception % is never deleted', OLD.id;
  END IF;
  SELECT status INTO st FROM electronic_batch_record WHERE tenant_id = NEW.tenant_id AND id = NEW.record_id;
  IF st IS DISTINCT FROM 'in-review' THEN
    RAISE EXCEPTION 'batch record % is %; its exceptions no longer change', NEW.record_id, coalesce(st, 'missing');
  END IF;
  IF TG_OP = 'UPDATE' THEN
    IF OLD.disposition IS NOT NULL THEN
      RAISE EXCEPTION 'ebr_exception % is decided; the decision is final', OLD.id;
    END IF;
    IF (NEW.tenant_id, NEW.id, NEW.record_id, NEW.legal_entity_id, NEW.source_type, NEW.source_id, NEW.description, NEW.severity, NEW.created_by, NEW.created_at)
       IS DISTINCT FROM (OLD.tenant_id, OLD.id, OLD.record_id, OLD.legal_entity_id, OLD.source_type, OLD.source_id, OLD.description, OLD.severity, OLD.created_by, OLD.created_at) THEN
      RAISE EXCEPTION 'ebr_exception % keeps what was raised; only its decision is recorded', OLD.id;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER ebr_exception_immutable
  BEFORE INSERT OR UPDATE OR DELETE ON ebr_exception
  FOR EACH ROW EXECUTE PROCEDURE ebr_exception_guard();

ALTER TABLE batch_record_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_record_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_record_profile_tenant_access ON batch_record_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_record_profile_legal_entity_scope ON batch_record_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE electronic_batch_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE electronic_batch_record FORCE ROW LEVEL SECURITY;
CREATE POLICY electronic_batch_record_tenant_access ON electronic_batch_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY electronic_batch_record_legal_entity_scope ON electronic_batch_record AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ebr_exception ENABLE ROW LEVEL SECURITY;
ALTER TABLE ebr_exception FORCE ROW LEVEL SECURITY;
CREATE POLICY ebr_exception_tenant_access ON ebr_exception AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ebr_exception_legal_entity_scope ON ebr_exception AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The profile is updated when retired, the record on every review step and
-- the exception when it is decided; nothing is deleted (and the triggers above
-- refuse it even for the owner).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON batch_record_profile, electronic_batch_record, ebr_exception TO passage_app';
  END IF;
END
$$;
