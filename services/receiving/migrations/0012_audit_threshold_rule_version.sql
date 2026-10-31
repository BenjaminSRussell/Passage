ALTER TABLE receiving_audit_policy
  ADD COLUMN IF NOT EXISTS threshold_amount numeric(20,6) CHECK (threshold_amount IS NULL OR threshold_amount > 0),
  ADD COLUMN IF NOT EXISTS threshold_currency text CHECK (threshold_currency IS NULL OR threshold_currency ~ '^[A-Z]{3}$'),
  ADD COLUMN IF NOT EXISTS rule_version int NOT NULL DEFAULT 1 CHECK (rule_version > 0);

ALTER TABLE receiving_audit_policy
  DROP CONSTRAINT IF EXISTS receiving_audit_policy_threshold_pair,
  ADD CONSTRAINT receiving_audit_policy_threshold_pair CHECK ((threshold_amount IS NULL) = (threshold_currency IS NULL));

ALTER TABLE receiving_audit_policy_history
  ADD COLUMN IF NOT EXISTS old_threshold_amount numeric(20,6),
  ADD COLUMN IF NOT EXISTS new_threshold_amount numeric(20,6),
  ADD COLUMN IF NOT EXISTS rule_version int;

ALTER TABLE receiving_vendor_invoice
  ADD COLUMN IF NOT EXISTS audit_rule_version int;
