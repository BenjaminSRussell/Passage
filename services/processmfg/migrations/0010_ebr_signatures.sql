-- Process manufacturing, wave 24 AC4 (#480): 21 CFR Part 11 electronic
-- signatures on the electronic batch record (FR-24.22, BR-24.14). The service
-- now makes the signature itself: the signer re-authenticates (the step-up
-- seam of the service, 11.200), and the signature row records the signer, the
-- printed name, the meaning (reviewed on an exception decision, approved on
-- QA approval), the time, the method the re-authenticator proved and the
-- SHA-256 of the canonical record it was bound to (11.50, 11.70). Signatures
-- are append-only, also for the table owner. Failed re-authentications are
-- counted per signer for the BR-24.14 lockout (three failures lock signing
-- for 15 minutes).

-- electronic_batch_record and ebr_exception are live tables: their new keys
-- are added NOT VALID (no scan under the lock) and hold for every new row.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS ebr_signature (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  record_id uuid NOT NULL,
  -- The exception decided under a 'reviewed' signature; null for 'approved'.
  exception_id uuid,
  meaning text NOT NULL CHECK (meaning IN ('reviewed', 'approved')),
  signer_id text NOT NULL CHECK (signer_id <> ''),
  printed_name text NOT NULL CHECK (char_length(printed_name) BETWEEN 1 AND 200),
  method text NOT NULL CHECK (method <> ''),
  record_hash bytea NOT NULL CHECK (length(record_hash) = 32),
  signed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, record_id) REFERENCES electronic_batch_record (tenant_id, id),
  FOREIGN KEY (tenant_id, exception_id) REFERENCES ebr_exception (tenant_id, id),
  CHECK ((meaning = 'reviewed') = (exception_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS ebr_signature_record_idx ON ebr_signature (tenant_id, record_id, signed_at);
-- One reviewed signature per exception and one approval per record.
CREATE UNIQUE INDEX IF NOT EXISTS ebr_signature_exception_idx ON ebr_signature (tenant_id, exception_id) WHERE exception_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ebr_signature_approval_idx ON ebr_signature (tenant_id, record_id) WHERE meaning = 'approved';

CREATE FUNCTION ebr_signature_guard() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'ebr_signature % is append-only', OLD.id;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER ebr_signature_append_only
  BEFORE UPDATE OR DELETE ON ebr_signature
  FOR EACH ROW EXECUTE PROCEDURE ebr_signature_guard();

-- A decision made from now on names a signature of this table. Exceptions
-- decided before this migration carry a client-given id that was never
-- verified, so the key is NOT VALID: it holds for every new decision and the
-- sealed document reports those older ones as unverified.
ALTER TABLE ebr_exception
  ADD CONSTRAINT ebr_exception_signature_fk FOREIGN KEY (tenant_id, signature_id) REFERENCES ebr_signature (tenant_id, id) NOT VALID;

ALTER TABLE electronic_batch_record ADD COLUMN IF NOT EXISTS approval_signature_id uuid;
ALTER TABLE electronic_batch_record
  ADD CONSTRAINT electronic_batch_record_approval_signature_fk FOREIGN KEY (tenant_id, approval_signature_id) REFERENCES ebr_signature (tenant_id, id) NOT VALID;

-- The approval signature is part of what an approved record keeps.
CREATE OR REPLACE FUNCTION electronic_batch_record_guard() RETURNS trigger AS $$
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
     OR (NEW.approved_by, NEW.approved_at, NEW.compiled_at, NEW.approval_signature_id)
        IS DISTINCT FROM (OLD.approved_by, OLD.approved_at, OLD.compiled_at, OLD.approval_signature_id)) THEN
    RAISE EXCEPTION 'electronic_batch_record % is approved: it is only sealed, with the approved content', OLD.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Failed re-authentications per signer (BR-24.14). Signing is tenant-wide, so
-- the row has no legal entity.
CREATE TABLE IF NOT EXISTS ebr_signer_lockout (
  tenant_id uuid NOT NULL,
  signer_id text NOT NULL,
  failures integer NOT NULL DEFAULT 0 CHECK (failures >= 0),
  locked_until timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, signer_id)
);

ALTER TABLE ebr_signature ENABLE ROW LEVEL SECURITY;
ALTER TABLE ebr_signature FORCE ROW LEVEL SECURITY;
CREATE POLICY ebr_signature_tenant_access ON ebr_signature AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ebr_signature_legal_entity_scope ON ebr_signature AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ebr_signer_lockout ENABLE ROW LEVEL SECURITY;
ALTER TABLE ebr_signer_lockout FORCE ROW LEVEL SECURITY;
CREATE POLICY ebr_signer_lockout_tenant_access ON ebr_signer_lockout AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ebr_signature TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ebr_signer_lockout TO passage_app';
  END IF;
END
$$;
