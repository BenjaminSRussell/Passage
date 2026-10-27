-- Lockbox (BAI2) file imports (wave 30 slice 2). One row per distinct file content;
-- a second upload of the same bytes is refused with AR-0003 (BR-30.7).

CREATE TABLE IF NOT EXISTS lockbox_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  file_sha256 char(64) NOT NULL CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
  received_date date NOT NULL,
  account_count integer NOT NULL CHECK (account_count >= 0),
  payment_count integer NOT NULL CHECK (payment_count >= 0),
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, file_sha256)
);
ALTER TABLE lockbox_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE lockbox_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY lockbox_batch_tenant_access ON lockbox_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY lockbox_batch_legal_entity_scope ON lockbox_batch AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
