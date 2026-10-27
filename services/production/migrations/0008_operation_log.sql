-- Wave 21 slice 9: finish and reopen an operation (FR-21.6).
-- Additive (expand): no existing row changes.

-- One row per `:finish` or `:reopen` that changed an operation's status: who, when,
-- from and to, and the Idempotency-Key that makes a retry replay. A route card's
-- `finished` flag is recorded on its journal line, not here. The operation reference
-- cascades like the trigger points': only an operation without feedback can be removed
-- by an order edit (FR-21.18).
CREATE TABLE IF NOT EXISTS production_operation_log (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL CHECK (oper_number > 0),
  action text NOT NULL CHECK (action IN ('finish', 'reopen')),
  from_status text NOT NULL CHECK (from_status IN ('open', 'started', 'finished')),
  to_status text NOT NULL CHECK (to_status IN ('open', 'started', 'finished')),
  actor text NOT NULL,
  trace_id text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, order_id, oper_number) REFERENCES production_operation (tenant_id, order_id, oper_number) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS production_operation_log_order ON production_operation_log (tenant_id, order_id, occurred_at);

ALTER TABLE production_operation_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_operation_log FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_operation_log_tenant_access ON production_operation_log;
CREATE POLICY production_operation_log_tenant_access ON production_operation_log AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_operation_log_legal_entity_scope ON production_operation_log;
CREATE POLICY production_operation_log_legal_entity_scope ON production_operation_log AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON production_operation_log TO passage_app';
  END IF;
END
$$;
