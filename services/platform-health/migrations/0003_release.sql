CREATE TABLE IF NOT EXISTS platform_environment (
  tenant_id uuid NOT NULL,
  name text NOT NULL,
  cluster_provider text CHECK (cluster_provider IN ('k3d', 'eks', 'gke', 'aks', 'baremetal')),
  argo_root_app text NOT NULL,
  promotion_policy text NOT NULL CHECK (promotion_policy IN ('auto', 'pr_approval')),
  soak_minutes int NOT NULL CHECK (soak_minutes >= 0),
  base_url text NOT NULL,
  PRIMARY KEY (tenant_id, name)
);
ALTER TABLE platform_environment ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_environment FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_environment_tenant_access ON platform_environment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS platform_release (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  service text NOT NULL,
  environment text NOT NULL,
  action text NOT NULL CHECK (action IN ('promote', 'rollback')),
  image_digest text NOT NULL CHECK (image_digest ~ '^sha256:[0-9a-f]{64}$'),
  previous_digest text NOT NULL CHECK (previous_digest ~ '^sha256:[0-9a-f]{64}$' AND previous_digest <> image_digest),
  git_sha text NOT NULL CHECK (git_sha ~ '^[0-9a-f]{40}$'),
  signed boolean NOT NULL,
  recorded_by_tenant uuid NOT NULL,
  recorded_by_user text NOT NULL,
  reason text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (recorded_by_tenant, idempotency_key),
  FOREIGN KEY (tenant_id, environment) REFERENCES platform_environment (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS platform_release_service_created_idx ON platform_release (tenant_id, service, created_at DESC, id DESC);
ALTER TABLE platform_release ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_release FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_release_tenant_access ON platform_release AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

SELECT set_config('passage.tenant_id', '00000000-0000-0000-0000-000000000000', true);
INSERT INTO platform_environment (tenant_id, name, argo_root_app, promotion_policy, soak_minutes, base_url) VALUES
  ('00000000-0000-0000-0000-000000000000', 'dev', 'root-dev', 'auto', 0, 'https://app.dev.passage.dev'),
  ('00000000-0000-0000-0000-000000000000', 'stage', 'root-stage', 'pr_approval', 0, 'https://app.stage.passage.dev'),
  ('00000000-0000-0000-0000-000000000000', 'prod', 'root-prod', 'pr_approval', 60, 'https://app.prod.passage.dev')
ON CONFLICT DO NOTHING;
