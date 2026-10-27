-- FR-22.7: Cancel and Decrease messages for surplus existing supply beyond the
-- coverage time fence.
ALTER TABLE action_message DROP CONSTRAINT IF EXISTS action_message_action_check;
ALTER TABLE action_message ADD CONSTRAINT action_message_action_check
  CHECK (action IN ('delay', 'postpone', 'advance', 'shortage', 'cancel', 'decrease'));

-- FR-22.10, BR-22.9: one firming request. response is the batch as the API
-- returned it, so a replay of the Idempotency-Key answers without firming again.
CREATE TABLE IF NOT EXISTS firming_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text NOT NULL,
  input_hash text NOT NULL,
  grouping text NOT NULL CHECK (grouping IN ('none', 'vendor', 'vendor-date', 'vendor-week')),
  firmed_count integer NOT NULL CHECK (firmed_count >= 1),
  response jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT firming_batch_idempotency_key UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE firming_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE firming_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY firming_batch_tenant_access ON firming_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A firmed planned order names the batch that firmed it and the target order
-- (purchase, production or transfer order) the owning context creates from
-- plannedorder.firmed.v1 with that id. Orders of one purchase group share it.
ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS firmed_batch_id uuid;
ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS firmed_target_type text
  CHECK (firmed_target_type IN ('purchase-order', 'production-order', 'transfer-order'));
ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS firmed_target_id uuid;
ALTER TABLE planned_order ADD CONSTRAINT planned_order_firmed_check CHECK (
  (status = 'firmed') = (firmed_batch_id IS NOT NULL AND firmed_target_type IS NOT NULL AND firmed_target_id IS NOT NULL));
ALTER TABLE planned_order ADD CONSTRAINT planned_order_firmed_batch_fk
  FOREIGN KEY (tenant_id, firmed_batch_id) REFERENCES firming_batch (tenant_id, id);
CREATE INDEX IF NOT EXISTS planned_order_firmed_target ON planned_order (tenant_id, firmed_target_id) WHERE firmed_target_id IS NOT NULL;
