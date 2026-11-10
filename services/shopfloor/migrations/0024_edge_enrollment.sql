-- PSG-25 #25 subtask 25-4: edge enrollment and certificate binding (FR-25.19, BR-25.14).
-- A supervisor mints a one-time enrollment token for an edge node (edge_enrollment_token, only the
-- SHA-256 of its secret is stored). The connector redeems it once with a certificate signing request;
-- the service issues a 90-day client certificate whose subject it chooses and binds that subject to the
-- node as connector_subject, so the identity the gateway forwards from the certificate is the only one
-- that may pull the node's configuration or report for it. edge_certificate is the append-only record of
-- every certificate issued (enrollment and renewal): serial, subject, validity and why.
-- New tables, so no lock on a live one.

CREATE TABLE IF NOT EXISTS edge_enrollment_token (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  edge_id uuid NOT NULL,
  token_hash text NOT NULL CHECK (char_length(token_hash) = 64),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  revoked_at timestamptz,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS edge_enrollment_token_hash ON edge_enrollment_token (tenant_id, token_hash);
CREATE INDEX IF NOT EXISTS edge_enrollment_token_edge ON edge_enrollment_token (tenant_id, edge_id) WHERE used_at IS NULL AND revoked_at IS NULL;
ALTER TABLE edge_enrollment_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE edge_enrollment_token FORCE ROW LEVEL SECURITY;
CREATE POLICY edge_enrollment_token_tenant_access ON edge_enrollment_token AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS edge_certificate (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  edge_id uuid NOT NULL,
  serial text NOT NULL CHECK (char_length(serial) BETWEEN 1 AND 200),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
  not_before timestamptz NOT NULL,
  not_after timestamptz NOT NULL,
  reason text NOT NULL CHECK (reason IN ('enroll', 'renew')),
  issued_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, serial),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS edge_certificate_edge ON edge_certificate (tenant_id, edge_id, issued_at DESC);
ALTER TABLE edge_certificate ENABLE ROW LEVEL SECURITY;
ALTER TABLE edge_certificate FORCE ROW LEVEL SECURITY;
CREATE POLICY edge_certificate_tenant_access ON edge_certificate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON edge_enrollment_token TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON edge_certificate TO passage_app';
  END IF;
END
$$;
