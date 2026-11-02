-- PSG-25 slice 14: edge fleet (FR-25.19). An edge node is one iot-edge connector the plant runs.
-- The shopfloor service keeps its ring, its last heartbeat (every 30 s, buffer fill and the
-- status of its machines) and the versioned configuration documents it pulls. Each document
-- version is immutable once published; a node reports the version it runs in its heartbeat and
-- only one version is applied at a time (applied_version on the node, applied_at on the version
-- row for the audit trail). Heartbeats update the node row but never its version, so an editor
-- holding If-Match is not disturbed by a connector reporting in.
--
-- connector_subject is the identity the connector authenticates as (the subject the gateway
-- forwards). Only that identity may pull the node's configuration (which carries the write
-- allowlist) or report for it; a node without one refuses both. One identity serves one node.
-- A published configuration is immutable: passage_app may update only applied_at, and a trigger
-- refuses any other change and any second stamp of applied_at, and refuses deletes.

CREATE TABLE IF NOT EXISTS edge_node (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  site_id uuid,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  platform text NOT NULL CHECK (platform IN ('k3s', 'windows', 'linux')),
  ring text NOT NULL DEFAULT 'all' CHECK (ring IN ('canary', 'ring1', 'all')),
  connector_subject text CHECK (connector_subject IS NULL OR char_length(connector_subject) BETWEEN 1 AND 200),
  connector_version text CHECK (connector_version IS NULL OR char_length(connector_version) BETWEEN 1 AND 60),
  cert_serial text CHECK (cert_serial IS NULL OR char_length(cert_serial) BETWEEN 1 AND 200),
  cert_expires_at timestamptz,
  last_heartbeat_at timestamptz,
  buffer_fill_pct numeric(5,2) CHECK (buffer_fill_pct IS NULL OR (buffer_fill_pct >= 0 AND buffer_fill_pct <= 100)),
  machine_status jsonb NOT NULL DEFAULT '[]'::jsonb,
  applied_version integer CHECK (applied_version IS NULL OR applied_version >= 1),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS edge_node_connector_subject ON edge_node (tenant_id, connector_subject)
  WHERE connector_subject IS NOT NULL;
ALTER TABLE edge_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE edge_node FORCE ROW LEVEL SECURITY;
CREATE POLICY edge_node_tenant_access ON edge_node AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS edge_config_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  edge_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  document jsonb NOT NULL,
  published_by text NOT NULL CHECK (char_length(published_by) BETWEEN 1 AND 200),
  published_at timestamptz NOT NULL DEFAULT now(),
  applied_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, edge_id, version),
  FOREIGN KEY (tenant_id, edge_id) REFERENCES edge_node (tenant_id, id)
);
CREATE OR REPLACE FUNCTION edge_config_version_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'edge_config_version is immutable: a published version cannot be deleted' USING ERRCODE = 'P0001';
  END IF;
  IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.id IS DISTINCT FROM OLD.id OR NEW.edge_id IS DISTINCT FROM OLD.edge_id
     OR NEW.version IS DISTINCT FROM OLD.version OR NEW.document IS DISTINCT FROM OLD.document
     OR NEW.published_by IS DISTINCT FROM OLD.published_by OR NEW.published_at IS DISTINCT FROM OLD.published_at
     OR (OLD.applied_at IS NOT NULL AND NEW.applied_at IS DISTINCT FROM OLD.applied_at) THEN
    RAISE EXCEPTION 'edge_config_version is immutable: only the first applied_at may be stamped' USING ERRCODE = 'P0001';
  END IF;
  RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS edge_config_version_immutable ON edge_config_version;
CREATE TRIGGER edge_config_version_immutable BEFORE UPDATE OR DELETE ON edge_config_version
  FOR EACH ROW EXECUTE FUNCTION edge_config_version_immutable();
ALTER TABLE edge_config_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE edge_config_version FORCE ROW LEVEL SECURITY;
CREATE POLICY edge_config_version_tenant_access ON edge_config_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON edge_node TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON edge_config_version TO passage_app';
    EXECUTE 'GRANT UPDATE (applied_at) ON edge_config_version TO passage_app';
  END IF;
END
$$;
