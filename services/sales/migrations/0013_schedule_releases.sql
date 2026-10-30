-- Wave 27 slice 9: scheduling-agreement releases stored against a sales
-- agreement, with cumulative reconciliation and atomic replacement of the open
-- schedule lines (FR-27.20, BR-27.12, AC-27.13).

-- cum_shipped is the quantity shipped since cum_reset_date; wave 28 shipments
-- will drive it, until then it is recorded through the API.
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS cum_tolerance numeric(20,6) NOT NULL DEFAULT 0 CHECK (cum_tolerance >= 0);
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS cum_shipped numeric(20,6) NOT NULL DEFAULT 0 CHECK (cum_shipped >= 0);
ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS cum_reset_date date;

CREATE TABLE IF NOT EXISTS schedule_release (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  release_no text NOT NULL CHECK (length(release_no) BETWEEN 1 AND 40),
  release_type text NOT NULL CHECK (release_type IN ('FORECAST', 'JIT')),
  status text NOT NULL CHECK (status IN ('ACCEPTED', 'HELD', 'REJECTED', 'SUPERSEDED')),
  cum_received numeric(20,6) NOT NULL CHECK (cum_received >= 0),
  cum_shipped numeric(20,6) NOT NULL CHECK (cum_shipped >= 0),
  -- shipped_offset is the quantity shipped in earlier cumulative periods that
  -- still counts against this release's cumulative figures: a reset of the
  -- agreement (cum_reset_date moving forward) adds the old period's shipped
  -- quantity, so open lines stay "cumulative required - shipped" on the
  -- release's own basis instead of reopening what was already shipped.
  shipped_offset numeric(20,6) NOT NULL DEFAULT 0 CHECK (shipped_offset >= 0),
  tolerance numeric(20,6) NOT NULL CHECK (tolerance >= 0),
  difference numeric(20,6) NOT NULL,
  open_qty numeric(20,6) NOT NULL CHECK (open_qty >= 0),
  buckets jsonb NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  received_by text NOT NULL,
  received_at timestamptz NOT NULL,
  resolved_by text,
  resolved_at timestamptz,
  resolution_reason text CHECK (resolution_reason IS NULL OR length(resolution_reason) BETWEEN 1 AND 500),
  version integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, agreement_id, release_no),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES sales_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS schedule_release_agreement ON schedule_release (tenant_id, agreement_id, received_at);

CREATE TABLE IF NOT EXISTS schedule_release_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  release_id uuid NOT NULL,
  due_date date NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  status text NOT NULL CHECK (status IN ('OPEN', 'SUPERSEDED')),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, release_id) REFERENCES schedule_release (tenant_id, id)
);
-- At most one release is in effect per agreement.
CREATE UNIQUE INDEX IF NOT EXISTS schedule_release_effective ON schedule_release (tenant_id, agreement_id) WHERE status = 'ACCEPTED';

CREATE INDEX IF NOT EXISTS schedule_release_line_release ON schedule_release_line (tenant_id, release_id);
CREATE INDEX IF NOT EXISTS schedule_release_line_open ON schedule_release_line (tenant_id, agreement_id) WHERE status = 'OPEN';

ALTER TABLE schedule_release ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_release FORCE ROW LEVEL SECURITY;
CREATE POLICY schedule_release_tenant_access ON schedule_release AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY schedule_release_legal_entity_scope ON schedule_release AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE schedule_release_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_release_line FORCE ROW LEVEL SECURITY;
CREATE POLICY schedule_release_line_tenant_access ON schedule_release_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY schedule_release_line_legal_entity_scope ON schedule_release_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts releases and lines and updates their status and
-- quantities; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON schedule_release, schedule_release_line TO passage_app';
  END IF;
END
$$;
