-- camt.053 bank statement imports (FR-30.5) and the deductions workbench
-- (FR-30.17, BR-30.12), wave 30 slice 4.

-- A statement file is refused when its SHA-256 is already stored (AR-0003). Each
-- booked customer credit becomes a CAMT053 payment whose source_ref is
-- "<statement id>:<entry reference>", so payment's unique (source, source_ref)
-- keeps an entry from being imported twice even from another file (BR-30.7).
CREATE TABLE IF NOT EXISTS bank_statement_import (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  file_sha256 char(64) NOT NULL CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
  statement_count integer NOT NULL CHECK (statement_count >= 1),
  payment_count integer NOT NULL CHECK (payment_count >= 0),
  duplicate_count integer NOT NULL CHECK (duplicate_count >= 0),
  skipped_count integer NOT NULL CHECK (skipped_count >= 0),
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, file_sha256)
);
CREATE INDEX IF NOT EXISTS bank_statement_import_at_idx ON bank_statement_import (tenant_id, imported_at DESC);
ALTER TABLE bank_statement_import ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_statement_import FORCE ROW LEVEL SECURITY;
CREATE POLICY bank_statement_import_tenant_access ON bank_statement_import AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY bank_statement_import_legal_entity_scope ON bank_statement_import AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A deduction is the unexplained part of a short payment, moved off the short-paid
-- open item so it is aged apart from invoices and never dunned while open
-- (FR-30.17). OPEN -> MATCHED -> APPROVED | DENIED | SPLIT | WRITTEN_OFF (BR-30.12).
-- A SPLIT parent keeps its amount; its OPEN children (parent_id) sum to it. A
-- denial re-bills the amount as a DEDUCTION debit open item (rebill_item_id).
CREATE TABLE IF NOT EXISTS deduction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  payment_id uuid,
  parent_id uuid,
  deduction_type text NOT NULL CHECK (deduction_type IN ('PROMO', 'PRICE', 'SHORT_SHIP', 'DAMAGE', 'UNKNOWN')),
  reason_code text NOT NULL CHECK (length(reason_code) BETWEEN 1 AND 60),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  status text NOT NULL DEFAULT 'OPEN'
    CHECK (status IN ('OPEN', 'MATCHED', 'APPROVED', 'DENIED', 'SPLIT', 'WRITTEN_OFF')),
  claim_type text CHECK (claim_type IN ('REBATE_ACCRUAL', 'PROMOTION_FUND', 'CHARGEBACK')),
  claim_ref text CHECK (length(claim_ref) BETWEEN 1 AND 200),
  claim_remaining numeric(20,6) CHECK (claim_remaining >= 0),
  denial_reason text CHECK (denial_reason IN ('NOT_AUTHORIZED', 'EXPIRED', 'DUPLICATE')),
  decision_note text CHECK (length(decision_note) <= 1000),
  rebill_item_id uuid,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payment (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES deduction (tenant_id, id),
  FOREIGN KEY (tenant_id, rebill_item_id) REFERENCES open_item (tenant_id, id),
  CHECK ((claim_ref IS NULL) = (claim_type IS NULL) AND (claim_ref IS NULL) = (claim_remaining IS NULL)),
  CHECK (status NOT IN ('MATCHED', 'APPROVED') OR claim_ref IS NOT NULL),
  CHECK (status <> 'MATCHED' OR amount <= claim_remaining),
  CHECK ((status = 'DENIED') = (denial_reason IS NOT NULL)),
  CHECK ((status = 'DENIED') = (rebill_item_id IS NOT NULL)),
  CHECK ((status IN ('OPEN', 'MATCHED')) = (resolved_at IS NULL))
);
CREATE INDEX IF NOT EXISTS deduction_status_idx ON deduction (tenant_id, status, customer_id);
CREATE INDEX IF NOT EXISTS deduction_parent_idx ON deduction (tenant_id, parent_id);
CREATE INDEX IF NOT EXISTS deduction_item_idx ON deduction (tenant_id, open_item_id);
ALTER TABLE deduction ENABLE ROW LEVEL SECURITY;
ALTER TABLE deduction FORCE ROW LEVEL SECURITY;
CREATE POLICY deduction_tenant_access ON deduction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY deduction_legal_entity_scope ON deduction AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
