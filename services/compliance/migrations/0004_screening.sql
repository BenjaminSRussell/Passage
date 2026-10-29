-- Denied-party and sanctions screening hits (FR-48.19, BR-48.15, AC-48.16).
-- A hit is OPEN until a TradeComplianceOfficer clears it as a false positive or
-- confirms it. A FALSE_POSITIVE row is also the whitelist entry of BR-48.15: it
-- holds the list entry version and the subject fingerprint (normalized name,
-- address and country) it was granted on, so the same party is not flagged again
-- for the same entry until either changes. Rows are never deleted: decisions are
-- kept 5 years (retention job with the evidence store).
CREATE TABLE IF NOT EXISTS screening_hit (
  tenant_id uuid NOT NULL,
  hit_id uuid NOT NULL,
  subject_type text NOT NULL CHECK (subject_type IN ('PARTY', 'DOCUMENT')),
  subject_id uuid NOT NULL,
  subject_name text NOT NULL CHECK (btrim(subject_name) <> ''),
  subject_fingerprint text NOT NULL CHECK (subject_fingerprint ~ '^[0-9a-f]{64}$'),
  list_code text NOT NULL CHECK (btrim(list_code) <> ''),
  list_entry_id text NOT NULL CHECK (btrim(list_entry_id) <> ''),
  list_entry_version integer NOT NULL CHECK (list_entry_version >= 1),
  list_entry_name text NOT NULL,
  score numeric(4,3) NOT NULL CHECK (score >= 0 AND score <= 1),
  matched_fields text[] NOT NULL,
  state text NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN', 'FALSE_POSITIVE', 'CONFIRMED')),
  decided_by text,
  reason text,
  decided_at timestamptz,
  detected_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  PRIMARY KEY (tenant_id, hit_id),
  CHECK ((state = 'OPEN') = (decided_at IS NULL)),
  CHECK (state = 'OPEN' OR (btrim(coalesce(decided_by, '')) <> '' AND btrim(coalesce(reason, '')) <> ''))
);
-- One undecided or confirmed hit per subject and list entry; cleared hits stay as history.
CREATE UNIQUE INDEX IF NOT EXISTS screening_hit_active
  ON screening_hit (tenant_id, subject_type, subject_id, list_code, list_entry_id) WHERE state <> 'FALSE_POSITIVE';
CREATE INDEX IF NOT EXISTS screening_hit_subject ON screening_hit (tenant_id, subject_type, subject_id, state);
CREATE INDEX IF NOT EXISTS screening_hit_listing ON screening_hit (tenant_id, detected_at DESC, hit_id DESC);

ALTER TABLE screening_hit ENABLE ROW LEVEL SECURITY;
ALTER TABLE screening_hit FORCE ROW LEVEL SECURITY;
CREATE POLICY screening_hit_tenant_access ON screening_hit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- passage_app reads, opens (insert, refresh of an OPEN hit on rescreen) and decides
-- hits (update under FOR UPDATE); it never deletes one.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON screening_hit TO passage_app';
  END IF;
END
$$;
