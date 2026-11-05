-- Inbound EDIFACT (#483). A partner may carry a UNB identity next to its ISA identity: the UNB sender (S002 0004,
-- with its 0007 qualifier, which may be empty) resolves the partner the way ISA05/ISA06 do, and edifact_contrl says
-- when its interchanges are answered with a CONTRL (REQUESTED: when UNB 0031 = 1; ALWAYS; NEVER).
ALTER TABLE trading_partner ADD COLUMN IF NOT EXISTS edifact_id text;
ALTER TABLE trading_partner ADD COLUMN IF NOT EXISTS edifact_qualifier text;
ALTER TABLE trading_partner ADD COLUMN IF NOT EXISTS edifact_contrl text;
ALTER TABLE trading_partner DROP CONSTRAINT IF EXISTS trading_partner_edifact_id_check;
ALTER TABLE trading_partner ADD CONSTRAINT trading_partner_edifact_id_check CHECK (edifact_id ~ '^[A-Za-z0-9._-]{1,35}$');
ALTER TABLE trading_partner DROP CONSTRAINT IF EXISTS trading_partner_edifact_qualifier_check;
ALTER TABLE trading_partner ADD CONSTRAINT trading_partner_edifact_qualifier_check CHECK (edifact_qualifier ~ '^[A-Za-z0-9]{1,4}$');
ALTER TABLE trading_partner DROP CONSTRAINT IF EXISTS trading_partner_edifact_contrl_check;
ALTER TABLE trading_partner ADD CONSTRAINT trading_partner_edifact_contrl_check CHECK (edifact_contrl IN ('REQUESTED', 'ALWAYS', 'NEVER'));
ALTER TABLE trading_partner DROP CONSTRAINT IF EXISTS trading_partner_edifact_complete_check;
ALTER TABLE trading_partner ADD CONSTRAINT trading_partner_edifact_complete_check
  CHECK ((edifact_id IS NULL) = (edifact_contrl IS NULL) AND (edifact_qualifier IS NULL OR edifact_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS trading_partner_edifact_sender
  ON trading_partner (tenant_id, edifact_id, coalesce(edifact_qualifier, ''), environment) WHERE edifact_id IS NOT NULL;

-- An interchange a transport brought from an authenticated partner but the receipt refused as a whole (syntax, or a
-- sender that is not the partner) is recorded with the reason, ack_status REJECTED, no messages and the negative 997
-- or CONTRL in ack_payload, so the answer can be delivered over the partner's transport. A refused interchange is not
-- a receipt: it never counts as a duplicate of a later corrected one.
ALTER TABLE interchange ADD COLUMN IF NOT EXISTS refusal text;
ALTER TABLE interchange DROP CONSTRAINT IF EXISTS interchange_refusal_check;
ALTER TABLE interchange ADD CONSTRAINT interchange_refusal_check
  CHECK (refusal IS NULL OR (char_length(refusal) BETWEEN 1 AND 1000 AND direction = 'IN' AND ack_status = 'REJECTED'));
