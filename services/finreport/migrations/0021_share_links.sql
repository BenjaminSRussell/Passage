-- PSG-1519 (wave 15, slice 19): share links with expiry for report library files
-- (FR-15.8 "shared link with expiry", PRD §2.15.6 report library "share link",
-- §2.15.14 `reporting.share_link_expiry_days`: 14 days by default, 1 to 90).
--
-- report_share_link is a bearer link to one stored library file. The link's
-- secret is shown once, when it is created; only its SHA-256 (token_hash) is
-- stored, so a database read cannot recover a working link. A link works until
-- expires_at or until it is revoked, whichever is first (status is derived,
-- never stored). legal_entity_id is the file's, copied at creation, so the
-- legal-entity policy covers links the way it covers the files.
--
-- A link never outlives its file: the foreign key removes it when the
-- retention sweep deletes the file (ON DELETE CASCADE runs as the table owner,
-- so the application role needs no DELETE here), and a link is created with an
-- expiry no later than the file's retain_until.
--
-- The application role inserts links, revokes them (UPDATE of the revoke
-- columns and revision only, so it cannot move a link to another file or
-- extend its expiry) and counts downloads (UPDATE of the two download columns).

CREATE TABLE IF NOT EXISTS report_share_link (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  library_id uuid NOT NULL,
  legal_entity_id uuid,
  token_hash text NOT NULL CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  created_by text NOT NULL CHECK (created_by <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_by text,
  revoked_at timestamptz,
  download_count integer NOT NULL DEFAULT 0 CHECK (download_count >= 0),
  last_downloaded_at timestamptz,
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
  request_hash text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, token_hash),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (expires_at > created_at),
  CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
  FOREIGN KEY (tenant_id, library_id) REFERENCES report_library_entry (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS report_share_link_by_file
  ON report_share_link (tenant_id, library_id, created_at DESC, id DESC);
ALTER TABLE report_share_link ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_share_link FORCE ROW LEVEL SECURITY;
CREATE POLICY report_share_link_tenant_access ON report_share_link AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY report_share_link_legal_entity_scope ON report_share_link AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The audit trail records the creation and revocation of links (the kind list is a CHECK, so it is replaced).
ALTER TABLE audit_trail_entry DROP CONSTRAINT IF EXISTS audit_trail_entry_kind_check;
ALTER TABLE audit_trail_entry ADD CONSTRAINT audit_trail_entry_kind_check CHECK (kind IN (
    'posted', 'replayed', 'reversed', 'batch_submitted', 'batch_approved', 'batch_rejected',
    'period_changed', 'reopen_approved', 'rate_override', 'tax_fallback', 'allocation_run',
    'year_end_run', 'rebuild',
    'statement_run', 'definition_published', 'definition_retired', 'hierarchy_published',
    'schedule_saved', 'audit_exported',
    'legal_hold_placed', 'legal_hold_released', 'library_swept', 'runs_swept',
    'share_link_created', 'share_link_revoked'));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON report_share_link TO passage_app';
    EXECUTE 'GRANT UPDATE (revoked_at, revoked_by, revision, download_count, last_downloaded_at) ON report_share_link TO passage_app';
  END IF;
END
$$;
