-- Wave 33 slice 4: incoming vendor certificates (FR-33.15, BR-33.13) and physical
-- samples (FR-33.20).

-- The certificate a quality association demands (certificate_required_default) is
-- copied onto the order when it is created, so the usage decision gates on it.
ALTER TABLE quality_association ADD COLUMN IF NOT EXISTS certificate_required text NOT NULL DEFAULT 'NONE'
  CHECK (certificate_required IN ('NONE','COA','COC','EN10204_31'));
ALTER TABLE quality_order ADD COLUMN IF NOT EXISTS certificate_required text NOT NULL DEFAULT 'NONE'
  CHECK (certificate_required IN ('NONE','COA','COC','EN10204_31'));

-- Certificate receipts and waivers of one order, append-only. The gate reads the
-- latest receipt of the required type, or a waiver of it (BR-33.13). An
-- out-of-specification receipt links the VENDOR NCR it raised.
CREATE TABLE IF NOT EXISTS quality_certificate (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quality_order_id uuid NOT NULL,
  cert_type text NOT NULL CHECK (cert_type IN ('COA','COC','EN10204_31')),
  document_ref text NOT NULL DEFAULT '' CHECK (char_length(document_ref) <= 200),
  certificate_values jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(certificate_values) = 'object'),
  in_spec boolean NOT NULL,
  deviations jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(deviations) = 'array'),
  waiver boolean NOT NULL DEFAULT false,
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  nonconformance_id uuid,
  recorded_by text NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (NOT waiver OR (reason <> '' AND certificate_values = '{}'::jsonb)),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id),
  FOREIGN KEY (tenant_id, nonconformance_id) REFERENCES quality_nonconformance (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_certificate_order ON quality_certificate (tenant_id, quality_order_id, recorded_at, id);
ALTER TABLE quality_certificate ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_certificate FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_certificate_tenant_access ON quality_certificate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Physical samples drawn for a quality order: PRIMARY, POOLED (mixed from primary
-- samples of the same order) and RESERVE (retention). The status ACTIVE /
-- DUE_FOR_DISPOSAL is derived from retention_expires_on on read.
CREATE TABLE IF NOT EXISTS quality_sample (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quality_order_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('PRIMARY','POOLED','RESERVE')),
  primary_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(primary_ids) = 'array'),
  quantity numeric(18,6) NOT NULL CHECK (quantity > 0),
  uom text NOT NULL CHECK (char_length(uom) BETWEEN 1 AND 10),
  container text NOT NULL DEFAULT '' CHECK (char_length(container) <= 60),
  storage_location text NOT NULL DEFAULT '' CHECK (char_length(storage_location) <= 60),
  condition text NOT NULL DEFAULT '' CHECK (char_length(condition) <= 120),
  batch_expires_on date,
  retention_years int NOT NULL CHECK (retention_years BETWEEN 0 AND 10),
  retention_expires_on date,
  recorded_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (kind <> 'RESERVE' OR batch_expires_on IS NOT NULL),
  CHECK ((batch_expires_on IS NULL) = (retention_expires_on IS NULL)),
  CHECK ((kind = 'POOLED') = (jsonb_array_length(primary_ids) > 0)),
  FOREIGN KEY (tenant_id, quality_order_id) REFERENCES quality_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_sample_order ON quality_sample (tenant_id, quality_order_id, created_at);
CREATE INDEX IF NOT EXISTS quality_sample_retention ON quality_sample (tenant_id, retention_expires_on) WHERE retention_expires_on IS NOT NULL;
ALTER TABLE quality_sample ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_sample FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_sample_tenant_access ON quality_sample AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
