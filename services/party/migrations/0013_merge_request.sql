-- #450: a merge is requested, then approved by someone else through workflow-ts, and only then executed.
-- The request is tenant-level like party_merge (parties span legal entities), so it carries no legal entity.
CREATE TABLE IF NOT EXISTS merge_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  survivor_party_id uuid NOT NULL,
  merged_party_id uuid NOT NULL,
  rules jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
  requested_by text NOT NULL CHECK (requested_by <> ''),
  decided_by text CHECK (decided_by IS NULL OR decided_by <> ''),
  decision_reason text CHECK (char_length(decision_reason) <= 500),
  merge_id uuid,
  idempotency_key text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, survivor_party_id) REFERENCES party (tenant_id, id),
  FOREIGN KEY (tenant_id, merged_party_id) REFERENCES party (tenant_id, id),
  FOREIGN KEY (tenant_id, merge_id) REFERENCES party_merge (tenant_id, id),
  CHECK (survivor_party_id <> merged_party_id),
  CHECK ((status = 'pending') = (decided_by IS NULL)),
  CHECK ((status = 'pending') = (decided_at IS NULL)),
  CHECK ((status = 'approved') = (merge_id IS NOT NULL)),
  -- Segregation of duties: the requester never decides their own merge.
  CHECK (decided_by IS NULL OR decided_by <> requested_by)
);

CREATE UNIQUE INDEX IF NOT EXISTS merge_request_one_open
  ON merge_request (tenant_id, survivor_party_id, merged_party_id) WHERE status = 'pending';
CREATE UNIQUE INDEX IF NOT EXISTS merge_request_idempotency_key
  ON merge_request (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

ALTER TABLE merge_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE merge_request FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS merge_request_tenant_access ON merge_request;
CREATE POLICY merge_request_tenant_access ON merge_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON merge_request TO passage_app';
  END IF;
END
$$;
