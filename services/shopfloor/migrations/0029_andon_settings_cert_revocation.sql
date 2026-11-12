-- #25: a tenant's andon thresholds, the node type of an edge write (its readback tolerance depends on it), and
-- revocation of edge certificates with their fingerprint.

CREATE TABLE IF NOT EXISTS andon_setting (
  tenant_id uuid PRIMARY KEY,
  downtime_minutes integer NOT NULL CHECK (downtime_minutes BETWEEN 1 AND 240),
  clear_minutes integer NOT NULL CHECK (clear_minutes BETWEEN 0 AND 120),
  scrap_percent integer NOT NULL CHECK (scrap_percent BETWEEN 1 AND 100),
  scrap_min_count integer NOT NULL CHECK (scrap_min_count BETWEEN 1 AND 100000),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE andon_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE andon_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY andon_setting_tenant_access ON andon_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE edge_write_audit ADD COLUMN IF NOT EXISTS node_type text NOT NULL DEFAULT '' CHECK (char_length(node_type) <= 20);

-- fingerprint is the SHA-256 of the certificate's DER (hex), what the gateway forwards as the certificate's Hash.
-- Certificates issued before this migration have none; edge_node_tenant_cert accepts them only until the node is
-- issued a fingerprinted one, and revoking one of them revokes them all (they cannot be told apart).
ALTER TABLE edge_certificate ADD COLUMN IF NOT EXISTS fingerprint text CHECK (fingerprint IS NULL OR fingerprint ~ '^[0-9a-f]{64}$');
ALTER TABLE edge_certificate ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
ALTER TABLE edge_certificate ADD COLUMN IF NOT EXISTS revoked_by text CHECK (revoked_by IS NULL OR char_length(revoked_by) BETWEEN 1 AND 200);
ALTER TABLE edge_certificate ADD COLUMN IF NOT EXISTS revoke_reason text CHECK (revoke_reason IS NULL OR char_length(revoke_reason) BETWEEN 1 AND 500);
CREATE INDEX IF NOT EXISTS edge_certificate_fingerprint ON edge_certificate (edge_id, fingerprint);

-- The tenant of a node for the connector identity, only when the presented certificate is one the node may use: its
-- fingerprint (the Hash the gateway forwards) is that of a certificate issued to the node and not revoked. A
-- certificate issued before fingerprints were kept is accepted only while the node has no fingerprinted certificate
-- (its first enrollment or renewal after this migration ends that) and one of its unfingerprinted certificates is
-- not revoked. No hash, no identity. Like edge_node_tenant it runs before any tenant is known and returns nothing else.
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edge_node_tenant_cert(p_id uuid, p_hash text) RETURNS uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT n.tenant_id FROM edge_node n
        WHERE n.id = p_id AND coalesce(p_hash, '') <> ''
          AND (EXISTS (SELECT 1 FROM edge_certificate c WHERE c.tenant_id = n.tenant_id AND c.edge_id = n.id
                         AND c.revoked_at IS NULL AND c.fingerprint = lower(p_hash))
               OR (NOT EXISTS (SELECT 1 FROM edge_certificate c WHERE c.tenant_id = n.tenant_id AND c.edge_id = n.id AND c.fingerprint IS NOT NULL)
                   AND EXISTS (SELECT 1 FROM edge_certificate c WHERE c.tenant_id = n.tenant_id AND c.edge_id = n.id
                                 AND c.fingerprint IS NULL AND c.revoked_at IS NULL)))
        LIMIT 1
      $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edge_node_tenant_cert(uuid, text) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON andon_setting TO passage_app';
    EXECUTE 'GRANT UPDATE ON edge_certificate TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION edge_node_tenant_cert(uuid, text) TO passage_app';
  END IF;
END
$$;
