-- GDPR and CCPA subject requests (FR-48.10, FR-48.11, AC-48.6, AC-48.7).
-- A request is taken in RECEIVED with its deadline (30 days GDPR, 45 days CCPA,
-- or a shorter tenant override) and moves through VERIFIED, DISCOVERED or HELD,
-- APPROVED, DELIVERED and CLOSED, or REJECTED from any open state. hold_ids are
-- the legal holds found at discovery; an ERASURE stays HELD while any remains.
-- Rows are never deleted: closed requests are the evidence of the response.
CREATE TABLE IF NOT EXISTS subject_request (
  tenant_id uuid NOT NULL,
  request_id uuid NOT NULL,
  type text NOT NULL CHECK (type IN ('ACCESS', 'PORTABILITY', 'ERASURE', 'RECTIFICATION', 'RESTRICTION')),
  regulation text NOT NULL CHECK (regulation IN ('GDPR', 'CCPA')),
  subject_ref text NOT NULL CHECK (btrim(subject_ref) <> ''),
  received_at timestamptz NOT NULL,
  deadline_at timestamptz NOT NULL CHECK (deadline_at > received_at),
  state text NOT NULL DEFAULT 'RECEIVED'
    CHECK (state IN ('RECEIVED', 'VERIFIED', 'DISCOVERED', 'HELD', 'APPROVED', 'DELIVERED', 'CLOSED', 'REJECTED')),
  hold_ids text[] NOT NULL DEFAULT '{}',
  verified_at timestamptz,
  privacy_officer_id text,
  approved_at timestamptz,
  delivered_at timestamptz,
  closed_at timestamptz,
  late boolean NOT NULL DEFAULT false,
  justification text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, request_id),
  CHECK ((state IN ('CLOSED', 'REJECTED')) = (closed_at IS NOT NULL)),
  CHECK (state <> 'HELD' OR cardinality(hold_ids) > 0),
  CHECK (state <> 'REJECTED' OR btrim(coalesce(justification, '')) <> '')
);
CREATE INDEX IF NOT EXISTS subject_request_deadline ON subject_request (tenant_id, deadline_at, request_id);
CREATE INDEX IF NOT EXISTS subject_request_state ON subject_request (tenant_id, state, deadline_at);

ALTER TABLE subject_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE subject_request FORCE ROW LEVEL SECURITY;
CREATE POLICY subject_request_tenant_access ON subject_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- passage_app takes requests in (insert) and moves them through their states
-- (update under FOR UPDATE); it never deletes one.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON subject_request TO passage_app';
  END IF;
END
$$;
